package game

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"
	"time"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

func TestParseRoomConfig(t *testing.T) {
	w := protocol.NewWriter(64)
	w.U8(8)
	w.UTF16LE("Room")
	w.U8(0)
	w.U8(3)
	w.U32(0x00040202)
	w.U8(5)
	w.U8(0)
	w.U16(600)
	w.U8(100)
	w.U8(0)
	w.U32(0x00c00002)
	w.U8(8)
	w.U8(0x44)
	w.U8(0x55)
	w.U16(0)
	w.U16(0)
	w.U8(0)
	w.U8(0)
	w.U8(12)
	w.U8(34)
	w.U16(100)
	config, err := ParseRoomConfig(w.Clone())
	if err != nil {
		t.Fatal(err)
	}
	if config.Title != "Room" || config.Map != 3 || config.Mode != 0x00040202 || config.EquipmentCount != 0 ||
		config.Time != 600 || config.Points != 100 || config.GameFlags != 2 || config.WeaponFlags != 0 ||
		config.HeavyWeaponLimit != 0x00c0 || config.Capacity != 8 || config.MapSetting != 0x44 || config.RuleSetting != 0x55 ||
		config.ClassLimitMin != 12 || config.ClassLimitMax != 34 || !config.classLimitsSet {
		t.Fatalf("config = %+v", config)
	}
}

func TestParseRoomConfigForLadderRetainsClientSpecialU16Fields(t *testing.T) {
	w := protocol.NewWriter(64)
	w.U8(0).U8(0).
		U8(3).U32(0x00040202).U8(5).U8(0).
		U16(600).U16(100).U8(0).U8(0).U16(0).U8(8).
		U8(0x44).U8(0x55).
		U16(0x1234).U16(0x5678).U8(0).U8(0).U8(12).U8(34).U16(100)

	config, err := ParseRoomConfigForChannel(w.Clone(), store.ChannelTypeLadder)
	if err != nil {
		t.Fatal(err)
	}
	if !config.channelExtrasSet || config.ChannelExtraU16A != 0x1234 || config.ChannelExtraU16B != 0x5678 {
		t.Fatalf("ladder room extras = %+v", config)
	}
	if config.ClassLimitMin != 12 || config.ClassLimitMax != 34 || !config.classLimitsSet {
		t.Fatalf("ladder class limits = %+v", config)
	}
}

func TestCreateRoomFlagsEnableHeroDeathmatch(t *testing.T) {
	// HeroModeOption is encoded as WeaponFlags bit 0 after GameFlags.
	payload := []byte{
		0, 0,
		0x31,
		2, 0, 0, 0,
		0, 5,
		0x58, 2,
		0x64, 0,
		0, heroModeFlag,
		0, 0, 16, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	}
	config, err := ParseRoomConfig(payload)
	if err != nil {
		t.Fatal(err)
	}
	if config.Map != 0x31 || config.Mode != 2 || config.Time != 600 || config.Points != 100 ||
		config.GameFlags != 0 || config.WeaponFlags != heroModeFlag || config.Capacity != 16 {
		t.Fatalf("Hero Deathmatch config = %+v", config)
	}
	if !heroModeEnabled(config) {
		t.Fatal("HeroModeOption did not enable Hero Deathmatch")
	}
	config.WeaponFlags = 0
	config.Mode = 0x10002
	if heroModeEnabled(config) {
		t.Fatal("obsolete synthetic mode bit enabled Hero Deathmatch")
	}
}

func TestParseEnterRoomRequestReadsCountedUTF16Password(t *testing.T) {
	payload := []byte{7, 0, 4, 'p', 0, 'w', 0, 1}
	request, err := ParseEnterRoomRequest(payload)
	if err != nil {
		t.Fatal(err)
	}
	if request.Number != 7 || request.Password != "pw" {
		t.Fatalf("enter-room request = %+v", request)
	}
	if _, err := ParseEnterRoomRequest([]byte{7, 0, 3, 'p', 0, 'w'}); err == nil {
		t.Fatal("odd password byte length was accepted")
	}
}

func TestChangeRulePayloadUsesCompactLayout(t *testing.T) {
	w := protocol.NewWriter(64)
	w.U8(8)
	w.UTF16LE("Room")
	w.U8(0)
	w.U8(3)
	w.U32(2)
	w.U8(5)
	w.U8(0)
	w.U16(600)
	w.U8(100)
	w.U8(0)
	w.U32(0x00c00002)
	w.U8(8)
	w.U8(0x44)
	w.U8(0x55)
	request := append(w.Clone(), make([]byte, 8)...)
	payload, config, err := ChangeRulePayload(request)
	if err != nil {
		t.Fatal(err)
	}
	if payload[0] != 0 || config.Map != 3 || config.Points != 100 || config.HeavyWeaponLimit != 0x00c0 || config.Capacity != 8 {
		t.Fatalf("payload/config = %x %+v", payload, config)
	}
	if len(payload) != 30 || payload[26] != 8 || payload[27] != 0x55 || binary.LittleEndian.Uint16(payload[28:30]) != 100 {
		t.Fatalf("change-rule compact ACK = %x", payload)
	}
}

func TestNoHeavyWeaponsUncheckedRoundTrips(t *testing.T) {
	w := protocol.NewWriter(64)
	w.U8(2)
	w.UTF16LE("R")
	w.U8(0)
	w.U8(3)
	w.U32(2)
	w.U8(5)
	w.U8(5)
	w.U16(600)
	w.U16(100)
	w.U8(2)
	w.U8(0)
	w.U16(0) // LimitHeavyWapon unchecked.
	w.U8(8)
	w.U8(0)
	w.U8(0x55)

	ack, config, err := ChangeRulePayload(w.Clone())
	if err != nil {
		t.Fatal(err)
	}
	if config.HeavyWeaponLimit != 0 || binary.LittleEndian.Uint16(ack[18:20]) != 0 {
		t.Fatalf("unchecked heavy-weapon limit changed: ack=%x config=%+v", ack, config)
	}
	state, err := FieldStatePayload(RoomSnapshot{Number: 7, Config: config})
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint16(state[19:21]) != 0 {
		t.Fatalf("field-state re-enabled heavy-weapon limit: %x", state)
	}
}

func TestCreateRoomPayloadCarriesAllocatedRoomNumberAndClassLimits(t *testing.T) {
	payload := CreateRoomPayload(0x123, RoomConfig{ClassLimitMin: 12, ClassLimitMax: 34})
	if len(payload) != 9 || payload[0] != 0 || !bytes.Equal(payload[1:5], []byte{0, 0, 0, 0}) ||
		binary.LittleEndian.Uint16(payload[5:7]) != 0x123 ||
		payload[7] != 12 || payload[8] != 34 {
		t.Fatalf("create-room payload = %x", payload)
	}
}

func TestFieldEntryCarriesClassLimits(t *testing.T) {
	payload, err := FieldCreatePayload(RoomSnapshot{
		Number: 7,
		Config: RoomConfig{
			Title: "limited", Capacity: 8, ClassLimitMin: 12, ClassLimitMax: 34,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload[len(payload)-2:], []byte{12, 34}) {
		t.Fatalf("field entry class limits = %x", payload)
	}
}

func TestRoomStartTransitionsReadyPlayers(t *testing.T) {
	room := newRoom(1, RoomConfig{Capacity: 8, Map: 3, Mode: 0x040002, Time: 600})
	session := &Session{RoomNumber: -1, RoomSlot: -1}
	if _, ok := room.Add(session); !ok {
		t.Fatal("add session")
	}
	if _, ok := room.SetStatus(session, 1); !ok {
		t.Fatal("set ready")
	}
	changed, gameMap, mode := room.BeginMatch()
	if len(changed) != 1 || changed[0] != 0 || gameMap != 3 || mode != 2 || session.RoomStatus != 3 {
		t.Fatalf("start state = changed %v map %d mode %d session %+v", changed, gameMap, mode, session)
	}
	round, remaining, master := room.BeginRound(time.Unix(123, 0))
	if round != 1 || remaining != 600 || master != 0 || !room.RoundStarted {
		t.Fatalf("round state = round %d remaining %d master %d", round, remaining, master)
	}
}

func TestRoomAddExistingSessionIsIdempotent(t *testing.T) {
	room := newRoom(1, RoomConfig{Capacity: 8, Time: 300})
	session := &Session{RoomNumber: -1, RoomSlot: -1}
	slot, ok := room.Add(session)
	if !ok {
		t.Fatal("add session")
	}
	session.RoomStatus = 4
	room.GameStarted = true
	room.RoundStarted = true
	room.Round = 3
	room.RedScore = 7
	room.BlueScore = 5

	again, ok := room.Add(session)
	if !ok || again != slot {
		t.Fatalf("idempotent add = slot %d ok %v, want slot %d", again, ok, slot)
	}
	snapshot := room.Snapshot()
	if snapshot.Users != 1 || len(snapshot.Members) != 1 || snapshot.Members[0] != session ||
		session.RoomSlot != int16(slot) || session.RoomStatus != 4 || !snapshot.GameStarted ||
		!snapshot.RoundStarted || snapshot.Round != 3 || snapshot.RedScore != 7 || snapshot.BlueScore != 5 {
		t.Fatalf("idempotent add changed live room: snapshot=%+v session=%+v", snapshot, session)
	}
}

func TestRoomAddSerializesCompetingFinalSlot(t *testing.T) {
	room := newRoom(1, RoomConfig{Capacity: 16, Time: 300})
	for index := 0; index < 15; index++ {
		if _, ok := room.Add(&Session{RoomNumber: -1, RoomSlot: -1}); !ok {
			t.Fatalf("fill room slot %d", index)
		}
	}

	start := make(chan struct{})
	results := make(chan bool, 3)
	var wait sync.WaitGroup
	for range 3 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, ok := room.Add(&Session{RoomNumber: -1, RoomSlot: -1})
			results <- ok
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	successes := 0
	for success := range results {
		if success {
			successes++
		}
	}
	snapshot := room.Snapshot()
	if successes != 1 || snapshot.Users != 16 || len(snapshot.Members) != 16 {
		t.Fatalf("competing final slot: successes=%d users=%d members=%d", successes, snapshot.Users, len(snapshot.Members))
	}
}

func TestRoomUpdateConfigAppliesModeOptions(t *testing.T) {
	room := newRoom(1, RoomConfig{Mode: aiModeFlag | 0x00000102})
	room.UpdateConfig(RoomConfig{Mode: 0x00080282})
	if got := room.Snapshot().Config.Mode; got != aiModeFlag|0x00080282 {
		t.Fatalf("updated mode = %#x", got)
	}
}

func TestStartedRoundJoinReturnsRemainingTimeWithoutResettingState(t *testing.T) {
	room := newRoom(1, RoomConfig{Capacity: 8, Time: 300})
	startedAt := time.Unix(1_000, 0)
	room.Round = 4
	room.RoundStarted = true
	room.RoundStartedAt = startedAt
	room.Flags[0] = 2
	room.FlagPoints[3] = 7
	room.PlantTime = startedAt.Add(30 * time.Second)
	room.BombPlanter = 3

	round, remaining, master := room.BeginRound(startedAt.Add(91 * time.Second))
	if round != 4 || remaining != 209 || master != 0 {
		t.Fatalf("joined round state = round %d remaining %d master %d", round, remaining, master)
	}
	if room.RoundStartedAt != startedAt || room.Flags[0] != 2 || room.FlagPoints[3] != 7 ||
		room.PlantTime != startedAt.Add(30*time.Second) || room.BombPlanter != 3 {
		t.Fatalf("joining reset live round state: %+v", room)
	}
}

func TestFieldListLayout(t *testing.T) {
	payload, err := FieldListPayload([]RoomSnapshot{{Number: 7, Config: RoomConfig{Title: "A", Map: 2, Mode: 0x102, Time: 60, Points: 100, Capacity: 8}, Users: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if payload[3] != 1 || binary.LittleEndian.Uint16(payload[4:6]) != 7 || payload[6] != 2 || payload[7] != 'A' {
		t.Fatalf("field list = %x", payload)
	}
}

func TestFieldSynchronizationPayloadLayouts(t *testing.T) {
	room := RoomSnapshot{
		Number:      7,
		GameStarted: true,
		Users:       3,
		Config: RoomConfig{
			Title: "A", Map: 2, Mode: 0x102, Time: 60,
			Points: 100, GameFlags: 0x12, WeaponFlags: 0x34,
			HeavyWeaponLimit: 0x5678, Capacity: 8, RuleSetting: 0x9a,
		},
	}
	created, err := FieldCreatePayload(room)
	if err != nil {
		t.Fatal(err)
	}
	state, err := FieldStatePayload(room)
	if err != nil {
		t.Fatal(err)
	}
	if created[0] != 0 || binary.LittleEndian.Uint16(created[1:3]) != 7 || created[3] != 2 || created[4] != 'A' || created[7] != 1 || binary.LittleEndian.Uint32(created[9:13]) != 0x102 || created[18] != 0x12 || created[19] != 0x34 || binary.LittleEndian.Uint16(created[20:22]) != 0x5678 || created[22] != 8 || created[23] != 3 || created[24] != 0x9a {
		t.Fatalf("single-field payload = %x", created)
	}
	if len(state) != 23 || state[0] != 0 || binary.LittleEndian.Uint16(state[1:3]) != 7 || state[3] != 2 || state[4] != 'A' || state[6] != 0 || state[7] != 2 || binary.LittleEndian.Uint32(state[8:12]) != 0x102 || state[17] != 0x12 || state[18] != 0x34 || binary.LittleEndian.Uint16(state[19:21]) != 0x5678 || state[21] != 8 || state[22] != 0x9a {
		t.Fatalf("field-state payload = %x", state)
	}
	if got := FieldExitPayload(7); !bytes.Equal(got, []byte{0, 7, 0}) {
		t.Fatalf("field-exit payload = %x", got)
	}
	if got := GameStatePayload(7, true); !bytes.Equal(got, []byte{0, 7, 0, 1}) {
		t.Fatalf("game-state payload = %x", got)
	}
	if got := FieldUsersPayload(7, 3); !bytes.Equal(got, []byte{0, 7, 0, 3}) {
		t.Fatalf("field-users payload = %x", got)
	}
	allowed, err := JoinAllowPayload([]RoomSnapshot{
		{Number: 9, Open: true, Config: RoomConfig{Capacity: 8}, Users: 8},
		{Number: 7, Open: true, Config: RoomConfig{Capacity: 8}, Users: 3},
		{Number: 3, Open: false},
		{Number: 5, Open: true, Config: RoomConfig{Capacity: 8, ClassLimitMin: 10, ClassLimitMax: 20}},
	}, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(allowed, []byte{0, 1, 7, 0}) {
		t.Fatalf("join-allow payload = %x", allowed)
	}
}

func TestFieldPayloadsMarkPasswordProtectedRoom(t *testing.T) {
	room := RoomSnapshot{Number: 7, Config: RoomConfig{Title: "A", Password: "pw"}}
	list, err := FieldListPayload([]RoomSnapshot{room})
	if err != nil {
		t.Fatal(err)
	}
	created, err := FieldCreatePayload(room)
	if err != nil {
		t.Fatal(err)
	}
	state, err := FieldStatePayload(room)
	if err != nil {
		t.Fatal(err)
	}
	if list[9] != 1 || created[6] != 1 || state[6] != 1 {
		t.Fatalf("password flags = list %x create %x state %x", list, created, state)
	}
}
