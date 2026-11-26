package game

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"testing"

	"wtserver-go/internal/protocol"
)

func TestRoomRosterContainsMemberIdentity(t *testing.T) {
	room := RoomSnapshot{Number: 2, Master: 0, Config: RoomConfig{Title: "R", Capacity: 8, Map: 1, Mode: 2, Wins: 5, Points: 100, Time: 600}}
	members := []RosterMember{{
		Slot: 0, Team: 1, TeamSlot: 0, SessionID: 0x0831,
		Authority: 12, UnresolvedByte: 3, Level: 7,
		UnresolvedUint32: 0x11223344, PrideMarkID: 0x55667788,
		LoginName: "user", Callsign: "Player", PrideName: "Clan",
		RedCharacter: 30006, BlueCharacter: 30005,
		GlobalIP: netip.MustParseAddr("127.0.0.1"), GlobalPort: 40710,
		LocalIP: netip.MustParseAddr("127.0.0.1"), LocalPort: 40710,
	}}
	payload, err := RoomRosterPayload(room, members)
	if err != nil {
		t.Fatal(err)
	}
	if payload[0] != 0 || payload[1] != 1 || payload[2] != 0 || payload[3] != 2 {
		t.Fatalf("roster prefix = %x", payload[:16])
	}
}

func TestRosterMemberCarriesNamedIdentityFields(t *testing.T) {
	member := RosterMember{
		Slot: 2, Team: 1, TeamSlot: 1, Status: 1, SessionID: 0x1234,
		Authority: 12, UnresolvedByte: 3, Level: 9,
		UnresolvedUint32: 0x11223344, PrideMarkID: 0x55667788,
		LoginName: "user", Callsign: "Player", PrideName: "Clan",
		RedCharacter: 30006, BlueCharacter: 30005,
		GlobalIP: netip.MustParseAddr("203.0.113.7"), GlobalPort: 40710,
		LocalIP: netip.MustParseAddr("192.168.1.7"), LocalPort: 40710,
	}
	writer := protocol.NewWriter(256)
	if err := appendRosterMember(writer, member); err != nil {
		t.Fatal(err)
	}
	payload := writer.Clone()
	if payload[10] != 12 || payload[11] != 3 || payload[12] != 9 {
		t.Fatalf("roster authority/flags/level = %x", payload[10:13])
	}
	if binary.LittleEndian.Uint32(payload[13:17]) != 0x11223344 ||
		binary.LittleEndian.Uint32(payload[17:21]) != 0x55667788 {
		t.Fatalf("roster identity values = %x", payload[13:21])
	}
	if payload[21] != 4 || string(payload[22:26]) != "user" ||
		payload[26] != 12 || string(payload[27:39]) != "P\x00l\x00a\x00y\x00e\x00r\x00" ||
		payload[39] != 8 || string(payload[40:48]) != "C\x00l\x00a\x00n\x00" {
		t.Fatalf("roster display names = %x", payload[21:48])
	}
}

func TestRoomRosterMarksRunningGameState(t *testing.T) {
	room := RoomSnapshot{
		Number:      2,
		Master:      0,
		GameStarted: true,
		Config:      RoomConfig{Title: "R", Capacity: 8, Map: 1, Mode: 2, Wins: 5, Points: 100, Time: 600},
	}
	payload, err := RoomRosterPayload(room, nil)
	if err != nil {
		t.Fatal(err)
	}
	if payload[1] != 2 {
		t.Fatalf("running roster field state = %d, want 2", payload[1])
	}
	if !bytes.Equal(payload[len(payload)-2:], []byte{0, 0x3d}) {
		t.Fatalf("default roster class limits = %x", payload[len(payload)-2:])
	}
}

func TestRoomRosterCarriesRoomPassword(t *testing.T) {
	room := RoomSnapshot{Number: 2, Config: RoomConfig{Title: "R", Password: "pw", Capacity: 8}}
	payload, err := RoomRosterPayload(room, nil)
	if err != nil {
		t.Fatal(err)
	}
	// status, field state, master, room number, title length, UTF-16 title
	passwordOffset := 6 + int(payload[5])
	if payload[passwordOffset] != 4 || !bytes.Equal(payload[passwordOffset+1:passwordOffset+5], []byte{'p', 0, 'w', 0}) {
		t.Fatalf("roster password = %x", payload)
	}
}

func TestRoomRosterCarriesRoomRuleFields(t *testing.T) {
	room := RoomSnapshot{Number: 2, PowerMaster: true, Config: RoomConfig{
		Title: "R", Capacity: 8, Wins: 5, EquipmentCount: 2, Time: 600, Points: 100,
		GameFlags: 0x12, WeaponFlags: 0x34, HeavyWeaponLimit: 0, RuleSetting: 0x56,
		ClassLimitMin: 12, ClassLimitMax: 34,
	}}
	payload, err := RoomRosterPayload(room, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The fixed room rules start after the title/password/map/mode prefix.
	rulesOffset := 6 + int(payload[5])
	rulesOffset += 1 + int(payload[rulesOffset]) + 1 + 4
	if payload[rulesOffset] != 5 || payload[rulesOffset+1] != 2 ||
		binary.LittleEndian.Uint16(payload[rulesOffset+4:rulesOffset+6]) != 600 ||
		binary.LittleEndian.Uint16(payload[rulesOffset+6:rulesOffset+8]) != 100 ||
		payload[rulesOffset+8] != 0x12 || payload[rulesOffset+9] != 0x34 ||
		binary.LittleEndian.Uint16(payload[rulesOffset+10:rulesOffset+12]) != 0 ||
		payload[rulesOffset+15] != 0x56 || payload[rulesOffset+16] != 1 ||
		!bytes.Equal(payload[len(payload)-2:], []byte{12, 34}) {
		t.Fatalf("roster rule fields = %x", payload)
	}
}

func TestEnterRoomCarriesSelectedCharactersAndPowerItems(t *testing.T) {
	member := RosterMember{
		Slot: 3, Team: 2, TeamSlot: 1, SessionID: 0x08e1, LoginName: "user",
		RedCharacter: 30069, BlueCharacter: 30071,
		RedCharItems: [16]uint16{31256, 31271}, BlueCharItems: [16]uint16{30071},
		PowerItems: [15]uint16{19601, 10050},
		GlobalIP:   netip.MustParseAddr("127.0.0.1"), GlobalPort: 40708,
		LocalIP: netip.MustParseAddr("127.0.0.1"), LocalPort: 40708,
	}
	payload, err := EnterRoomPayload(0, member)
	if err != nil {
		t.Fatal(err)
	}
	characters := []byte{3, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0}
	binary.LittleEndian.PutUint16(characters[1:3], member.RedCharacter)
	binary.LittleEndian.PutUint16(characters[3:5], member.RedCharItems[0])
	binary.LittleEndian.PutUint16(characters[5:7], member.RedCharItems[1])
	binary.LittleEndian.PutUint16(characters[8:10], member.BlueCharacter)
	binary.LittleEndian.PutUint16(characters[10:12], member.BlueCharItems[0])
	if !bytes.Contains(payload, characters) {
		t.Fatalf("selected characters missing from enter payload: %x", payload)
	}
	powers := []byte{0, 0x91, 0x4c, 0x42, 0x27}
	if !bytes.Contains(payload, powers) {
		t.Fatalf("power items missing from enter payload: %x", payload)
	}
	if !bytes.Equal(payload[1:7], make([]byte, 6)) || !bytes.Equal(payload[8:12], make([]byte, 4)) {
		t.Fatalf("enter-room reserved fields are not zero: %x", payload[:12])
	}
}

func TestCharacterItemsKeepDefaultBaseBodyWhenNoEquipmentWasSaved(t *testing.T) {
	writer := protocol.NewWriter(8)
	appendCharacterItems(writer, 30006, [16]uint16{})
	want := []byte{2, 0x36, 0x75, 0x36, 0x75}
	if got := writer.Clone(); !bytes.Equal(got, want) {
		t.Fatalf("default character items = %x, want %x", got, want)
	}
}

func TestRoomRosterPayloadHasExpectedLayout(t *testing.T) {
	room := RoomSnapshot{
		Number: 2,
		Master: 1,
		Config: RoomConfig{
			Title: "Room", Password: "pw", Capacity: 8, Map: 3, Mode: 2,
			Wins: 5, Points: 100, Time: 600,
		},
	}
	member := RosterMember{
		Slot: 1, Team: 2, TeamSlot: 0, SessionID: 0x0831, Level: 7, LoginName: "user",
		RedCharacter: 30006, BlueCharacter: 30005,
		RedCharItems: [16]uint16{31256}, PowerItems: [15]uint16{19601},
		GlobalIP: netip.MustParseAddr("203.0.113.7"), GlobalPort: 40710,
		LocalIP: netip.MustParseAddr("192.168.1.7"), LocalPort: 40710,
	}
	payload, err := RoomRosterPayload(room, []RosterMember{member})
	if err != nil {
		t.Fatal(err)
	}

	cursor := newRosterTestCursor(t, payload)
	cursor.skip(5) // result, field state, master, room number
	cursor.countedBytes()
	cursor.countedBytes()
	cursor.skip(6) // map, mode, wins
	cursor.skip(int(cursor.u8()))
	cursor.skip(9) // time, points, flags, heavy limit, capacity
	cursor.skip(4) // two reserved room bytes, rule setting, power-master flag
	memberCount := cursor.u8()
	for range memberCount {
		consumeRosterMember(cursor)
	}
	cursor.skip(2) // class minimum and maximum
	cursor.requireEnd()
}

func TestEnterRoomPayloadHasExpectedLayout(t *testing.T) {
	member := RosterMember{
		Slot: 3, Team: 2, TeamSlot: 1, SessionID: 0x08e1, Level: 9, LoginName: "user",
		RedCharacter: 30069, BlueCharacter: 30071,
		BlueCharItems: [16]uint16{31271}, PowerItems: [15]uint16{19601, 10050},
		GlobalIP: netip.MustParseAddr("203.0.113.8"), GlobalPort: 40708,
		LocalIP: netip.MustParseAddr("10.0.0.8"), LocalPort: 40708,
	}
	payload, err := EnterRoomPayload(0, member)
	if err != nil {
		t.Fatal(err)
	}

	cursor := newRosterTestCursor(t, payload)
	cursor.skip(7) // result and six skipped bytes
	cursor.skip(1) // slot
	cursor.skip(4) // second skipped range
	cursor.skip(15)
	consumeRosterNames(cursor)
	consumeRosterStatsAndCharacters(cursor)
	cursor.skip(5)  // extra u16 and three one-byte fields
	cursor.skip(12) // global and local IPv4 endpoints
	consumeRosterTailRecords(cursor)
	cursor.requireEnd()
}

type rosterTestCursor struct {
	t       *testing.T
	payload []byte
	offset  int
}

func newRosterTestCursor(t *testing.T, payload []byte) *rosterTestCursor {
	t.Helper()
	return &rosterTestCursor{t: t, payload: payload}
}

func (cursor *rosterTestCursor) u8() byte {
	cursor.t.Helper()
	if cursor.offset >= len(cursor.payload) {
		cursor.t.Fatalf("read at end of %d-byte payload", len(cursor.payload))
	}
	value := cursor.payload[cursor.offset]
	cursor.offset++
	return value
}

func (cursor *rosterTestCursor) skip(count int) {
	cursor.t.Helper()
	if count < 0 || cursor.offset+count > len(cursor.payload) {
		cursor.t.Fatalf("skip %d at offset %d exceeds %d-byte payload", count, cursor.offset, len(cursor.payload))
	}
	cursor.offset += count
}

func (cursor *rosterTestCursor) countedBytes() {
	cursor.t.Helper()
	cursor.skip(int(cursor.u8()))
}

func (cursor *rosterTestCursor) requireEnd() {
	cursor.t.Helper()
	if cursor.offset != len(cursor.payload) {
		cursor.t.Fatalf("client stopped at offset %d of %d; %d trailing bytes", cursor.offset, len(cursor.payload), len(cursor.payload)-cursor.offset)
	}
}

func consumeRosterMember(cursor *rosterTestCursor) {
	cursor.t.Helper()
	cursor.skip(7)  // slot, skipped u32, team, team slot
	cursor.skip(14) // status, session, two byte fields, level, two u32 fields
	consumeRosterNames(cursor)
	cursor.skip(12) // global and local IPv4 endpoints
	cursor.skip(1)  // room status
	consumeRosterStatsAndCharacters(cursor)
	cursor.skip(3) // extra u16 and marker
	consumeRosterTailRecords(cursor)
}

func consumeRosterNames(cursor *rosterTestCursor) {
	cursor.t.Helper()
	cursor.countedBytes()
	cursor.countedBytes()
	cursor.countedBytes()
}

func consumeRosterStatsAndCharacters(cursor *rosterTestCursor) {
	cursor.t.Helper()
	statRegionCount := cursor.u8()
	if statRegionCount != 1 {
		cursor.t.Fatalf("stat region count = %d, want one client-consumed region", statRegionCount)
	}
	for range statRegionCount {
		cursor.skip(1 + rosterPowerItemCount*2 + 2)
	}
	for range 2 {
		cursor.skip(int(cursor.u8()) * 2)
	}
}

func consumeRosterTailRecords(cursor *rosterTestCursor) {
	cursor.t.Helper()
	if count := cursor.u8(); count != 0 {
		cursor.t.Fatalf("roster tail record count = %d, want empty compatibility list", count)
	}
}
