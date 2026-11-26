package game

import (
	"bytes"
	"context"
	"testing"
	"time"

	"wtserver-go/internal/transport"
)

func TestSpecialModeReplyOpcodes(t *testing.T) {
	want := map[uint16]uint16{
		ReplyWolfKing:           0x167b,
		ReplyChangeCreatureTeam: 0x167d,
		ReplyWolfHuntTeamSort:   0x167f,
		ReplyLadderRoundEnd:     0x1683,
		ReplyNoticeRemainLife:   0x1691,
		ReplyHeroSelect:         0x1693,
		ReplyHeroRemainLife:     0x169b,
		ReplyHeroModeState:      0x169d,
		ReplyHeroOptionState:    0x16a5,
		ReplyChangeRole:         0x16a9,
	}
	for got, expected := range want {
		if got != expected {
			t.Fatalf("reply opcode %#x, want %#x", got, expected)
		}
	}
}

func TestWolfHuntTeamMapping(t *testing.T) {
	if wolfHuntCreatureTeam != 1 || wolfHuntHumanTeam != 2 {
		t.Fatalf("WolfHunt teams = wolf %d human %d, want red/wolf 1 and blue/human 2", wolfHuntCreatureTeam, wolfHuntHumanTeam)
	}
}

func TestWolfHuntPayloadLayouts(t *testing.T) {
	teams, err := WolfHuntTeamSortPayload([]WolfHuntTeamState{
		{Slot: 3, Team: 1, TeamSlot: 0},
		{Slot: 7, Team: 2, TeamSlot: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(teams, []byte{0, 2, 3, 1, 0, 7, 2, 1}) {
		t.Fatalf("team-sort payload = %x", teams)
	}
	roles, err := WolfKingPayload([]byte{3}, []byte{5, 6})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(roles, []byte{0, 1, 3, 2, 5, 6}) {
		t.Fatalf("wolf-king payload = %x", roles)
	}
	if got := ChangeCreatureTeamPayload(7, 2); !bytes.Equal(got, []byte{0, 7, 2}) {
		t.Fatalf("change-creature-team payload = %x", got)
	}
	if got := ChangeRolePayload(1); !bytes.Equal(got, []byte{0, 1}) {
		t.Fatalf("change-role payload = %x", got)
	}
	// 2012 SWITCHTEAM_ACK (sub_341CCD60): result byte, then the switched flag.
	if switched := SwitchTeamPayload(true); !bytes.Equal(switched, []byte{0, 1}) {
		t.Fatalf("switch-team payload = %x", switched)
	}
}

func TestWolfHuntTeamSortFreesDestinationRowsBeforeFillingThem(t *testing.T) {
	players := []WolfHuntTeamState{
		{Slot: 0, Team: wolfHuntHumanTeam, TeamSlot: 0}, // incoming from wolf row zero
		{Slot: 1, Team: wolfHuntHumanTeam, TeamSlot: 1}, // move the existing human first
		{Slot: 2, Team: wolfHuntCreatureTeam, TeamSlot: 0},
	}
	orderWolfHuntTeamStates(players)
	wantSlots := []byte{1, 0, 2}
	for index, want := range wantSlots {
		if players[index].Slot != want {
			t.Fatalf("team-sort order = %+v, want slots %v", players, wantSlots)
		}
	}
}

func TestHeroAndLadderPayloadLayouts(t *testing.T) {
	ladder, err := LadderRoundEndPayload([]LadderRoundPlayer{{Slot: 2, RoundScore: 0x1234, TotalScore: 0x89abcdef}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ladder, []byte{0, 1, 2, 0x34, 0x12, 0xef, 0xcd, 0xab, 0x89}) {
		t.Fatalf("ladder payload = %x", ladder)
	}
	notice, err := NoticeRemainLifePayload([]RemainLifeNotice{{Slot: 2, Lives: 3, Kind: wolfHuntLifeKind}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(notice, []byte{0, 1, 2, 0, 3, wolfHuntLifeKind}) {
		t.Fatalf("remaining-life notice = %x", notice)
	}
	selected, err := HeroSelectPayload(2, []byte{2, 5})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(selected, []byte{0, 2, 2, 2, 5}) {
		t.Fatalf("hero-select payload = %x", selected)
	}
	remain := HeroRemainLifePayload(2, 1, true, 4, 3, 0x1234)
	if !bytes.Equal(remain, []byte{0, 2, 1, 1, 4, 3, 0x34, 0x12}) {
		t.Fatalf("hero-remain-life payload = %x", remain)
	}
	mode, err := HeroModeStatePayload([]HeroModeRecord{{
		TeamIndex:       1,
		ShowHeroGauge:   true,
		NormalUserTotal: 3,
		NormalUserCount: 4,
		HeroUserTotal:   5,
		HeroUserCount:   6,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mode, []byte{0, 1, 1, 1, 3, 4, 5, 6}) {
		t.Fatalf("hero-mode payload = %x", mode)
	}
	options, err := HeroOptionStatePayload(1, 0, []HeroOptionPlayer{{Slot: 2, State: 1}, {Slot: 5, State: 0}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(options, []byte{0, 1, 0, 2, 2, 1, 5, 0}) {
		t.Fatalf("hero-option payload = %x", options)
	}
}

func TestRoomPreparesSpecialModeOnceAndKeepsHeroRespawnEnabled(t *testing.T) {
	assertWolfHuntTeamSort(t, 2, 1)
	assertWolfHuntTeamSort(t, 8, 2)

	heroRoom := newRoom(2, RoomConfig{Capacity: 8, Mode: 2, WeaponFlags: heroModeFlag})
	hero := &Session{RoomNumber: -1, RoomSlot: -1}
	if _, ok := heroRoom.Add(hero); !ok {
		t.Fatal("add hero")
	}
	beginTestMatch(heroRoom)
	heroRoom.BeginRound(testTime)
	state, ok := heroRoom.PrepareSpecialRound()
	if !ok || !state.Hero || len(state.Heroes) != 0 ||
		len(state.Life) != 1 || state.Life[0].Lives != 1 || state.Life[0].Kind != 0 {
		t.Fatalf("hero special state = %+v, initialized=%v", state, ok)
	}
	result := heroRoom.RecordKill(hero, hero)
	if !result.Accepted || heroRoom.RemainingLives[0] != 1 {
		t.Fatalf("hero death changed respawn state: result=%+v life=%d", result, heroRoom.RemainingLives[0])
	}
	update, selected, ok := heroRoom.RecordHeroDeath(hero)
	if !ok || selected || update.Slot != 0 || update.Team != 1 || !update.Active || update.Lives != 1 || update.Kind != 0 {
		t.Fatalf("Hero Deathmatch revival state = %+v, selected=%v available=%v", update, selected, ok)
	}
	if _, _, revived := heroRoom.ReviveSlot(0); !revived {
		t.Fatal("Hero Deathmatch player could not revive")
	}
}

func TestHeroDeathmatchStartsWithEveryPlayerNormal(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	room := newRoom(1, RoomConfig{Capacity: 3, Mode: 2, Points: 20, Time: 300, WeaponFlags: heroModeFlag})
	if _, ok := room.Add(harness.session); !ok {
		t.Fatal("add connected player")
	}
	for index := 0; index < 2; index++ {
		if _, ok := room.Add(&Session{RoomNumber: -1, RoomSlot: -1}); !ok {
			t.Fatalf("add peer %d", index)
		}
	}
	beginTestMatch(room)
	room.BeginRound(testTime)
	state, ok := room.PrepareSpecialRound()
	if !ok || len(state.Heroes) != 0 {
		t.Fatalf("initial Hero Deathmatch state = %+v, initialized=%v", state, ok)
	}
	for _, player := range state.Options {
		if player.State != 0 {
			t.Fatalf("slot %d started as Hero state %d", player.Slot, player.State)
		}
	}

	done := make(chan error, 1)
	go func() {
		done <- harness.server.sendSpecialRound(harness.session, room, state)
	}()
	assertFrameID(t, harness.client, ReplyHeroModeState)
	options := assertFrameID(t, harness.client, ReplyHeroOptionState)
	life := assertFrameID(t, harness.client, ReplyNoticeRemainLife)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(options.Payload) < 10 ||
		!bytes.Equal(options.Payload[:10], []byte{0, 0, 0, 3, 0, 0, 1, 0, 2, 0}) {
		t.Fatalf("Hero option states = %x", options.Payload)
	}
	if len(life.Payload) < 14 ||
		!bytes.Equal(life.Payload[:14], []byte{0, 3, 0, 0, 1, 0, 1, 0, 1, 0, 2, 0, 1, 0}) {
		t.Fatalf("Hero life states = %x", life.Payload)
	}
}

func TestStartRoundInitializesHeroBeforeRoundConstruction(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	room := newRoom(1, RoomConfig{
		Capacity: 1, Mode: 2, Points: 20, Time: 300, WeaponFlags: heroModeFlag,
	})
	if _, ok := room.Add(harness.session); !ok {
		t.Fatal("add connected player")
	}
	beginTestMatch(room)
	harness.server.mu.Lock()
	harness.server.rooms[room.Number] = room
	harness.server.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{MessageID: RequestStartRound})
	}()
	assertFrameID(t, harness.client, ReplyChangeSlot)
	assertFrameID(t, harness.client, ReplyHeroModeState)
	options := assertFrameID(t, harness.client, ReplyHeroOptionState)
	assertFrameID(t, harness.client, ReplyNoticeRemainLife)
	assertFrameID(t, harness.client, ReplyStartRound)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(options.Payload) < 6 || !bytes.Equal(options.Payload[:6], []byte{0, 0, 0, 1, 0, 0}) {
		t.Fatalf("post-STARTROUND Hero options = %x", options.Payload)
	}
}

func TestRestartRoundInitializesHeroBeforeRoundConstruction(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	room := newRoom(1, RoomConfig{
		Capacity: 1, Mode: 2, Wins: 3, Points: 20, Time: 90, WeaponFlags: heroModeFlag,
	})
	if _, ok := room.Add(harness.session); !ok {
		t.Fatal("add connected player")
	}
	beginTestMatch(room)
	room.BeginRound(testTime)
	room.PrepareSpecialRound()
	round, _, _, ok := room.FinishRound(false)
	if !ok {
		t.Fatal("end Hero Deathmatch round")
	}
	room.CompleteRoundDelay(false, testTime.Add(time.Second))

	done := make(chan struct{})
	go func() {
		harness.server.restartRound(room, round, room.Config.Time)
		close(done)
	}()
	assertFrameID(t, harness.client, ReplyHeroModeState)
	assertFrameID(t, harness.client, ReplyHeroOptionState)
	assertFrameID(t, harness.client, ReplyNoticeRemainLife)
	assertFrameID(t, harness.client, ReplyRestartRound)
	<-done
}

func TestHeroDeathmatchSelectsDeadPlayersAsPointGapGrows(t *testing.T) {
	room := newRoom(1, RoomConfig{Capacity: 4, Mode: 2, Points: 40, Time: 300, WeaponFlags: heroModeFlag})
	for index := 0; index < 4; index++ {
		if _, ok := room.Add(&Session{RoomNumber: -1, RoomSlot: -1}); !ok {
			t.Fatalf("add player %d", index)
		}
	}
	beginTestMatch(room)
	room.BeginRound(testTime)
	room.PrepareSpecialRound()

	room.RedScore, room.BlueScore = 0, 2
	if life, selected, ok := room.RecordHeroDeath(room.SessionAt(0)); !ok || selected || life.Kind != 0 {
		t.Fatalf("two-point gap selected a Hero: life=%+v selected=%v available=%v", life, selected, ok)
	}

	room.BlueScore = 3
	if life, selected, ok := room.RecordHeroDeath(room.SessionAt(0)); !ok || !selected || life.Kind != heroLifeKind {
		t.Fatalf("three-point gap did not select first Hero: life=%+v selected=%v available=%v", life, selected, ok)
	}
	if life, selected, ok := room.RecordHeroDeath(room.SessionAt(2)); !ok || selected || life.Kind != 0 {
		t.Fatalf("three-point gap selected a second Hero: life=%+v selected=%v available=%v", life, selected, ok)
	}
	if life, selected, ok := room.RecordHeroDeath(room.SessionAt(0)); !ok || selected || life.Kind != heroLifeKind {
		t.Fatalf("eligible Hero did not persist through death: life=%+v selected=%v available=%v", life, selected, ok)
	}

	room.BlueScore = 6
	if life, selected, ok := room.RecordHeroDeath(room.SessionAt(2)); !ok || !selected || life.Kind != heroLifeKind {
		t.Fatalf("six-point gap did not select second Hero: life=%+v selected=%v available=%v", life, selected, ok)
	}

	room.BlueScore = 3
	if life, selected, ok := room.RecordHeroDeath(room.SessionAt(2)); !ok || selected || life.Kind != 0 {
		t.Fatalf("excess Hero was not demoted after gap closed: life=%+v selected=%v available=%v", life, selected, ok)
	}
}

func TestHeroDeathmatchDeathArmsClientRespawn(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	room := newRoom(1, RoomConfig{Capacity: 1, Mode: 2, Points: 20, Time: 300, WeaponFlags: heroModeFlag})
	if _, ok := room.Add(harness.session); !ok {
		t.Fatal("add connected player")
	}
	beginTestMatch(room)
	room.BeginRound(testTime)
	if state, ok := room.PrepareSpecialRound(); !ok || !state.Hero {
		t.Fatalf("prepare Hero Deathmatch state = %+v, initialized=%v", state, ok)
	}
	harness.server.mu.Lock()
	harness.server.rooms[room.Number] = room
	harness.server.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{
			MessageID: RequestPlayerDead,
			Payload:   []byte{0, 0, 0, 0, 0, 0, 0, 0},
		})
	}()
	assertFrameID(t, harness.client, ReplyPlayerDead)
	options := assertFrameID(t, harness.client, ReplyHeroOptionState)
	life := assertFrameID(t, harness.client, ReplyHeroRemainLife)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(options.Payload) < 4 || !bytes.Equal(options.Payload[:4], []byte{0, 0, 0, 0}) {
		t.Fatalf("Hero Deathmatch option refresh = %x", options.Payload)
	}
	if len(life.Payload) < 8 || !bytes.Equal(life.Payload[:8], []byte{0, 0, 1, 1, 1, 0, 0, 0}) {
		t.Fatalf("Hero Deathmatch remaining-life payload = %x", life.Payload)
	}
}

func TestHeroDeathmatchAssistedRevivalEchoesReviverSlot(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	room := newRoom(1, RoomConfig{Capacity: 2, Mode: 2, Points: 20, Time: 300, WeaponFlags: heroModeFlag})
	if _, ok := room.Add(harness.session); !ok {
		t.Fatal("add dead player")
	}
	reviver := &Session{RoomNumber: -1, RoomSlot: -1}
	reviverSlot, ok := room.Add(reviver)
	if !ok {
		t.Fatal("add reviver")
	}
	beginTestMatch(room)
	room.BeginRound(testTime)
	room.PrepareSpecialRound()
	if _, _, ok := room.RecordHeroDeath(harness.session); !ok {
		t.Fatal("mark Hero Deathmatch player dead")
	}
	harness.server.mu.Lock()
	harness.server.rooms[room.Number] = room
	harness.server.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{
			MessageID: RequestRevival,
			Payload:   []byte{reviverSlot, 0},
		})
	}()
	revival := assertFrameID(t, harness.client, ReplyRevival)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(revival.Payload) < 5 ||
		!bytes.Equal(revival.Payload[:5], []byte{0, 0, redTeam, 0, reviverSlot}) {
		t.Fatalf("Hero Deathmatch revival payload = %x", revival.Payload)
	}

	// The death token is one-shot. A duplicate must not send another revival.
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{
			MessageID: RequestRevival,
			Payload:   []byte{reviverSlot, 0},
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("duplicate revival attempted to send another ACK")
	}
}

func TestHeroDeathmatchPromotesTrailingPlayerOnDeath(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	room := newRoom(1, RoomConfig{Capacity: 2, Mode: 2, Points: 20, Time: 300, WeaponFlags: heroModeFlag})
	if _, ok := room.Add(harness.session); !ok {
		t.Fatal("add trailing player")
	}
	if _, ok := room.Add(&Session{RoomNumber: -1, RoomSlot: -1}); !ok {
		t.Fatal("add leading player")
	}
	beginTestMatch(room)
	room.BeginRound(testTime)
	room.PrepareSpecialRound()
	room.RedScore, room.BlueScore = 0, 3
	harness.server.mu.Lock()
	harness.server.rooms[room.Number] = room
	harness.server.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{
			MessageID: RequestPlayerDead,
			Payload:   []byte{1, 0, 0, 0, 0, 0, 0, 0},
		})
	}()
	assertFrameID(t, harness.client, ReplyPlayerDead)
	selection := assertFrameID(t, harness.client, ReplyHeroSelect)
	options := assertFrameID(t, harness.client, ReplyHeroOptionState)
	life := assertFrameID(t, harness.client, ReplyHeroRemainLife)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(selection.Payload) < 4 ||
		!bytes.Equal(selection.Payload[:4], []byte{0, redTeam, 1, 0}) {
		t.Fatalf("Hero selection payload = %x", selection.Payload)
	}
	if len(options.Payload) < 4 ||
		!bytes.Equal(options.Payload[:4], []byte{0, 1, 0, 0}) {
		t.Fatalf("promoted Hero option refresh = %x", options.Payload)
	}
	if len(life.Payload) < 8 ||
		!bytes.Equal(life.Payload[:8], []byte{0, 0, redTeam, 1, 1, heroLifeKind, 0, 0}) {
		t.Fatalf("promoted Hero life payload = %x", life.Payload)
	}
}

func assertWolfHuntTeamSort(t *testing.T, playerCount, kingCount int) {
	t.Helper()
	room := newRoom(1, RoomConfig{Capacity: byte(playerCount), Mode: 0x180})
	for index := 0; index < playerCount; index++ {
		if _, ok := room.Add(&Session{RoomNumber: -1, RoomSlot: -1}); !ok {
			t.Fatalf("add WolfHunt player %d", index)
		}
	}
	beginTestMatch(room)
	room.BeginRound(testTime)
	state, initialized := room.PrepareSpecialRound()
	if !initialized || !state.WolfHunt || state.Hero {
		t.Fatalf("wolf special state = %+v, initialized=%v", state, initialized)
	}
	if len(state.Players) != playerCount || len(state.Kings) != kingCount || len(state.Wolves) != 0 {
		t.Fatalf("creature team counts: players=%d kings=%d wolves=%d", len(state.Players), len(state.Kings), len(state.Wolves))
	}
	if len(state.Life) != playerCount {
		t.Fatalf("WolfHunt life records=%d, want %d", len(state.Life), playerCount)
	}
	kingLife := make(map[byte]byte, len(state.Life))
	for _, life := range state.Life {
		if life.Kind != wolfHuntLifeKind {
			t.Fatalf("slot %d life kind=%d, want WolfHunt kind %d", life.Slot, life.Kind, wolfHuntLifeKind)
		}
		kingLife[life.Slot] = life.Lives
	}

	kings := make(map[byte]struct{}, len(state.Kings))
	for _, slot := range state.Kings {
		if _, duplicate := kings[slot]; duplicate {
			t.Fatalf("duplicate Wolf King slot %d", slot)
		}
		kings[slot] = struct{}{}
	}
	teamRows := map[byte]map[byte]struct{}{1: {}, 2: {}}
	teamCounts := map[byte]int{}
	for _, player := range state.Players {
		member := room.SessionAt(player.Slot)
		if member == nil {
			t.Fatalf("team-sort slot %d has no room member", player.Slot)
		}
		member.mu.RLock()
		team, teamSlot := member.RoomTeam, member.RoomTeamSlot
		member.mu.RUnlock()
		if team != player.Team || teamSlot != player.TeamSlot {
			t.Fatalf("slot %d session team=(%d,%d), sort team=(%d,%d)", player.Slot, team, teamSlot, player.Team, player.TeamSlot)
		}
		if _, duplicate := teamRows[player.Team][player.TeamSlot]; duplicate {
			t.Fatalf("team %d has duplicate row %d", player.Team, player.TeamSlot)
		}
		teamRows[player.Team][player.TeamSlot] = struct{}{}
		teamCounts[player.Team]++
		_, king := kings[player.Slot]
		if king && (player.Team != wolfHuntCreatureTeam || room.SpecialRoles[player.Slot] != 1) {
			t.Fatalf("king slot %d has team %d role %d", player.Slot, player.Team, room.SpecialRoles[player.Slot])
		}
		if !king && (player.Team != wolfHuntHumanTeam || room.SpecialRoles[player.Slot] != 2) {
			t.Fatalf("human slot %d has team %d role %d", player.Slot, player.Team, room.SpecialRoles[player.Slot])
		}
		wantLives := byte(0)
		if king {
			wantLives = wolfHuntKingLives
		}
		if kingLife[player.Slot] != wantLives {
			t.Fatalf("slot %d lives=%d, want %d", player.Slot, kingLife[player.Slot], wantLives)
		}
	}
	if teamCounts[wolfHuntCreatureTeam] != kingCount || teamCounts[wolfHuntHumanTeam] != playerCount-kingCount {
		t.Fatalf("sorted teams: wolves=%d humans=%d", teamCounts[wolfHuntCreatureTeam], teamCounts[wolfHuntHumanTeam])
	}
	for team, count := range teamCounts {
		for row := 0; row < count; row++ {
			if _, ok := teamRows[team][byte(row)]; !ok {
				t.Fatalf("team %d is missing row %d", team, row)
			}
		}
	}
	if _, duplicate := room.PrepareSpecialRound(); duplicate {
		t.Fatal("special state initialized twice in one round")
	}
}

func TestWolfHuntKingCount(t *testing.T) {
	tests := map[int]int{0: 0, 1: 1, 2: 1, 7: 1, 8: 2, 16: 2}
	for players, want := range tests {
		if got := wolfHuntKingCount(players); got != want {
			t.Fatalf("wolfHuntKingCount(%d) = %d, want %d", players, got, want)
		}
	}
}

func TestWolfHuntFlagSelectsSoloPlayerAsKing(t *testing.T) {
	room := newRoom(1, RoomConfig{Capacity: 1, Mode: 0x180})
	player := &Session{RoomNumber: -1, RoomSlot: -1}
	if _, ok := room.Add(player); !ok {
		t.Fatal("add solo WolfHunt player")
	}
	beginTestMatch(room)
	room.BeginRound(testTime)
	state, initialized := room.PrepareSpecialRound()
	if !initialized || !state.WolfHunt || !bytes.Equal(state.Kings, []byte{0}) || len(state.Wolves) != 0 {
		t.Fatalf("solo WolfHunt state = %+v, initialized=%v", state, initialized)
	}
	payload, err := WolfKingPayload(state.Kings, state.Wolves)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, []byte{0, 1, 0, 0}) {
		t.Fatalf("solo Wolf King payload = %x", payload)
	}
}

func TestStartRoundSendsSoloWolfKing(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	room := newRoom(1, RoomConfig{Capacity: 1, Mode: 0x180, Time: 300})
	if _, ok := room.Add(harness.session); !ok {
		t.Fatal("add solo WolfHunt player")
	}
	beginTestMatch(room)
	harness.server.mu.Lock()
	harness.server.rooms[room.Number] = room
	harness.server.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{MessageID: RequestStartRound})
	}()
	assertFrameID(t, harness.client, ReplyChangeSlot)
	roles := assertFrameID(t, harness.client, ReplyWolfKing)
	if len(roles.Payload) < 4 || !bytes.Equal(roles.Payload[:4], []byte{0, 1, 0, 0}) {
		t.Fatalf("solo pre-STARTROUND Wolf King payload = %x", roles.Payload)
	}
	teams := assertFrameID(t, harness.client, ReplyWolfHuntTeamSort)
	if len(teams.Payload) < 5 || !bytes.Equal(teams.Payload[:5], []byte{0, 1, 0, wolfHuntCreatureTeam, 0}) {
		t.Fatalf("solo pre-STARTROUND team-sort payload = %x", teams.Payload)
	}
	life := assertFrameID(t, harness.client, ReplyNoticeRemainLife)
	if len(life.Payload) < 6 || !bytes.Equal(life.Payload[:6], []byte{0, 1, 0, 0, wolfHuntKingLives, wolfHuntLifeKind}) {
		t.Fatalf("solo pre-STARTROUND life payload = %x", life.Payload)
	}
	assertFrameID(t, harness.client, ReplyStartRound)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStartRoundResendsWolfHuntStateForEveryRequester(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	room := newRoom(1, RoomConfig{Capacity: 1, Mode: 0x180, Time: 300})
	if _, ok := room.Add(harness.session); !ok {
		t.Fatal("add solo WolfHunt player")
	}
	beginTestMatch(room)
	harness.server.mu.Lock()
	harness.server.rooms[room.Number] = room
	harness.server.mu.Unlock()

	var firstTeams, firstRoles, firstLife []byte
	for attempt := 0; attempt < 2; attempt++ {
		done := make(chan error, 1)
		go func() {
			done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{MessageID: RequestStartRound})
		}()
		assertFrameID(t, harness.client, ReplyChangeSlot)
		roles := assertFrameID(t, harness.client, ReplyWolfKing)
		teams := assertFrameID(t, harness.client, ReplyWolfHuntTeamSort)
		life := assertFrameID(t, harness.client, ReplyNoticeRemainLife)
		assertFrameID(t, harness.client, ReplyStartRound)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if attempt == 0 {
			firstTeams = append([]byte(nil), teams.Payload...)
			firstRoles = append([]byte(nil), roles.Payload...)
			firstLife = append([]byte(nil), life.Payload...)
			continue
		}
		if !bytes.Equal(teams.Payload, firstTeams) || !bytes.Equal(roles.Payload, firstRoles) || !bytes.Equal(life.Payload, firstLife) {
			t.Fatalf("repeated WolfHunt state changed: teams=%x/%x roles=%x/%x life=%x/%x", firstTeams, teams.Payload, firstRoles, roles.Payload, firstLife, life.Payload)
		}
	}
}

func TestWolfHuntTransitionSendsRoleBeforeRoundConstruction(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	room := newRoom(1, RoomConfig{Capacity: 8, Mode: 0x180})
	if _, ok := room.Add(harness.session); !ok {
		t.Fatal("add Wolf King")
	}

	done := make(chan struct{})
	go func() {
		harness.server.broadcastSpecialRound(room, SpecialRoundState{
			WolfHunt: true,
			Players:  []WolfHuntTeamState{{Slot: 0, Team: wolfHuntCreatureTeam, TeamSlot: 0}},
			Kings:    []byte{0},
			Life:     []RemainLifeNotice{{Slot: 0, Lives: wolfHuntKingLives, Kind: wolfHuntLifeKind}},
		})
		close(done)
	}()
	wolfKing := assertFrameID(t, harness.client, ReplyWolfKing)
	if len(wolfKing.Payload) < 4 || !bytes.Equal(wolfKing.Payload[:4], []byte{0, 1, 0, 0}) {
		t.Fatalf("Wolf King payload = %x", wolfKing.Payload)
	}
	teamSort := assertFrameID(t, harness.client, ReplyWolfHuntTeamSort)
	if len(teamSort.Payload) < 5 || !bytes.Equal(teamSort.Payload[:5], []byte{0, 1, 0, wolfHuntCreatureTeam, 0}) {
		t.Fatalf("WolfHunt team-sort payload = %x", teamSort.Payload)
	}
	life := assertFrameID(t, harness.client, ReplyNoticeRemainLife)
	if len(life.Payload) < 6 || !bytes.Equal(life.Payload[:6], []byte{0, 1, 0, 0, wolfHuntKingLives, wolfHuntLifeKind}) {
		t.Fatalf("WolfHunt life payload = %x", life.Payload)
	}
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		unexpected, err := harness.client.ReadFrame()
		if err != nil {
			t.Fatalf("WolfHunt initialization did not finish: %v", err)
		}
		t.Fatalf("unexpected pre-round WolfHunt message %#x payload=%x", unexpected.MessageID, unexpected.Payload)
	}
}

func TestWolfHuntRestartTransitionsRoleBeforeRoundPacket(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	room := newRoom(1, RoomConfig{Capacity: 1, Mode: 0x180, Wins: 3, Time: 90})
	if _, ok := room.Add(harness.session); !ok {
		t.Fatal("add WolfHunt player")
	}
	beginTestMatch(room)
	room.BeginRound(testTime)
	room.PrepareSpecialRound()
	round, _, _, ok := room.FinishRound(false)
	if !ok {
		t.Fatal("end WolfHunt round")
	}
	room.CompleteRoundDelay(false, testTime.Add(time.Second))

	done := make(chan struct{})
	go func() {
		harness.server.restartRound(room, round, room.Config.Time)
		close(done)
	}()
	roles := assertFrameID(t, harness.client, ReplyWolfKing)
	teams := assertFrameID(t, harness.client, ReplyWolfHuntTeamSort)
	life := assertFrameID(t, harness.client, ReplyNoticeRemainLife)
	assertFrameID(t, harness.client, ReplyRestartRound)
	<-done

	if len(roles.Payload) < 4 || !bytes.Equal(roles.Payload[:4], []byte{0, 1, 0, 0}) {
		t.Fatalf("restart Wolf King payload = %x", roles.Payload)
	}
	if len(teams.Payload) < 5 || !bytes.Equal(teams.Payload[:5], []byte{0, 1, 0, wolfHuntCreatureTeam, 0}) {
		t.Fatalf("restart WolfHunt team-sort payload = %x", teams.Payload)
	}
	if len(life.Payload) < 6 || !bytes.Equal(life.Payload[:6], []byte{0, 1, 0, 0, wolfHuntKingLives, wolfHuntLifeKind}) {
		t.Fatalf("restart WolfHunt life payload = %x", life.Payload)
	}
}

func TestWolfHuntRoundWaitsForEveryHumanClient(t *testing.T) {
	room := newRoom(1, RoomConfig{Capacity: 2, Mode: 0x180, Wins: 3, Time: 90})
	first := &Session{RoomNumber: -1, RoomSlot: -1}
	second := &Session{RoomNumber: -1, RoomSlot: -1}
	if _, ok := room.Add(first); !ok {
		t.Fatal("add first player")
	}
	if _, ok := room.Add(second); !ok {
		t.Fatal("add second player")
	}
	beginTestMatch(room)
	if _, _, _, _, started, ready := room.StartRoundWhenReady(first, testTime); ready || started || room.Snapshot().RoundStarted {
		t.Fatalf("Wolf Hunt started before second client: ready=%v started=%v", ready, started)
	}
	_, round, seconds, _, started, ready := room.StartRoundWhenReady(second, testTime)
	if !ready || !started || round != 1 || seconds != 90 || !room.Snapshot().RoundStarted {
		t.Fatalf("Wolf Hunt barrier result = round %d seconds %d ready=%v started=%v", round, seconds, ready, started)
	}
}

func TestWolfHuntInfectionMovesHumanToWolfSide(t *testing.T) {
	room := newRoom(1, RoomConfig{Capacity: 3, Mode: 0x180, Wins: 3, Time: 90})
	for index := 0; index < 3; index++ {
		if _, ok := room.Add(&Session{RoomNumber: -1, RoomSlot: -1}); !ok {
			t.Fatalf("add player %d", index)
		}
	}
	beginTestMatch(room)
	room.BeginRound(testTime)
	state, initialized := room.PrepareSpecialRound()
	if !initialized || len(state.Kings) != 1 || len(state.Players) != 3 {
		t.Fatalf("initial Wolf Hunt state = %+v initialized=%v", state, initialized)
	}
	killer := room.SessionAt(state.Kings[0])
	humans := wolfHuntSlotsForTeam(state.Players, wolfHuntHumanTeam)
	firstVictim := room.SessionAt(humans[0])
	result := room.RecordKill(killer, firstVictim)
	if !result.WolfHuntConverted || result.FinishRound || result.RedScore != 0 || len(result.SpecialRound.Wolves) != 1 ||
		!result.WolfHuntLifeChanged || result.WolfHuntLife.Lives != wolfHuntWolfLives {
		t.Fatalf("first infection = %+v", result)
	}
	firstVictim.mu.RLock()
	convertedTeam, convertedRow := firstVictim.RoomTeam, firstVictim.RoomTeamSlot
	firstVictim.mu.RUnlock()
	killer.mu.RLock()
	killerRow := killer.RoomTeamSlot
	killer.mu.RUnlock()
	if convertedTeam != wolfHuntCreatureTeam || convertedRow == killerRow || convertedRow > 1 {
		t.Fatalf("converted player team/row = %d/%d", convertedTeam, convertedRow)
	}
	// The converted wolf respawns on the client's own timer (no 0x1662), so
	// its next death report is a real death: it spends the single life and
	// the wolf stays down for the rest of the round.
	second := room.RecordKill(killer, firstVictim)
	if !second.Accepted || second.WolfHuntConverted || second.WolfHuntLife.Lives != 0 || second.FinishRound {
		t.Fatalf("converted wolf's second death = %+v", second)
	}
	if !room.Snapshot().Dead[humans[0]] {
		t.Fatal("a wolf without lives came back")
	}
	lastVictim := room.SessionAt(humans[1])
	result = room.RecordKill(firstVictim, lastVictim)
	if !result.WolfHuntConverted || !result.FinishRound || result.EndGame || result.WinTeam != wolfHuntCreatureTeam || result.RedScore != 1 || result.BlueScore != 0 {
		t.Fatalf("last-human infection = %+v", result)
	}
}

func TestWolfHuntTimeoutAwardsHumansOneRound(t *testing.T) {
	start := time.Unix(8_000, 0)
	room := newRoom(1, RoomConfig{Capacity: 2, Mode: 0x180, Wins: 3, Time: 60})
	for index := 0; index < 2; index++ {
		if _, ok := room.Add(&Session{RoomNumber: -1, RoomSlot: -1}); !ok {
			t.Fatalf("add player %d", index)
		}
	}
	beginTestMatch(room)
	room.BeginRound(start)
	room.PrepareSpecialRound()
	winTeam, endGame, triggered := room.RoundTimeout(start.Add(61 * time.Second))
	if !triggered || endGame || winTeam != wolfHuntHumanTeam || room.RedScore != 0 || room.BlueScore != 1 {
		t.Fatalf("Wolf Hunt timeout = team %d end=%v triggered=%v score=%d/%d", winTeam, endGame, triggered, room.RedScore, room.BlueScore)
	}
}

func TestWolfHuntWolfHasThreeLivesBeforeHumansWin(t *testing.T) {
	room := newRoom(1, RoomConfig{Capacity: 2, Mode: 0x180, Wins: 3, Time: 60})
	for index := 0; index < 2; index++ {
		if _, ok := room.Add(&Session{RoomNumber: -1, RoomSlot: -1}); !ok {
			t.Fatalf("add player %d", index)
		}
	}
	beginTestMatch(room)
	room.BeginRound(testTime)
	state, _ := room.PrepareSpecialRound()
	wolf := room.SessionAt(state.Kings[0])
	human := room.SessionAt(wolfHuntSlotsForTeam(state.Players, wolfHuntHumanTeam)[0])
	for remaining := byte(2); ; remaining-- {
		result := room.RecordKill(human, wolf)
		wantEnd := remaining == 0
		if !result.Accepted || result.WolfHuntConverted || result.FinishRound != wantEnd ||
			!result.WolfHuntLifeChanged || result.WolfHuntLife.Lives != remaining {
			t.Fatalf("wolf death with %d lives remaining = %+v", remaining, result)
		}
		if wantEnd {
			if result.EndGame || result.WinTeam != wolfHuntHumanTeam || result.RedScore != 0 || result.BlueScore != 1 {
				t.Fatalf("final wolf death = %+v", result)
			}
			break
		}
		if room.Dead[state.Kings[0]] {
			t.Fatalf("Wolf King remained server-dead with %d lives remaining", remaining)
		}
	}
}

func TestWolfHuntDominateKingUsesAllThreeLivesBeforeConversion(t *testing.T) {
	room := newRoom(1, RoomConfig{Capacity: 2, Mode: 0x1980, Wins: 3, Time: 60})
	for index := 0; index < 2; index++ {
		if _, ok := room.Add(&Session{RoomNumber: -1, RoomSlot: -1}); !ok {
			t.Fatalf("add player %d", index)
		}
	}
	beginTestMatch(room)
	room.BeginRound(testTime)
	state, _ := room.PrepareSpecialRound()
	kingSlot := state.Kings[0]
	wolf := room.SessionAt(kingSlot)
	human := room.SessionAt(wolfHuntSlotsForTeam(state.Players, wolfHuntHumanTeam)[0])
	for remaining := byte(2); ; remaining-- {
		result := room.RecordKill(human, wolf)
		wolf.mu.RLock()
		team := wolf.RoomTeam
		wolf.mu.RUnlock()
		finalLife := remaining == 0
		if !result.Accepted || result.WolfHuntConverted != finalLife || result.FinishRound != finalLife ||
			!result.WolfHuntLifeChanged || result.WolfHuntLife.Lives != remaining {
			t.Fatalf("Dominate King death with %d lives remaining = %+v team=%d", remaining, result, team)
		}
		if finalLife {
			if result.WolfHuntTeam != wolfHuntHumanTeam || team != wolfHuntHumanTeam ||
				result.WinTeam != wolfHuntHumanTeam || result.BlueScore != 1 ||
				len(result.SpecialRound.Kings) != 0 || len(result.SpecialRound.Wolves) != 0 {
				t.Fatalf("Dominate final King conversion = %+v team=%d", result, team)
			}
			break
		}
		if team != wolfHuntCreatureTeam || !room.WolfKings[kingSlot] {
			t.Fatalf("Dominate King changed role early with %d lives remaining: team=%d king=%v", remaining, team, room.WolfKings[kingSlot])
		}
		if room.Dead[kingSlot] {
			t.Fatalf("Dominate King remained server-dead with %d lives remaining", remaining)
		}
	}
}

func TestWolfHuntDominateConvertedWolfHasOneLife(t *testing.T) {
	room := newRoom(1, RoomConfig{Capacity: 3, Mode: 0x1980, Wins: 3, Time: 60})
	for index := 0; index < 3; index++ {
		if _, ok := room.Add(&Session{RoomNumber: -1, RoomSlot: -1}); !ok {
			t.Fatalf("add player %d", index)
		}
	}
	beginTestMatch(room)
	room.BeginRound(testTime)
	state, _ := room.PrepareSpecialRound()
	king := room.SessionAt(state.Kings[0])
	humans := wolfHuntSlotsForTeam(state.Players, wolfHuntHumanTeam)
	convertedSlot := humans[0]
	converted := room.SessionAt(convertedSlot)
	human := room.SessionAt(humans[1])

	infection := room.RecordKill(king, converted)
	if !infection.WolfHuntConverted || infection.WolfHuntTeam != wolfHuntCreatureTeam ||
		infection.WolfHuntLife.Lives != wolfHuntWolfLives {
		t.Fatalf("Dominate normal-wolf infection = %+v", infection)
	}
	if _, _, ok := room.ReviveSlot(convertedSlot); !ok {
		t.Fatal("converted normal wolf could not use its one life")
	}

	death := room.RecordKill(human, converted)
	converted.mu.RLock()
	team := converted.RoomTeam
	converted.mu.RUnlock()
	if !death.WolfHuntConverted || death.WolfHuntTeam != wolfHuntHumanTeam ||
		death.WolfHuntLife.Lives != 0 || team != wolfHuntHumanTeam || room.WolfKings[convertedSlot] {
		t.Fatalf("Dominate normal-wolf final death = %+v team=%d", death, team)
	}
}

func TestWolfHuntLastWolfDepartureEndsOnlyTheRound(t *testing.T) {
	room := newRoom(1, RoomConfig{Capacity: 2, Mode: 0x180, Wins: 3, Time: 60})
	for index := 0; index < 2; index++ {
		if _, ok := room.Add(&Session{RoomNumber: -1, RoomSlot: -1}); !ok {
			t.Fatalf("add player %d", index)
		}
	}
	beginTestMatch(room)
	room.BeginRound(testTime)
	state, _ := room.PrepareSpecialRound()
	wolf := room.SessionAt(state.Kings[0])
	wolf.mu.RLock()
	departingTeam := wolf.RoomTeam
	wolf.mu.RUnlock()
	if _, _, empty := room.Remove(wolf); empty {
		t.Fatal("human survivor should keep the room open")
	}
	winTeam, endGame, triggered := room.ForfeitEmptyTeam(departingTeam)
	if !triggered || endGame || winTeam != wolfHuntHumanTeam || room.BlueScore != 1 {
		t.Fatalf("Wolf Hunt departure = team %d end=%v triggered=%v blue=%d", winTeam, endGame, triggered, room.BlueScore)
	}
}

func TestWolfHuntInfectionSendsAuthoritativeRedTeam(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	room := newRoom(1, RoomConfig{Capacity: 3, Mode: 0x180, Wins: 3, Time: 60})
	if _, ok := room.Add(harness.session); !ok {
		t.Fatal("add connected player")
	}
	for index := 0; index < 2; index++ {
		if _, ok := room.Add(&Session{RoomNumber: -1, RoomSlot: -1}); !ok {
			t.Fatalf("add peer %d", index)
		}
	}
	beginTestMatch(room)
	room.BeginRound(testTime)
	state, _ := room.PrepareSpecialRound()
	humans := wolfHuntSlotsForTeam(state.Players, wolfHuntHumanTeam)
	killer, reporterSlot, victim := state.Kings[0], humans[0], humans[1]
	reporter := room.SessionAt(reporterSlot)
	harness.server.mu.Lock()
	harness.server.rooms[room.Number] = room
	harness.server.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		payload := make([]byte, 8)
		payload[0], payload[1] = killer, victim
		done <- harness.server.handleMessage(context.Background(), reporter, transport.Frame{
			MessageID: RequestPlayerDead,
			Payload:   payload,
		})
	}()
	assertFrameID(t, harness.client, ReplyPlayerDead)
	teamChange := assertFrameID(t, harness.client, ReplyChangeCreatureTeam)
	life := assertFrameID(t, harness.client, ReplyNoticeRemainLife)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(teamChange.Payload) < 3 || !bytes.Equal(teamChange.Payload[:3], []byte{0, victim, wolfHuntCreatureTeam}) {
		t.Fatalf("change-creature-team payload = %x", teamChange.Payload)
	}
	if len(life.Payload) < 6 || !bytes.Equal(life.Payload[:6], []byte{0, 1, victim, 0, wolfHuntWolfLives, wolfHuntLifeKind}) {
		t.Fatalf("converted-player life payload = %x", life.Payload)
	}
	converted := room.SessionAt(victim)
	converted.mu.RLock()
	convertedTeam := converted.RoomTeam
	converted.mu.RUnlock()
	if convertedTeam != wolfHuntCreatureTeam {
		t.Fatalf("converted victim remained on server team %d", convertedTeam)
	}

	revived := room.SessionAt(victim)
	revived.mu.RLock()
	wantTeam, wantTeamSlot := revived.RoomTeam, revived.RoomTeamSlot
	revived.mu.RUnlock()
	go func() {
		done <- harness.server.handleMessage(context.Background(), revived, transport.Frame{
			MessageID: RequestRevival,
			Payload:   []byte{1, victim},
		})
	}()
	revival := assertFrameID(t, harness.client, ReplyRevival)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(revival.Payload) < 5 || revival.Payload[1] != victim || revival.Payload[2] != wantTeam ||
		revival.Payload[3] != wantTeamSlot || revival.Payload[4] != 1 || wantTeam != wolfHuntCreatureTeam {
		t.Fatalf("converted-player revival payload = %x want slot/team/row=%d/%d/%d", revival.Payload, victim, wantTeam, wantTeamSlot)
	}
	if room.Dead[victim] {
		t.Fatal("revival did not clear the converted player's death state")
	}
}

func wolfHuntSlotsForTeam(players []WolfHuntTeamState, team byte) []byte {
	var slots []byte
	for _, player := range players {
		if player.Team == team {
			slots = append(slots, player.Slot)
		}
	}
	return slots
}

var testTime = func() time.Time { return time.Unix(123, 0) }()
