package game

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"wtserver-go/internal/store"
	"wtserver-go/internal/testsupport"
	"wtserver-go/internal/transport"
)

type fakeUsers struct {
	testsupport.GameRepository
	mu              sync.Mutex
	slots           map[int]int
	equipment       map[string]int
	items           []store.Item
	loadout         store.CharacterSelection
	loginUser       string
	loginPass       string
	loginIDs        map[string]int
	rejectLogin     bool
	usedItem        int
	openedBox       int
	openedPack      int
	gifts           []store.Gift
	claimedGift     uint32
	registeredKills int
	upgradedItem    int
	upgradeSlot     byte
	upgradeID       uint16
	activeItems     map[int]bool
	charEquipment   map[int]store.CharacterEquipment
	equipmentSlots  [34]int
	expiredItems    []int
	authority       byte
}

func TestConnectionAdvertisedIPDoesNotExposeContainerAddress(t *testing.T) {
	tests := []struct {
		name       string
		local      net.Addr
		remote     net.Addr
		publicIP   netip.Addr
		advertised netip.Addr
		want       netip.Addr
	}{
		{
			name:       "docker host client",
			local:      &net.TCPAddr{IP: net.ParseIP("172.21.0.2"), Port: 40707},
			remote:     &net.TCPAddr{IP: net.ParseIP("172.21.0.1"), Port: 50000},
			publicIP:   netip.MustParseAddr("127.0.0.1"),
			advertised: netip.MustParseAddr("127.0.0.1"),
			want:       netip.MustParseAddr("127.0.0.1"),
		},
		{
			name:       "external client",
			local:      &net.TCPAddr{IP: net.ParseIP("10.0.0.2"), Port: 40707},
			remote:     &net.TCPAddr{IP: net.ParseIP("198.51.100.25"), Port: 50000},
			publicIP:   netip.MustParseAddr("203.0.113.10"),
			advertised: netip.MustParseAddr("10.0.0.2"),
			want:       netip.MustParseAddr("203.0.113.10"),
		},
		{
			name:       "native loopback client",
			local:      &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 40707},
			remote:     &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 50000},
			publicIP:   netip.MustParseAddr("203.0.113.10"),
			advertised: netip.MustParseAddr("127.0.0.1"),
			want:       netip.MustParseAddr("127.0.0.1"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{publicIP: test.publicIP, advertisedIP: test.advertised}
			if got := server.connectionAdvertisedIPForAddresses(test.local, test.remote); got != test.want {
				t.Fatalf("connection advertised IP = %s, want %s", got, test.want)
			}
		})
	}
}

type rosterCountingUsers struct {
	*fakeUsers
	countsMu           sync.Mutex
	itemLoads          int
	loadoutLoads       int
	charEquipmentLoads int
	equipmentLoads     int
}

func (u *rosterCountingUsers) Items(ctx context.Context, userID int) ([]store.Item, error) {
	u.countsMu.Lock()
	u.itemLoads++
	u.countsMu.Unlock()
	return u.fakeUsers.Items(ctx, userID)
}

func (u *rosterCountingUsers) CharacterSelection(ctx context.Context, userID int) (store.CharacterSelection, error) {
	u.countsMu.Lock()
	u.loadoutLoads++
	u.countsMu.Unlock()
	return u.fakeUsers.CharacterSelection(ctx, userID)
}

func (u *rosterCountingUsers) CharacterEquipment(ctx context.Context, userID int) (map[int]store.CharacterEquipment, error) {
	u.countsMu.Lock()
	u.charEquipmentLoads++
	u.countsMu.Unlock()
	return u.fakeUsers.CharacterEquipment(ctx, userID)
}

func (u *rosterCountingUsers) Equipment(ctx context.Context, userID int) ([34]int, error) {
	u.countsMu.Lock()
	u.equipmentLoads++
	u.countsMu.Unlock()
	return u.fakeUsers.Equipment(ctx, userID)
}

func (u *rosterCountingUsers) rosterLoads() [4]int {
	u.countsMu.Lock()
	defer u.countsMu.Unlock()
	return [4]int{u.itemLoads, u.loadoutLoads, u.charEquipmentLoads, u.equipmentLoads}
}

type awardUsers struct {
	*fakeUsers
	info        store.AccountSnapshot
	awardGold   int
	awardGP     int
	matchWins   int
	matchLosses int
	matchDraws  int
}

func (f *awardUsers) AccountSnapshot(context.Context, int) (store.AccountSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info, nil
}

func (f *awardUsers) CreditMatchRewards(_ context.Context, _ int, gold, gp int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.awardGold += gold
	f.awardGP += gp
	f.info.Gold += gold
	f.info.GP += gp
	return nil
}

func (f *awardUsers) RegisterMatchResult(_ context.Context, _ int, won bool) error {
	outcome := store.MatchLoss
	if won {
		outcome = store.MatchWin
	}
	return f.RegisterMatchOutcome(context.Background(), 0, outcome)
}

func (f *awardUsers) RegisterMatchOutcome(_ context.Context, _ int, outcome store.MatchOutcome) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch outcome {
	case store.MatchWin:
		f.matchWins++
		f.info.Wins++
	case store.MatchLoss:
		f.matchLosses++
		f.info.Losses++
	case store.MatchDraw:
		f.matchDraws++
		f.info.Draws++
	}
	return nil
}

func (f *awardUsers) AddPlayTime(_ context.Context, _ int, seconds uint32) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.info.PlayTimeSeconds += seconds
	return f.info.PlayTimeSeconds, nil
}

func TestMatchOutcomeIncludesDraws(t *testing.T) {
	tests := []struct {
		team, winner byte
		want         store.MatchOutcome
	}{
		{team: redTeam, winner: redTeam, want: store.MatchWin},
		{team: blueTeam, winner: redTeam, want: store.MatchLoss},
		{team: redTeam, winner: 0, want: store.MatchDraw},
	}
	for _, test := range tests {
		if got := matchOutcome(test.team, test.winner); got != test.want {
			t.Errorf("matchOutcome(%d, %d) = %d, want %d", test.team, test.winner, got, test.want)
		}
	}
}

func (f *fakeUsers) Login(_ context.Context, username, password string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loginUser = username
	f.loginPass = password
	if f.rejectLogin {
		return -1, nil
	}
	if userID := f.loginIDs[username]; userID > 0 {
		return userID, nil
	}
	return 42, nil
}

func TestUDPReadyDoesNotSynthesizeEnterGame(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	room := newRoom(0, RoomConfig{Capacity: 8, Time: 300})
	if _, ok := room.Add(harness.session); !ok {
		t.Fatal("add player")
	}
	beginTestMatch(room)
	harness.server.mu.Lock()
	harness.server.rooms[room.Number] = room
	harness.server.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{
			MessageID: RequestUDPReady,
			Payload:   []byte{1},
		})
	}()
	assertFrameID(t, harness.client, ReplyUDPReady)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("UDP-ready handler blocked sending an unsolicited enter-game ACK")
	}
	harness.session.mu.RLock()
	status := harness.session.RoomStatus
	harness.session.mu.RUnlock()
	if status != 1 {
		t.Fatalf("UDP-ready changed room status to %d, want 1", status)
	}

	done = make(chan error, 1)
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{MessageID: RequestEnterGame})
	}()
	// Coming from the room screen (status 1 after UDPREADY), the slot is
	// announced with 0x1617 before ENTERGAME (sub_342E1B60 adds the
	// scoreboard row only while the cached status is not yet 3).
	announce := assertFrameID(t, harness.client, ReplyChangeSlot)
	if announce.Payload[1] != 0 || announce.Payload[2] != 3 {
		t.Fatalf("late-join announcement = %x", announce.Payload)
	}
	enterGame := assertFrameID(t, harness.client, ReplyEnterGame)
	// 2012 ENTERGAME_ACK: slot, slot status 3, current round.
	if enterGame.Payload[1] != 0 || enterGame.Payload[2] != 3 || enterGame.Payload[3] != 1 {
		t.Fatalf("enter-game slot/status/round = %x", enterGame.Payload)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	harness.session.mu.RLock()
	status = harness.session.RoomStatus
	harness.session.mu.RUnlock()
	if status != 3 {
		t.Fatalf("enter-game changed room status to %d, want 3", status)
	}
}

func TestFullAITeamChangeReturnsStatus182(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	room := newRoom(0, RoomConfig{Capacity: 16, Mode: aiModeFlag | 2})
	if _, ok := room.Add(harness.session); !ok {
		t.Fatal("add human")
	}
	for row := 0; row < teamSlotCount; row++ {
		if bot := room.ProvisionBot(blueTeam, 1, false, 40708, row); bot == nil {
			t.Fatalf("add blue bot at row %d", row)
		}
	}
	harness.server.mu.Lock()
	harness.server.rooms[room.Number] = room
	harness.server.mu.Unlock()

	frame := dispatchAndRead(t, harness, RequestChangeTeam, ReplyChangeTeam, nil)
	if len(frame.Payload) != 16 || frame.Payload[0] != 182 {
		t.Fatalf("full-team reply = %x", frame.Payload)
	}
	if harness.session.RoomTeam != redTeam || harness.session.RoomTeamSlot != 0 {
		t.Fatalf("rejected change moved player to team/row %d/%d", harness.session.RoomTeam, harness.session.RoomTeamSlot)
	}
}

func TestEndGamePersistsPowerUserAwardBeforeSendingBalances(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	defer serverNet.Close()
	defer clientNet.Close()
	_ = clientNet.SetDeadline(time.Now().Add(3 * time.Second))
	loopback := netip.MustParseAddr("127.0.0.1")
	users := &awardUsers{
		fakeUsers: &fakeUsers{activeItems: map[int]bool{powerUserItemID: true}},
		info:      store.AccountSnapshot{Callsign: "Winner", Level: 7, Gold: 1000, GP: 2000, PlayTimeSeconds: 600},
	}
	server := NewServer("unused", 40707, 40708, loopback, loopback, users, slog.New(slog.NewTextHandler(io.Discard, nil)))
	session, err := newSession(transport.NewEncryptedConn(serverNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	session.setAuthenticated("winner", 42, "Winner", 7)
	room := newRoom(0, RoomConfig{Capacity: 8, WeaponFlags: 0x44})

	done := make(chan struct{})
	go func() {
		server.sendEndGame(room, 1, []MatchPlayer{{
			Session: session,
			Slot:    3,
			Team:    1,
			Stats:   MatchPlayerStats{Kills: 2},
		}}, 120)
		close(done)
	}()
	points := assertFrameID(t, transport.NewEncryptedConn(clientNet, nil), ReplyGamePoints)
	frame := assertFrameID(t, transport.NewEncryptedConn(clientNet, nil), ReplyEndGame)
	<-done
	assertNoFrame(t, clientNet)
	if users.awardGold != 180 || users.awardGP != 360 {
		t.Fatalf("persisted award = %d/%d", users.awardGold, users.awardGP)
	}
	if users.matchWins != 1 || users.matchLosses != 0 {
		t.Fatalf("persisted match result = %d wins/%d losses", users.matchWins, users.matchLosses)
	}
	if binary.LittleEndian.Uint32(frame.Payload[1:5]) != 1180 || binary.LittleEndian.Uint32(frame.Payload[9:13]) != 2360 ||
		binary.LittleEndian.Uint32(frame.Payload[18:22]) != 720 || session.playTimeSeconds() != 720 {
		t.Fatalf("end-game balances = %x", frame.Payload)
	}
	if binary.LittleEndian.Uint32(points.Payload[1:5]) != 1180 || binary.LittleEndian.Uint32(points.Payload[9:13]) != 2360 || binary.LittleEndian.Uint32(points.Payload[13:17]) != 12000000 {
		t.Fatalf("final game points = %x", points.Payload)
	}
	// The two empty Pride Battle names leave the weapon flags at byte 44 and
	// the 23-byte player record at byte 46.
	if binary.LittleEndian.Uint32(frame.Payload[5:9]) != uint32(store.LevelBaseThreshold(7)) ||
		frame.Payload[44] != 0x44 || frame.Payload[45] != 1 || frame.Payload[46] != 3 ||
		binary.LittleEndian.Uint32(frame.Payload[47:51]) != 120 ||
		binary.LittleEndian.Uint32(frame.Payload[51:55]) != 240 ||
		binary.LittleEndian.Uint32(frame.Payload[55:59]) != 60 ||
		binary.LittleEndian.Uint32(frame.Payload[59:63]) != 120 ||
		binary.LittleEndian.Uint32(frame.Payload[63:67]) != endGameBonusPowerUser {
		t.Fatalf("end-game record = %x", frame.Payload)
	}
}

func TestPowerUserInfoUsesInventoryEntitlement(t *testing.T) {
	users := &fakeUsers{activeItems: map[int]bool{powerUserItemID: true}}
	harness := newFailSoftHarness(t, users)
	frame := dispatchAndRead(t, harness, RequestGetPowerUser, ReplyGetPowerUser, nil)
	if binary.LittleEndian.Uint32(frame.Payload[1:5]) != 1 || binary.LittleEndian.Uint32(frame.Payload[5:9]) != 1 ||
		binary.LittleEndian.Uint32(frame.Payload[14:18]) != PowerUserFeatureMask {
		t.Fatalf("Power User was not enabled from inventory: %x", frame.Payload)
	}
}

func TestEventRewardListBootstrapRequestGetsEmptyReply(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	frame := dispatchAndRead(t, harness, RequestEventRewardList, ReplyEventRewardList, nil)
	if len(frame.Payload) < 2 || !channelAllZero(frame.Payload) {
		t.Fatalf("event reward list = %x", frame.Payload)
	}
}

func TestItemListSnapshotIncludesPowerMasterTemporaryItems(t *testing.T) {
	users := &fakeUsers{}
	harness := newFailSoftHarness(t, users)
	room := newRoom(0, RoomConfig{Capacity: 8})
	room.SetPowerMaster(true)
	if _, ok := room.Add(harness.session); !ok {
		t.Fatal("add Power Master player")
	}
	harness.server.mu.Lock()
	harness.server.rooms[room.Number] = room
	harness.server.mu.Unlock()

	frame := dispatchAndRead(t, harness, RequestItemList, ReplyItemList, nil)
	if frame.Payload[1] != byte(len(powerRoomItems)) {
		t.Fatalf("Power Room inventory count = %d, want %d: %x", frame.Payload[1], len(powerRoomItems), frame.Payload)
	}
	if binary.LittleEndian.Uint32(frame.Payload[2:6]) != powerRoomItems[0].UniqueID ||
		binary.LittleEndian.Uint16(frame.Payload[6:8]) != powerRoomItems[0].ItemID ||
		binary.LittleEndian.Uint32(frame.Payload[17:21]) != 0x0002ffff {
		t.Fatalf("Power Room inventory record = %x", frame.Payload)
	}
	wireLength := 3 + len(powerRoomItems)*19
	if len(frame.Payload) < wireLength {
		t.Fatalf("Power Room inventory length = %d, want at least %d", len(frame.Payload), wireLength)
	}
	if frame.Payload[wireLength-1] != 0 {
		t.Fatalf("Power Room inventory missing trailing refresh flag: %x", frame.Payload)
	}
}

func TestRoomCreationUsesPowerMasterInventoryEntitlement(t *testing.T) {
	users := &fakeUsers{activeItems: map[int]bool{powerMasterItemID: true}}
	harness := newFailSoftHarness(t, users)
	roomRequest := []byte{
		8, 'R', 0, 'o', 0, 'o', 0, 'm', 0, 0,
		3, 2, 0, 0, 0, 5, 0, 0x58, 2, 100, 0, 2, 0, 0xc0, 0, 8,
	}
	done := make(chan error, 1)
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{
			MessageID: RequestCreateRoom,
			Payload:   roomRequest,
		})
	}()
	assertFrameID(t, harness.client, ReplyCreateRoom)
	roster := assertFrameID(t, harness.client, ReplyRoomChars)
	powerItems := assertFrameID(t, harness.client, ReplyPowerMasterItems)
	assertFrameID(t, harness.client, ReplyFieldList)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	room := harness.server.roomByNumber(0)
	if room == nil || !room.Snapshot().PowerMaster {
		t.Fatal("room was not marked as Power Master from inventory item 10092")
	}
	rulesOffset := 6 + int(roster.Payload[5])
	rulesOffset += 1 + int(roster.Payload[rulesOffset]) + 1 + 4
	equipmentCount := int(roster.Payload[rulesOffset+1])
	if roster.Payload[rulesOffset+14+equipmentCount] != 1 {
		t.Fatalf("room roster omitted Power Master marker: %x", roster.Payload)
	}
	if len(powerItems.Payload) < 3+len(powerRoomItems)*8 || powerItems.Payload[1] != 1 ||
		int(powerItems.Payload[2]) != len(powerRoomItems) {
		t.Fatalf("Power Room item pack = %x", powerItems.Payload)
	}
}

func TestAIEndGamePersistsAwardsWithoutCompetitiveResult(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	defer serverNet.Close()
	defer clientNet.Close()
	_ = clientNet.SetDeadline(time.Now().Add(3 * time.Second))
	loopback := netip.MustParseAddr("127.0.0.1")
	users := &awardUsers{
		fakeUsers: &fakeUsers{},
		info:      store.AccountSnapshot{Callsign: "AI Player", Level: 61, Gold: 1000, GP: 0},
	}
	server := NewServer("unused", 40707, 40708, loopback, loopback, users, slog.New(slog.NewTextHandler(io.Discard, nil)))
	session, err := newSession(transport.NewEncryptedConn(serverNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	session.setAuthenticated("ai-player", 42, "AI Player", 61)
	room := newRoom(0, RoomConfig{Capacity: 8, Mode: 0x040002})
	slot, ok := room.Add(session)
	if !ok {
		t.Fatal("add AI-match player")
	}
	server.mu.Lock()
	server.rooms[room.Number] = room
	server.mu.Unlock()

	done := make(chan struct{})
	go func() {
		server.sendEndGame(room, 1, []MatchPlayer{{
			Session: session,
			Slot:    slot,
			Team:    1,
			Stats:   MatchPlayerStats{Kills: 40, Deaths: 2},
		}}, 120)
		close(done)
	}()
	conn := transport.NewEncryptedConn(clientNet, nil)
	assertFrameID(t, conn, ReplyGamePoints)
	assertFrameID(t, conn, ReplyEndGame)
	<-done
	assertNoFrame(t, clientNet)
	if users.awardGold == 0 || users.awardGP == 0 {
		t.Fatalf("AI-match award was not persisted: %d/%d", users.awardGold, users.awardGP)
	}
	if users.matchWins != 0 || users.matchLosses != 0 || users.info.Wins != 0 || users.info.Losses != 0 {
		t.Fatalf("AI match altered competitive result: %+v", users)
	}
}

func assertNoFrame(t *testing.T, connection net.Conn) {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	frame, err := transport.NewEncryptedConn(connection, nil).ReadFrame()
	if err == nil {
		t.Fatalf("unexpected frame %#x after end-game result", frame.MessageID)
	}
	var networkErr net.Error
	if !errors.As(err, &networkErr) || !networkErr.Timeout() {
		t.Fatalf("check for unexpected end-game frame: %v", err)
	}
}

func TestSessionAccumulatesPlayTimeForEndGameProgress(t *testing.T) {
	session := &Session{}
	if got := session.addPlayTime(300); got != 300 {
		t.Fatalf("first play-time update = %d, want 300", got)
	}
	if got := session.addPlayTime(120); got != 420 || session.playTimeSeconds() != 420 {
		t.Fatalf("cumulative play-time update = %d/%d, want 420", got, session.playTimeSeconds())
	}
}

func TestRosterStateIncludesSelectedCharacterItems(t *testing.T) {
	redEquipment := store.CharacterEquipment{}
	redEquipment[0] = 2_000_031_256
	redEquipment[1] = 2_000_031_271
	redEquipment[10] = 2_000_033_135
	blueEquipment := store.CharacterEquipment{}
	blueEquipment[0] = 2_000_030_048
	users := &fakeUsers{
		items: []store.Item{
			{ID: 30030, Type: 1},
			{ID: 30048, Type: 1},
		},
		loadout: store.CharacterSelection{RedCharacter: 30030, BlueCharacter: 30048},
		charEquipment: map[int]store.CharacterEquipment{
			30030: redEquipment,
			30048: blueEquipment,
		},
	}
	harness := newFailSoftHarness(t, users)
	state := harness.server.loadRosterState(context.Background(), harness.session, false)
	if state.RedCharacter != 30030 || state.BlueCharacter != 30048 {
		t.Fatalf("roster characters = %d/%d", state.RedCharacter, state.BlueCharacter)
	}
	if state.RedCharItems[0] != 31256 || state.RedCharItems[1] != 31271 || state.RedCharItems[10] != 33135 {
		t.Fatalf("red character items = %v", state.RedCharItems)
	}
	if state.BlueCharItems[0] != 30048 {
		t.Fatalf("blue character items = %v", state.BlueCharItems)
	}
}

func TestRosterPayloadCachesExistingMemberState(t *testing.T) {
	users := &rosterCountingUsers{fakeUsers: &fakeUsers{items: []store.Item{
		{ID: 30005, Type: 1},
		{ID: 30006, Type: 1},
	}}}
	loopback := netip.MustParseAddr("127.0.0.1")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer("unused", 40707, 40708, loopback, loopback, users, logger)
	room := newRoom(0, RoomConfig{Capacity: 8})
	server.mu.Lock()
	server.rooms[room.Number] = room
	server.mu.Unlock()

	newMember := func(userID int, name string) *Session {
		return &Session{
			LoginName: name, Callsign: name, UserID: userID, UserLevel: 7,
			RoomNumber: -1, RoomSlot: -1, RoomTeam: 0xff, RoomTeamSlot: 0xff,
		}
	}
	first := newMember(1, "first")
	second := newMember(2, "second")
	if _, ok := room.Add(first); !ok {
		t.Fatal("add first member")
	}
	if _, ok := room.Add(second); !ok {
		t.Fatal("add second member")
	}
	if _, err := server.rosterPayload(context.Background(), room); err != nil {
		t.Fatal(err)
	}
	if got := users.rosterLoads(); got != [4]int{2, 2, 2, 2} {
		t.Fatalf("initial roster loads = %v, want one load per member", got)
	}

	third := newMember(3, "third")
	if _, ok := room.Add(third); !ok {
		t.Fatal("add third member")
	}
	if _, err := server.rosterPayload(context.Background(), room); err != nil {
		t.Fatal(err)
	}
	if got := users.rosterLoads(); got != [4]int{3, 3, 3, 3} {
		t.Fatalf("roster loads after join = %v, want only the new member loaded", got)
	}

	if err := server.leaveRoom(third, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := room.Add(third); !ok {
		t.Fatal("re-add third member")
	}
	if _, err := server.rosterPayload(context.Background(), room); err != nil {
		t.Fatal(err)
	}
	if got := users.rosterLoads(); got != [4]int{4, 4, 4, 4} {
		t.Fatalf("roster loads after re-entry = %v, want departed member reloaded", got)
	}
}

func TestEquipmentSaveInvalidatesRosterState(t *testing.T) {
	users := &rosterCountingUsers{fakeUsers: &fakeUsers{}}
	harness := newFailSoftHarness(t, users)
	if state := harness.server.loadRosterState(context.Background(), harness.session, true); state.RedCharacter == 0 {
		t.Fatal("initial roster state was not loaded")
	}

	payload := make([]byte, 90)
	payload[0] = 1
	dispatchAndRead(t, harness, RequestEquip, ReplyEquip, payload)
	harness.server.loadRosterState(context.Background(), harness.session, true)
	if got := users.rosterLoads(); got != [4]int{2, 2, 2, 2} {
		t.Fatalf("roster loads after equipment save = %v, want cache refresh", got)
	}
}

func (f *fakeUsers) AccountSnapshot(context.Context, int) (store.AccountSnapshot, error) {
	return store.AccountSnapshot{
		Callsign: "Player", Authority: f.authority, Level: 7, Gold: 100, WC: 200, Cash: 300,
	}, nil
}
func (f *fakeUsers) Items(context.Context, int) ([]store.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Item(nil), f.items...), nil
}

func (f *fakeUsers) TakeExpiredItems(_ context.Context, _ int, limit int) ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit > len(f.expiredItems) {
		limit = len(f.expiredItems)
	}
	items := append([]int(nil), f.expiredItems[:limit]...)
	f.expiredItems = f.expiredItems[limit:]
	return items, nil
}
func (f *fakeUsers) HasActiveItem(_ context.Context, _ int, itemID int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activeItems[itemID], nil
}
func (f *fakeUsers) CommitPurchase(_ context.Context, request store.PurchaseOrder) (store.PurchaseReceipt, error) {
	return store.PurchaseReceipt{Success: true, Expiry: "20300102030405", Gold: 100, WC: 200, Cash: 300}, nil
}
func (f *fakeUsers) PurchaseBasket(_ context.Context, requests []store.PurchaseOrder) (store.BasketPurchaseResult, error) {
	items := make([]store.PurchaseReceipt, len(requests))
	for index := range items {
		items[index] = store.PurchaseReceipt{Success: true, Expiry: "20991231235959"}
	}
	return store.BasketPurchaseResult{Success: true, Items: items}, nil
}
func (f *fakeUsers) DailyShopItems(context.Context, time.Time) ([2]uint16, error) {
	return [2]uint16{10038, 10087}, nil
}
func (f *fakeUsers) GiftCount(context.Context, int) (int, error) {
	return len(f.gifts), nil
}
func (f *fakeUsers) Gifts(context.Context, int, byte, int, int) (store.GiftPage, error) {
	return store.GiftPage{Gifts: append([]store.Gift(nil), f.gifts...), Total: len(f.gifts), TotalPages: min(1, len(f.gifts))}, nil
}
func (f *fakeUsers) GiftDetail(_ context.Context, _ int, giftID uint32, _ byte) (store.Gift, bool, error) {
	for index := range f.gifts {
		if f.gifts[index].ID == giftID {
			f.gifts[index].Status = max(f.gifts[index].Status, byte(1))
			return f.gifts[index], true, nil
		}
	}
	return store.Gift{}, false, nil
}
func (f *fakeUsers) ClaimGift(_ context.Context, _ int, giftID uint32) (store.GiftClaimResult, error) {
	f.claimedGift = giftID
	for index := range f.gifts {
		if f.gifts[index].ID == giftID && f.gifts[index].Status != 2 {
			f.gifts[index].Status = 2
			return store.GiftClaimResult{Success: true, Gift: f.gifts[index], Expiry: "20300102030405", Gold: 100, Cash: 300}, nil
		}
	}
	return store.GiftClaimResult{Gold: 100, Cash: 300}, nil
}
func (f *fakeUsers) ConsumeInventoryItem(_ context.Context, _ int, itemID int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.usedItem = itemID
	for index, item := range f.items {
		if item.ID == itemID {
			f.items = append(f.items[:index], f.items[index+1:]...)
			break
		}
	}
	return true, nil
}
func (f *fakeUsers) ClaimPackageContents(_ context.Context, _ int, itemID int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openedPack = itemID
	return true, nil
}
func (f *fakeUsers) DrawLuckyBox(_ context.Context, _ int, itemID, uniqueID int) (store.LuckyBoxReceipt, error) {
	f.mu.Lock()
	f.openedBox = itemID
	f.mu.Unlock()
	return store.LuckyBoxReceipt{Success: true, BoxUnique: 1, RewardItem: 19601, Days: 7, Remaining: 4}, nil
}

func (f *fakeUsers) SetItemUpgrade(_ context.Context, _ int, itemID int, slot byte, upgradeID uint16) (bool, error) {
	f.mu.Lock()
	f.upgradedItem = itemID
	f.upgradeSlot = slot
	f.upgradeID = upgradeID
	f.mu.Unlock()
	return true, nil
}
func (f *fakeUsers) SellItem(context.Context, int, int) (store.SellResult, error) {
	return store.SellResult{Success: true, Gold: 10, Cash: 20}, nil
}
func (f *fakeUsers) RefundItem(context.Context, int, int) (store.RefundResult, error) {
	return store.RefundResult{Success: true}, nil
}
func (f *fakeUsers) SetEquipment(_ context.Context, field string, value, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.equipment == nil {
		f.equipment = make(map[string]int)
	}
	f.equipment[field] = value
	return nil
}
func (f *fakeUsers) Equipment(context.Context, int) ([34]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.equipmentSlots, nil
}
func (f *fakeUsers) RecordKill(context.Context, int, int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registeredKills++
	return nil
}
func (f *fakeUsers) SetCharacterSlot(_ context.Context, _ int, slot, characterID int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.slots == nil {
		f.slots = make(map[int]int)
	}
	f.slots[slot] = characterID
	return nil
}
func (f *fakeUsers) CharacterSlots(context.Context, int) (map[int]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make(map[int]int, len(f.slots))
	for slot, characterID := range f.slots {
		result[slot] = characterID
	}
	return result, nil
}
func (f *fakeUsers) ReplaceCharacterSlots(_ context.Context, _ int, slots map[int]int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.slots = make(map[int]int, len(slots))
	for slot, characterID := range slots {
		f.slots[slot] = characterID
	}
	return nil
}
func (f *fakeUsers) CharacterEquipment(context.Context, int) (map[int]store.CharacterEquipment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make(map[int]store.CharacterEquipment, len(f.charEquipment))
	for characterID, equipment := range f.charEquipment {
		result[characterID] = equipment
	}
	return result, nil
}
func (f *fakeUsers) SetCharacterEquipment(_ context.Context, _ int, characterID int, equipment store.CharacterEquipment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.charEquipment == nil {
		f.charEquipment = make(map[int]store.CharacterEquipment)
	}
	f.charEquipment[characterID] = equipment
	return nil
}
func (f *fakeUsers) CharacterSelection(context.Context, int) (store.CharacterSelection, error) {
	return f.loadout, nil
}
func (f *fakeUsers) SaveCharacterSelection(_ context.Context, _ int, loadout store.CharacterSelection) error {
	f.loadout = loadout
	return nil
}

func TestGameLoginAndUDPBootstrap(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	users := &fakeUsers{authority: 13}
	server := NewServer("unused", 40707, 40708, netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("203.0.113.152"), users, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.handleConnection(ctx, serverNet) }()
	client := transport.NewEncryptedConn(clientNet, nil)

	login := []byte{0, 0, 0, 4, 'u', 's', 'e', 'r', 4, 'p', 'a', 's', 's'}
	writeDone := make(chan error, 1)
	go func() {
		_, err := client.WriteMessage(RequestChannelLogin, login)
		writeDone <- err
	}()
	reply, err := client.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if reply.MessageID != ReplyChannelLogin || reply.Payload[0] != 0 || binary.LittleEndian.Uint32(reply.Payload[3:7]) != 42 {
		t.Fatalf("unexpected login reply: id=%#x payload=%x", reply.MessageID, reply.Payload)
	}
	users.mu.Lock()
	if users.loginUser != "user" || users.loginPass != "pass" {
		t.Fatalf("forwarded credentials = %q/%q", users.loginUser, users.loginPass)
	}
	users.mu.Unlock()
	sessions := server.channelSessions()
	if len(sessions) != 1 || sessions[0].UserAuthority != 13 {
		t.Fatalf("authenticated session authority count/value = %d/%v, want 1/13", len(sessions), sessions)
	}

	serverSocket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer serverSocket.Close()
	clientSocket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer clientSocket.Close()
	registration := make([]byte, 16)
	registration[0] = 2
	binary.LittleEndian.PutUint32(registration[6:10], 42)
	registrationDone := make(chan error, 1)
	go func() {
		registrationDone <- server.handleUDP(ctx, serverSocket, clientSocket.LocalAddr().(*net.UDPAddr), registration, nil)
	}()
	_ = clientSocket.SetReadDeadline(time.Now().Add(2 * time.Second))
	echo := make([]byte, 16)
	if _, _, err := clientSocket.ReadFromUDP(echo); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(echo, registration) {
		t.Fatalf("UDP registration echo = %x, want %x", echo, registration)
	}
	udpAddressReply, err := client.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if udpAddressReply.MessageID != ReplyUDPAddress {
		t.Fatalf("UDP registration TCP reply = %#x, want %#x", udpAddressReply.MessageID, ReplyUDPAddress)
	}
	if err := <-registrationDone; err != nil {
		t.Fatal(err)
	}

	go func() {
		_, err := client.WriteMessage(RequestUDPSuccess, nil)
		writeDone <- err
	}()
	udpReply, err := client.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if udpReply.MessageID != ReplyUDPSuccess || udpReply.Payload[1] != 127 || binary.BigEndian.Uint16(udpReply.Payload[5:7]) != 40707 {
		t.Fatalf("unexpected UDP reply: id=%#x payload=%x", udpReply.MessageID, udpReply.Payload)
	}

	go func() {
		_, err := client.WriteMessage(RequestEventShop, nil)
		writeDone <- err
	}()
	assertFrameID(t, client, ReplyEventShop)
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}

	// A malformed request is dropped without invalidating the encrypted channel.
	go func() {
		_, err := client.WriteMessage(RequestCreateRoom, nil)
		if err == nil {
			_, err = client.WriteMessage(RequestCharInfo, nil)
		}
		writeDone <- err
	}()
	assertFrameID(t, client, ReplyCharInfo)
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}

	_ = client.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("game handler: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("game handler did not stop")
	}
}

func TestDeletedSyncAndUseItemRoutesMatchProtocol(t *testing.T) {
	users := &fakeUsers{items: []store.Item{
		{ID: 15005, Type: 0, Expiry: "permanent"},
		{ID: 15005, Type: 0, Expiry: "permanent"},
	}, expiredItems: []int{7777}}
	harness := newFailSoftHarness(t, users)

	deleted := dispatchAndRead(t, harness, RequestDeletedItems, ReplyDeletedItems, nil)
	if deleted.Payload[0] != 0 || deleted.Payload[1] != 1 ||
		binary.LittleEndian.Uint32(deleted.Payload[2:6]) != 2_000_007_777 ||
		binary.LittleEndian.Uint16(deleted.Payload[6:8]) != 7777 {
		t.Fatalf("deleted-item sync payload = %x", deleted.Payload)
	}

	generic := make([]byte, 15)
	binary.LittleEndian.PutUint32(generic[0:4], 2_000_015_005)
	binary.LittleEndian.PutUint16(generic[4:6], 15005)
	generic[10] = 4
	copy(generic[11:], []byte{'O', 0, 'K', 0})
	used := dispatchAndRead(t, harness, RequestUseItem, ReplyUseItem, generic)
	if used.Payload[0] != 0 ||
		binary.LittleEndian.Uint32(used.Payload[1:5]) != 100 ||
		binary.LittleEndian.Uint32(used.Payload[5:9]) != 300 ||
		binary.LittleEndian.Uint32(used.Payload[9:13]) != 0 ||
		binary.LittleEndian.Uint32(used.Payload[17:21]) != uint32(store.NextLevelThreshold(7)) ||
		used.Payload[21] != 7 ||
		binary.LittleEndian.Uint16(used.Payload[26:28]) != 1 ||
		binary.LittleEndian.Uint16(used.Payload[28:30]) != 15005 ||
		used.Payload[34] != 4 ||
		!bytes.Equal(used.Payload[35:39], []byte{'O', 0, 'K', 0}) ||
		binary.LittleEndian.Uint32(used.Payload[39:43]) != 200 {
		t.Fatalf("generic use reply = %x", used.Payload)
	}

	bigHitBox := make([]byte, 4)
	binary.LittleEndian.PutUint32(bigHitBox, 2_000_017_019)
	box := dispatchAndRead(t, harness, RequestUseBigHitBox, ReplyUseBigHitBox, bigHitBox)
	if box.Payload[0] != 0 || binary.LittleEndian.Uint32(box.Payload[1:5]) != 2_000_017_019 ||
		binary.LittleEndian.Uint16(box.Payload[5:7]) != 19601 || binary.LittleEndian.Uint16(box.Payload[7:9]) != 7 ||
		binary.LittleEndian.Uint16(box.Payload[11:13]) != 0 || binary.LittleEndian.Uint16(box.Payload[13:15]) != 0 ||
		box.Payload[15] != 0 || binary.LittleEndian.Uint32(box.Payload[16:20]) != 100 {
		t.Fatalf("big-hit-box reply = %x", box.Payload)
	}
	taken := dispatchAndRead(t, harness, RequestGetBigHitBox, ReplyGetBigHitBox, nil)
	if binary.LittleEndian.Uint32(taken.Payload[1:5]) != 2_000_017_019 ||
		binary.LittleEndian.Uint16(taken.Payload[5:7]) != 4 ||
		binary.LittleEndian.Uint16(taken.Payload[7:9]) != 19601 ||
		binary.LittleEndian.Uint16(taken.Payload[9:11]) != 7 {
		t.Fatalf("big-hit-box take reply = %x", taken.Payload)
	}

	users.mu.Lock()
	defer users.mu.Unlock()
	if users.usedItem != 15005 || users.openedBox != 17019 || users.openedPack != 0 {
		t.Fatalf("inventory routes: used=%d box=%d package=%d", users.usedItem, users.openedBox, users.openedPack)
	}
}

func TestGameLoginRejectsInvalidPassword(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	defer serverNet.Close()
	defer clientNet.Close()
	loopback := netip.MustParseAddr("127.0.0.1")
	users := &fakeUsers{rejectLogin: true}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer("unused", 40707, 40708, loopback, loopback, users, logger)
	session, err := newSession(transport.NewEncryptedConn(serverNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- server.handleLogin(context.Background(), session, []byte{
			0, 0, 0, 4, 'u', 's', 'e', 'r', 5, 'w', 'r', 'o', 'n', 'g',
		})
	}()
	reply, err := transport.NewEncryptedConn(clientNet, nil).ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if reply.MessageID != ReplyChannelLogin || reply.Payload[0] != 23 || binary.LittleEndian.Uint32(reply.Payload[3:7]) != 0 {
		t.Fatalf("invalid-password reply: id=%#x payload=%x", reply.MessageID, reply.Payload)
	}
}

func TestExplicitExitReturnsChannelRoster(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer("unused", 40707, 40708, netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("203.0.113.152"), &fakeUsers{}, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.handleConnection(ctx, serverNet) }()
	client := transport.NewEncryptedConn(clientNet, nil)
	_ = clientNet.SetDeadline(time.Now().Add(5 * time.Second))

	login := []byte{0, 0, 0, 4, 'u', 's', 'e', 'r', 4, 'p', 'a', 's', 's'}
	if _, err := client.WriteMessage(RequestChannelLogin, login); err != nil {
		t.Fatal(err)
	}
	assertFrameID(t, client, ReplyChannelLogin)
	roomRequest := []byte{
		8, 'R', 0, 'o', 0, 'o', 0, 'm', 0, 0,
		3, 2, 0, 0, 0, 5, 0, 0x58, 2, 100, 0, 2, 0, 0xc0, 0, 8,
	}
	if _, err := client.WriteMessage(RequestCreateRoom, roomRequest); err != nil {
		t.Fatal(err)
	}
	assertFrameID(t, client, ReplyCreateRoom)
	assertFrameID(t, client, ReplyRoomChars)
	assertFrameID(t, client, ReplyFieldList)

	if _, err := client.WriteMessage(RequestExitRoom, nil); err != nil {
		t.Fatal(err)
	}
	assertFrameID(t, client, ReplyExitRoom)
	assertFrameID(t, client, ReplyFieldExit)
	assertFrameID(t, client, ReplyJoinAllow)
	channel := assertFrameID(t, client, ReplyChannelChars)
	if len(channel.Payload) < 3 || channel.Payload[2] != 1 {
		t.Fatalf("channel roster after exit = %x", channel.Payload)
	}

	_ = client.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("game handler: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("game handler did not stop")
	}
}

func TestTwoPlayerRoomAndF1BotFlow(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	users := &fakeUsers{loginIDs: map[string]int{"one": 1, "two": 2}}
	server := NewServer("unused", 40707, 40708, netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("203.0.113.152"), users, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type peer struct {
		connection net.Conn
		wire       *transport.EncryptedConn
		done       chan error
	}
	newPeer := func() peer {
		serverNet, clientNet := net.Pipe()
		_ = clientNet.SetDeadline(time.Now().Add(5 * time.Second))
		done := make(chan error, 1)
		go func() { done <- server.handleConnection(ctx, serverNet) }()
		return peer{connection: clientNet, wire: transport.NewEncryptedConn(clientNet, nil), done: done}
	}
	first, second := newPeer(), newPeer()
	defer first.connection.Close()
	defer second.connection.Close()

	login := func(p peer, username string) {
		t.Helper()
		payload := append([]byte{0, 0, 0, byte(len(username))}, []byte(username)...)
		payload = append(payload, 4, 'p', 'a', 's', 's')
		if _, err := p.wire.WriteMessage(RequestChannelLogin, payload); err != nil {
			t.Fatal(err)
		}
		assertFrameID(t, p.wire, ReplyChannelLogin)
	}
	login(first, "one")
	login(second, "two")
	type sequenceResult struct {
		frames   []transport.Frame
		err      error
		expected uint16
		got      uint16
	}
	readAsync := func(wire *transport.EncryptedConn, ids []uint16) <-chan sequenceResult {
		result := make(chan sequenceResult, 1)
		go func() {
			frames := make([]transport.Frame, 0, len(ids))
			for _, id := range ids {
				frame, err := wire.ReadFrame()
				if err != nil {
					result <- sequenceResult{err: err}
					return
				}
				if frame.MessageID != id {
					result <- sequenceResult{expected: id, got: frame.MessageID}
					return
				}
				frames = append(frames, frame)
			}
			result <- sequenceResult{frames: frames}
		}()
		return result
	}
	readBoth := func(firstIDs, secondIDs []uint16) ([]transport.Frame, []transport.Frame) {
		firstResult, secondResult := readAsync(first.wire, firstIDs), readAsync(second.wire, secondIDs)
		a, b := <-firstResult, <-secondResult
		for _, result := range []sequenceResult{a, b} {
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.expected != 0 {
				t.Fatalf("message ID = %#x, want %#x", result.got, result.expected)
			}
		}
		return a.frames, b.frames
	}

	roomRequest := []byte{
		8, 'R', 0, 'o', 0, 'o', 0, 'm', 0, 0,
		3, 2, 0, 0, 0, 5, 0, 0x58, 2, 100, 0, 2, 0, 0xc0, 0, 8,
	}
	if _, err := first.wire.WriteMessage(RequestCreateRoom, roomRequest); err != nil {
		t.Fatal(err)
	}
	assertFrameID(t, first.wire, ReplyCreateRoom)
	assertFrameID(t, first.wire, ReplyRoomChars)
	readBoth(
		[]uint16{ReplyFieldList},
		[]uint16{ReplyChannelChars, ReplyFieldCreate, ReplyJoinAllow},
	)

	if _, err := second.wire.WriteMessage(RequestQuickEnter, nil); err != nil {
		t.Fatal(err)
	}
	readBoth([]uint16{ReplyEnterRoom}, []uint16{ReplyEnterRoom, ReplyRoomChars})

	if _, err := first.wire.WriteMessage(RequestAddAISlot, []byte{6, 0, 2, 2}); err != nil {
		t.Fatal(err)
	}
	aiFramesFirst, aiFramesSecond := readBoth([]uint16{ReplyAddAISlot}, []uint16{ReplyAddAISlot})
	aiFirst, aiSecond := aiFramesFirst[0], aiFramesSecond[0]
	if !bytes.Equal(aiFirst.Payload, aiSecond.Payload) || aiFirst.Payload[1] != 2 || aiFirst.Payload[2] != 6 || aiFirst.Payload[4] != 1 || aiFirst.Payload[21] != 2 {
		t.Fatalf("AI replies differ or have wrong F1 layout: %x / %x", aiFirst.Payload, aiSecond.Payload)
	}
	bot := server.roomByNumber(0).SessionAt(2)
	if bot == nil || bot.RoomTeam != 2 || bot.RoomTeamSlot != 1 {
		t.Fatalf("column-6 bot seat = %+v", bot)
	}
	if _, err := first.wire.WriteMessage(RequestAddAISlot, []byte{6, 1, 3, 1}); err != nil {
		t.Fatal(err)
	}
	editedFramesFirst, editedFramesSecond := readBoth([]uint16{ReplyAddAISlot}, []uint16{ReplyAddAISlot})
	editedFirst, editedSecond := editedFramesFirst[0], editedFramesSecond[0]
	if !bytes.Equal(editedFirst.Payload, editedSecond.Payload) || editedFirst.Payload[1] != 2 || editedFirst.Payload[3] != 1 || editedFirst.Payload[5] != 3 || editedFirst.Payload[21] != 1 {
		t.Fatalf("AI edit replies differ or have wrong F1 layout: %x / %x", editedFirst.Payload, editedSecond.Payload)
	}
	if room := server.roomByNumber(0); room.Snapshot().Users != 3 || room.SessionAt(2) != bot || bot.BotDifficulty != 3 || bot.BotWolf {
		t.Fatalf("AI edit replaced or duplicated bot: bot=%+v users=%d", bot, room.Snapshot().Users)
	}
	if _, err := first.wire.WriteMessage(RequestDeleteAISlot, []byte{2}); err != nil {
		t.Fatal(err)
	}
	deletedFirst, deletedSecond := readBoth([]uint16{ReplyDeleteAISlot}, []uint16{ReplyDeleteAISlot})
	if deletedFirst[0].Payload[1] != 2 || !bytes.Equal(deletedFirst[0].Payload, deletedSecond[0].Payload) {
		t.Fatalf("delete-AI replies differ or have wrong layout: %x / %x", deletedFirst[0].Payload, deletedSecond[0].Payload)
	}
	if room := server.roomByNumber(0); room.Snapshot().Users != 2 || room.SessionAt(2) != nil || room.Config.Mode&0x040000 != 0 {
		t.Fatalf("AI was not removed: users=%d mode=%#x slot=%v", room.Snapshot().Users, room.Config.Mode, room.SessionAt(2))
	}
	if _, err := first.wire.WriteMessage(RequestAddAISlot, []byte{6, 1, 3, 1}); err != nil {
		t.Fatal(err)
	}
	readdedFirst, readdedSecond := readBoth([]uint16{ReplyAddAISlot}, []uint16{ReplyAddAISlot})
	if !bytes.Equal(readdedFirst[0].Payload, readdedSecond[0].Payload) || readdedFirst[0].Payload[1] != 2 {
		t.Fatalf("re-added AI replies differ or have wrong layout: %x / %x", readdedFirst[0].Payload, readdedSecond[0].Payload)
	}
	if _, err := second.wire.WriteMessage(RequestUDPReady, []byte{1}); err != nil {
		t.Fatal(err)
	}
	readBoth([]uint16{ReplyUDPReady}, []uint16{ReplyUDPReady})
	if _, err := second.wire.WriteMessage(RequestGetPowerUser, nil); err != nil {
		t.Fatal(err)
	}
	assertFrameID(t, second.wire, ReplyGetPowerUser)

	if _, err := first.wire.WriteMessage(RequestStartGame, nil); err != nil {
		t.Fatal(err)
	}
	readBoth(
		[]uint16{ReplyChangeSlot, ReplyChangeSlot, ReplyStartGame},
		[]uint16{ReplyChangeSlot, ReplyChangeSlot, ReplyStartGame},
	)

	if _, err := first.wire.WriteMessage(RequestStartRound, nil); err != nil {
		t.Fatal(err)
	}
	roundFirst, roundSecond := readBoth(
		[]uint16{ReplyChangeSlot, ReplyStartRound, ReplyChangeSlot},
		[]uint16{ReplyChangeSlot, ReplyChangeSlot},
	)
	botLiveFirst, botLiveSecond := roundFirst[2], roundSecond[1]
	if botLiveFirst.Payload[1] != 2 || botLiveFirst.Payload[2] != 3 || !bytes.Equal(botLiveFirst.Payload, botLiveSecond.Payload) {
		t.Fatalf("bot live promotion = %x / %x", botLiveFirst.Payload, botLiveSecond.Payload)
	}
	deathRequest := make([]byte, 8)
	deathRequest[0], deathRequest[1] = 0, 1
	if _, err := second.wire.WriteMessage(RequestPlayerDead, deathRequest); err != nil {
		t.Fatal(err)
	}
	deathFirst, deathSecond := readBoth(
		[]uint16{ReplyPlayerDead},
		[]uint16{ReplyPlayerDead},
	)
	if !bytes.Equal(deathFirst[0].Payload, deathSecond[0].Payload) ||
		binary.LittleEndian.Uint16(deathFirst[0].Payload[1:3]) != 1 ||
		binary.LittleEndian.Uint16(deathFirst[0].Payload[3:5]) != 0 ||
		deathFirst[0].Payload[5] != 0 || deathFirst[0].Payload[6] != 1 || deathFirst[0].Payload[7] != 0 {
		t.Fatalf("player-dead reply has wrong protocol layout: %x / %x", deathFirst[0].Payload, deathSecond[0].Payload)
	}
	users.mu.Lock()
	registeredKills := users.registeredKills
	users.mu.Unlock()
	if registeredKills != 0 {
		t.Fatalf("AI deathmatch persisted %d competitive kill records", registeredKills)
	}

	if _, err := first.wire.WriteMessage(RequestKick, []byte{1}); err != nil {
		t.Fatal(err)
	}
	kickerFrames, kickedFrames := readBoth(
		[]uint16{ReplyKick, ReplyExitRoom},
		[]uint16{ReplyExitRoom, ReplyFieldUsers, ReplyJoinAllow, ReplyChannelChars},
	)
	if kickerFrames[1].Payload[1] != 1 || kickedFrames[0].Payload[1] != 1 {
		t.Fatalf("kick exit slots = %x / %x", kickerFrames[1].Payload, kickedFrames[0].Payload)
	}
	if room := server.roomByNumber(0); room.Snapshot().Users != 2 || room.SessionAt(1) != nil {
		t.Fatalf("kicked player remains in room: users=%d slot=%v", room.Snapshot().Users, room.SessionAt(1))
	}

	_ = first.connection.Close()
	_ = second.connection.Close()
	for _, p := range []peer{first, second} {
		select {
		case err := <-p.done:
			if err != nil {
				t.Fatalf("connection handler: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("connection handler did not stop")
		}
	}
}

func TestWaitingRoomJoinReceivesExistingBotsAsAISlots(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	loopback := netip.MustParseAddr("127.0.0.1")
	room := newRoom(0, RoomConfig{Title: "AI room", Capacity: 8, Mode: 2, Time: 300})
	creator := &Session{
		LoginName: "creator", Callsign: "Creator", UserID: 7,
		RoomNumber: -1, RoomSlot: -1, RoomTeam: 0xff, RoomTeamSlot: 0xff,
		PeerIP: loopback, UDPIP: loopback, UDPPort: 40709,
	}
	if _, ok := room.Add(creator); !ok {
		t.Fatal("add room creator")
	}
	bot := room.ProvisionBot(2, 3, true, 40708, 1)
	if bot == nil {
		t.Fatal("add existing bot")
	}
	if room.Snapshot().GameStarted {
		t.Fatal("test room unexpectedly started")
	}
	harness.server.mu.Lock()
	harness.server.rooms[room.Number] = room
	harness.server.mu.Unlock()

	request := make([]byte, 2)
	binary.LittleEndian.PutUint16(request, uint16(room.Number))
	done := make(chan error, 1)
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{
			MessageID: RequestEnterRoom,
			Payload:   request,
		})
	}()

	assertFrameID(t, harness.client, ReplyEnterRoom)
	roster := assertFrameID(t, harness.client, ReplyRoomChars)
	aiSlot := assertFrameID(t, harness.client, ReplyAddAISlot)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(roster.Payload, []byte(bot.LoginName)) {
		t.Fatalf("bot was serialized as a normal room peer: %x", roster.Payload)
	}
	if aiSlot.Payload[1] != byte(bot.RoomSlot) || aiSlot.Payload[2] != 6 ||
		aiSlot.Payload[3] != bot.RoomTeamSlot || aiSlot.Payload[4] != 1 ||
		aiSlot.Payload[5] != bot.BotDifficulty || aiSlot.Payload[6] != bot.BotNumber ||
		aiSlot.Payload[21] != 2 {
		t.Fatalf("existing bot AI-slot replay = %x", aiSlot.Payload)
	}
}

func TestLateJoinStartRoundUsesLiveRoundClock(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	room := newRoom(0, RoomConfig{Capacity: 8, Mode: 2, Time: 300})
	if room.ProvisionBot(2, 1, false, 40708, 0) == nil {
		t.Fatal("add existing bot")
	}
	beginTestMatch(room)
	startedAt := time.Now().Add(-90 * time.Second)
	room.BeginRound(startedAt)
	room.RedScore, room.BlueScore = 2, 1
	if _, ok := room.Add(harness.session); !ok {
		t.Fatal("add late joiner")
	}
	harness.server.mu.Lock()
	harness.server.rooms[room.Number] = room
	harness.server.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{
			MessageID: RequestStartRound,
		})
	}()
	assertFrameID(t, harness.client, ReplyChangeSlot)
	start := assertFrameID(t, harness.client, ReplyStartRound)
	assertFrameID(t, harness.client, ReplyChangeSlot)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	timer := binary.LittleEndian.Uint16(start.Payload[2:4])
	if start.Payload[1] != 1 || timer < 214 || timer > 215 || start.Payload[4] != 2 || start.Payload[5] != 1 {
		t.Fatalf("late-join round payload = %x", start.Payload)
	}
	if room.RoundStartedAt != startedAt {
		t.Fatalf("late join reset round start from %v to %v", startedAt, room.RoundStartedAt)
	}
}

func TestRunningMemberEnterRoomDoesNotDuplicateOrResetMatch(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	room := newRoom(0, RoomConfig{Capacity: 8, Mode: 2, Time: 300})
	other := &Session{RoomNumber: -1, RoomSlot: -1, LoginName: "other", UserID: 7}
	if _, ok := room.Add(other); !ok {
		t.Fatal("add existing player")
	}
	originalSlot, ok := room.Add(harness.session)
	if !ok {
		t.Fatal("add returning player")
	}
	beginTestMatch(room)
	startedAt := time.Now().Add(-75 * time.Second)
	room.BeginRound(startedAt)
	room.RedScore, room.BlueScore = 4, 3
	harness.session.mu.Lock()
	harness.session.RoomStatus = 4
	harness.session.mu.Unlock()
	harness.server.mu.Lock()
	harness.server.rooms[room.Number] = room
	harness.server.mu.Unlock()

	payload := make([]byte, 2)
	binary.LittleEndian.PutUint16(payload, uint16(room.Number))
	done := make(chan error, 1)
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{
			MessageID: RequestEnterRoom,
			Payload:   payload,
		})
	}()
	assertFrameID(t, harness.client, ReplyEnterRoom)
	roster := assertFrameID(t, harness.client, ReplyRoomChars)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if roster.Payload[1] != 2 {
		t.Fatalf("running room roster field state = %d, want 2", roster.Payload[1])
	}

	snapshot := room.Snapshot()
	harness.session.mu.RLock()
	gotSlot, gotStatus := harness.session.RoomSlot, harness.session.RoomStatus
	harness.session.mu.RUnlock()
	if snapshot.Users != 2 || len(snapshot.Members) != 2 || gotSlot != int16(originalSlot) || gotStatus != 4 {
		t.Fatalf("room re-entry duplicated player: users=%d members=%d slot=%d status=%d", snapshot.Users, len(snapshot.Members), gotSlot, gotStatus)
	}
	if !snapshot.GameStarted || !snapshot.RoundStarted || snapshot.Round != 1 ||
		snapshot.RedScore != 4 || snapshot.BlueScore != 3 || room.RoundStartedAt != startedAt {
		t.Fatalf("room re-entry reset match state: snapshot=%+v started=%v want=%v", snapshot, room.RoundStartedAt, startedAt)
	}
}

func TestEnterPasswordProtectedRoomRejectsWrongPassword(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	room := newRoom(0, RoomConfig{Title: "locked", Password: "secret", Capacity: 8})
	harness.server.mu.Lock()
	harness.server.rooms[room.Number] = room
	harness.server.mu.Unlock()

	payload := []byte{0, 0, 6, 'b', 0, 'a', 0, 'd', 0, 0}
	done := make(chan error, 1)
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{
			MessageID: RequestEnterRoom,
			Payload:   payload,
		})
	}()
	reply := assertFrameID(t, harness.client, ReplyEnterRoom)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if reply.Payload[0] != 0x7f || room.Snapshot().Users != 0 || harness.session.RoomNumber != -1 {
		t.Fatalf("wrong-password enter reply/state = %x users=%d room=%d", reply.Payload, room.Snapshot().Users, harness.session.RoomNumber)
	}

	payload = []byte{0, 0, 12, 's', 0, 'e', 0, 'c', 0, 'r', 0, 'e', 0, 't', 0, 0}
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{
			MessageID: RequestEnterRoom,
			Payload:   payload,
		})
	}()
	reply = assertFrameID(t, harness.client, ReplyEnterRoom)
	assertFrameID(t, harness.client, ReplyRoomChars)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if reply.Payload[0] != 0 || room.Snapshot().Users != 1 || harness.session.RoomNumber != room.Number {
		t.Fatalf("correct-password enter reply/state = %x users=%d room=%d", reply.Payload, room.Snapshot().Users, harness.session.RoomNumber)
	}
}

func TestEnterClassLimitedRoomRejectsOutOfRangeLevel(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	room := newRoom(0, RoomConfig{
		Title: "limited", Capacity: 8, ClassLimitMin: 10, ClassLimitMax: 20,
	})
	harness.server.mu.Lock()
	harness.server.rooms[room.Number] = room
	harness.server.mu.Unlock()

	payload := []byte{0, 0}
	done := make(chan error, 1)
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{
			MessageID: RequestEnterRoom,
			Payload:   payload,
		})
	}()
	reply := assertFrameID(t, harness.client, ReplyEnterRoom)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if reply.Payload[0] != 0x80 || room.Snapshot().Users != 0 || harness.session.RoomNumber != -1 {
		t.Fatalf("class-limited enter reply/state = %x users=%d room=%d", reply.Payload, room.Snapshot().Users, harness.session.RoomNumber)
	}

	harness.session.mu.Lock()
	harness.session.UserLevel = 12
	harness.session.mu.Unlock()
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{
			MessageID: RequestEnterRoom,
			Payload:   payload,
		})
	}()
	reply = assertFrameID(t, harness.client, ReplyEnterRoom)
	assertFrameID(t, harness.client, ReplyRoomChars)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if reply.Payload[0] != 0 || room.Snapshot().Users != 1 || harness.session.RoomNumber != room.Number {
		t.Fatalf("allowed class enter reply/state = %x users=%d room=%d", reply.Payload, room.Snapshot().Users, harness.session.RoomNumber)
	}
}

func TestInitFieldDoesNotForceActivePlayerExit(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	room := newRoom(0, RoomConfig{Capacity: 8, Mode: 2, Time: 300})
	if _, ok := room.Add(harness.session); !ok {
		t.Fatal("add active player")
	}
	peer := &Session{RoomNumber: -1, RoomSlot: -1, LoginName: "peer"}
	if _, ok := room.Add(peer); !ok {
		t.Fatal("add surviving peer")
	}
	beginTestMatch(room)
	startedAt := time.Now().Add(-45 * time.Second)
	room.BeginRound(startedAt)
	harness.session.mu.Lock()
	harness.session.RoomStatus = 4
	harness.session.mu.Unlock()
	peer.mu.Lock()
	peer.RoomStatus = 4
	peer.mu.Unlock()
	harness.server.mu.Lock()
	harness.server.rooms[room.Number] = room
	harness.server.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{
			MessageID: RequestInitField,
		})
	}()
	assertFrameID(t, harness.client, ReplyInitField)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	harness.session.mu.RLock()
	status, currentSlot, roomNumber := harness.session.RoomStatus, harness.session.RoomSlot, harness.session.RoomNumber
	harness.session.mu.RUnlock()
	snapshot := room.Snapshot()
	if status != 4 || currentSlot < 0 || roomNumber != room.Number || snapshot.Users != 2 || !snapshot.GameStarted ||
		!snapshot.RoundStarted || room.RoundStartedAt != startedAt {
		t.Fatalf("init-field changed active state: status=%d slot=%d room=%d snapshot=%+v", status, currentSlot, roomNumber, snapshot)
	}
}

func assertFrameID(t *testing.T, wire *transport.EncryptedConn, messageID uint16) transport.Frame {
	t.Helper()
	frame, err := wire.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if frame.MessageID != messageID {
		t.Fatalf("message ID = %#x, want %#x (payload %x)", frame.MessageID, messageID, frame.Payload)
	}
	return frame
}
