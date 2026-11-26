package game

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

// reader2012 mirrors the 2012 CShell packet cursor (packet+0x2004): every read
// advances by the field size, and tests require that the whole payload is
// consumed. Index 0 is the common result byte consumed by the pump.
type reader2012 struct {
	t   *testing.T
	b   []byte
	pos int
}

func newReader2012(t *testing.T, payload []byte) *reader2012 {
	t.Helper()
	if len(payload) == 0 || payload[0] != 0 {
		t.Fatalf("payload has no success result byte: %x", payload)
	}
	return &reader2012{t: t, b: payload, pos: 1}
}

func (r *reader2012) need(n int) []byte {
	r.t.Helper()
	if r.pos+n > len(r.b) {
		r.t.Fatalf("read %d at %d exceeds %d-byte payload %x", n, r.pos, len(r.b), r.b)
	}
	out := r.b[r.pos : r.pos+n]
	r.pos += n
	return out
}
func (r *reader2012) u8() byte    { return r.need(1)[0] }
func (r *reader2012) u16() uint16 { return binary.LittleEndian.Uint16(r.need(2)) }
func (r *reader2012) u32() uint32 { return binary.LittleEndian.Uint32(r.need(4)) }
func (r *reader2012) ascii() string {
	return string(r.need(int(r.u8())))
}
func (r *reader2012) utf16() string {
	raw := r.need(int(r.u8()))
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(raw[2*i:])
	}
	return string(utf16.Decode(units))
}
func (r *reader2012) done() {
	r.t.Helper()
	if r.pos != len(r.b) {
		r.t.Fatalf("client consumed %d of %d bytes: %x", r.pos, len(r.b), r.b)
	}
}

func utf16Field(w *protocol.Writer, value string) {
	units := utf16.Encode([]rune(value))
	w.U8(byte(2 * len(units)))
	for _, u := range units {
		w.U16(u)
	}
}

type clientRoomConfig2012 struct {
	Title, Password               string
	Map                           byte
	Mode                          uint32
	Wins, EquipCount              byte
	Time, Points                  uint16
	Flags, Weapon                 byte
	Heavy                         uint16
	Capacity, MapSetting, Rule    byte
	LadderA, LadderB              uint16
	ClassByte, ClassMin, ClassMax byte
}

// client2012CreateRequest translates 2013 TR CS_FD_CREATE_REQ (sub_34184270):
// the weapon-flags byte follows the game flags again.
func client2012CreateRequest(c clientRoomConfig2012) []byte {
	w := protocol.NewWriter(96)
	utf16Field(w, c.Title)
	utf16Field(w, c.Password)
	w.U8(c.Map).U32(c.Mode).U8(c.Wins).U8(c.EquipCount).U16(c.Time).U16(c.Points).U8(c.Flags).U8(c.Weapon).
		U16(c.Heavy).U8(c.Capacity).U8(c.MapSetting).U8(c.Rule).
		U16(c.LadderA).U16(c.LadderB).U8(c.ClassByte).U8(c.ClassByte).U8(c.ClassMin).U8(c.ClassMax).U16(c.Points)
	return w.Clone()
}

// client2012ChangeRuleRequest translates 2013 TR CS_FD_CHANGERULE_REQ (sub_34249710).
func client2012ChangeRuleRequest(c clientRoomConfig2012, currentMode uint32) []byte {
	w := protocol.NewWriter(96)
	utf16Field(w, c.Title)
	utf16Field(w, c.Password)
	w.U8(c.Map).U32(c.Mode).U8(c.Wins).U8(c.EquipCount).U16(c.Time).U16(c.Points).U8(c.Flags).U8(c.Weapon).
		U16(c.Heavy).U8(c.Capacity).U8(c.MapSetting).U8(c.Rule).
		U32(currentMode).U8(0).U8(0).U16(c.Points)
	return w.Clone()
}

// withPadding adds the garbage the 2012 client leaves in the AES padding.
func withPadding(body []byte) []byte {
	padded := append([]byte(nil), body...)
	for len(padded)%16 != 0 {
		padded = append(padded, 0xcc)
	}
	return padded
}

var sampleRoom2012 = clientRoomConfig2012{
	Title: "Gel", Password: "pw", Map: 3, Mode: 0x00040202, Wins: 5, EquipCount: 5,
	Time: 600, Points: 100, Flags: 2, Weapon: 0x30, Heavy: 0x00c0, Capacity: 8, MapSetting: 0x44, Rule: 0x55,
	ClassMin: 12, ClassMax: 34,
}

func TestParseCreateRoom2012(t *testing.T) {
	config, err := ParseRoomConfig(withPadding(client2012CreateRequest(sampleRoom2012)))
	if err != nil {
		t.Fatal(err)
	}
	if config.Title != "Gel" || config.Password != "pw" || config.Map != 3 || config.Mode != 0x00040202 ||
		config.Wins != 5 || config.EquipmentCount != 5 || config.Time != 600 || config.Points != 100 ||
		config.GameFlags != 2 || config.WeaponFlags != 0x30 || config.HeavyWeaponLimit != 0x00c0 ||
		config.Capacity != 8 || config.MapSetting != 0x44 || config.RuleSetting != 0x55 ||
		config.ClassLimitMin != 12 || config.ClassLimitMax != 34 || !config.classLimitsSet {
		t.Fatalf("2012 create config = %+v", config)
	}

	ladder := sampleRoom2012
	ladder.LadderA, ladder.LadderB = 0x1234, 0x5678
	config, err = ParseRoomConfigForChannel(client2012CreateRequest(ladder), store.ChannelTypeLadder)
	if err != nil {
		t.Fatal(err)
	}
	if !config.channelExtrasSet || config.ChannelExtraU16A != 0x1234 || config.ChannelExtraU16B != 0x5678 ||
		config.ClassLimitMin != 12 || config.ClassLimitMax != 34 {
		t.Fatalf("2012 ladder create config = %+v", config)
	}
}

// client2012ParseChangeRuleAck translates 2013 TR CS_FD_CHANGERULE_ACK (sub_34184920).
func client2012ParseChangeRuleAck(t *testing.T, payload []byte) clientRoomConfig2012 {
	r := newReader2012(t, payload)
	var c clientRoomConfig2012
	c.Title, c.Password = r.utf16(), r.utf16()
	c.Map, c.Mode, c.Wins, c.EquipCount = r.u8(), r.u32(), r.u8(), r.u8()
	c.Time, c.Points, c.Flags, c.Weapon, c.Heavy = r.u16(), r.u16(), r.u8(), r.u8(), r.u16()
	c.Capacity, c.Rule = r.u8(), r.u8()
	c.Points = r.u16()
	r.done()
	return c
}

func TestChangeRule2012RoundTrip(t *testing.T) {
	ack, config, err := ChangeRulePayload(withPadding(client2012ChangeRuleRequest(sampleRoom2012, 0x00040202)))
	if err != nil {
		t.Fatal(err)
	}
	if config.HeavyWeaponLimit != 0x00c0 || config.Capacity != 8 || config.RuleSetting != 0x55 {
		t.Fatalf("change-rule config = %+v", config)
	}
	got := client2012ParseChangeRuleAck(t, ack)
	if got.Title != "Gel" || got.Password != "pw" || got.Map != 3 || got.Mode != 0x00040202 || got.Time != 600 ||
		got.Points != 100 || got.Flags != 2 || got.Weapon != 0x30 || got.Heavy != 0x00c0 || got.Capacity != 8 || got.Rule != 0x55 {
		t.Fatalf("client change-rule view = %+v", got)
	}
}

func TestCreateRoomAck2012(t *testing.T) {
	// 2013 TR CS_FD_CREATE_ACK (sub_3420E360): skip u32, u16 room, u8 class min, u8 class max.
	r := newReader2012(t, CreateRoomPayload(0x123, RoomConfig{ClassLimitMin: 12, ClassLimitMax: 34}))
	r.need(4)
	if room, lo, hi := r.u16(), r.u8(), r.u8(); room != 0x123 || lo != 12 || hi != 34 {
		t.Fatalf("create ACK = %d %d %d", room, lo, hi)
	}
	r.done()
}

type clientFieldEntry2012 struct {
	Number                        uint16
	Title                         string
	Password, Started, Map        byte
	Mode                          uint32
	Wins                          byte
	Time, Points                  uint16
	Flags, Weapon                 byte
	Heavy                         uint16
	Capacity, Users, Rule         byte
	Reserved, PowerMaster, Lo, Hi byte
}

// client2012ParseFieldEntry translates one room of CS_FD_FIELDLIST_ACK
// (2013 TR sub_34213320); CS_FD_FIELDCREATE_ACK (sub_34213070) reads the same entry.
func client2012ParseFieldEntry(r *reader2012) clientFieldEntry2012 {
	var e clientFieldEntry2012
	e.Number, e.Title = r.u16(), r.utf16()
	e.Password, e.Started, e.Map, e.Mode, e.Wins = r.u8(), r.u8(), r.u8(), r.u32(), r.u8()
	e.Time, e.Points, e.Flags, e.Weapon, e.Heavy = r.u16(), r.u16(), r.u8(), r.u8(), r.u16()
	e.Capacity, e.Users, e.Rule = r.u8(), r.u8(), r.u8()
	e.Reserved, e.PowerMaster, e.Lo, e.Hi = r.u8(), r.u8(), r.u8(), r.u8()
	return e
}

var fieldRoom2012 = RoomSnapshot{
	Number: 7, GameStarted: true, Users: 3, PowerMaster: true,
	Config: RoomConfig{
		Title: "A", Password: "pw", Map: 2, Mode: 0x102, Wins: 5, Time: 60, Points: 100, GameFlags: 0x12,
		WeaponFlags: 0x34, HeavyWeaponLimit: 0x5678, Capacity: 8, RuleSetting: 0x9a,
		ClassLimitMin: 12, ClassLimitMax: 34, classLimitsSet: true,
	},
}

func checkFieldEntry2012(t *testing.T, e clientFieldEntry2012) {
	t.Helper()
	if e.Number != 7 || e.Title != "A" || e.Password != 1 || e.Started != 1 || e.Map != 2 || e.Mode != 0x102 ||
		e.Wins != 5 || e.Time != 60 || e.Points != 100 || e.Flags != 0x12 || e.Weapon != 0x34 || e.Heavy != 0x5678 ||
		e.Capacity != 8 || e.Users != 3 || e.Rule != 0x9a || e.PowerMaster != 1 || e.Lo != 12 || e.Hi != 34 {
		t.Fatalf("client field entry = %+v", e)
	}
}

func TestFieldList2012(t *testing.T) {
	payload, err := FieldListPayload([]RoomSnapshot{fieldRoom2012})
	if err != nil {
		t.Fatal(err)
	}
	r := newReader2012(t, payload)
	r.u8() // 2013 TR: two u8 before the room count
	r.u8()
	if n := r.u8(); n != 1 {
		t.Fatalf("room count = %d", n)
	}
	checkFieldEntry2012(t, client2012ParseFieldEntry(r))
	r.done()

	empty, _ := FieldListPayload(nil)
	r = newReader2012(t, empty)
	r.u8()
	r.u8()
	if n := r.u8(); n != 0 {
		t.Fatalf("empty room count = %d", n)
	}
	r.done()

	created, err := FieldCreatePayload(fieldRoom2012)
	if err != nil {
		t.Fatal(err)
	}
	r = newReader2012(t, created)
	checkFieldEntry2012(t, client2012ParseFieldEntry(r))
	r.done()
}

func TestFieldState2012(t *testing.T) {
	// 2013 TR CS_FD_FIELDSTATE_ACK (sub_3420F450).
	payload, err := FieldStatePayload(fieldRoom2012)
	if err != nil {
		t.Fatal(err)
	}
	r := newReader2012(t, payload)
	number, title, password, gameMap, mode, wins := r.u16(), r.utf16(), r.u8(), r.u8(), r.u32(), r.u8()
	tm, points, flags, weapon, heavy, capacity, rule := r.u16(), r.u16(), r.u8(), r.u8(), r.u16(), r.u8(), r.u8()
	r.done()
	if number != 7 || title != "A" || password != 1 || gameMap != 2 || mode != 0x102 || wins != 5 ||
		tm != 60 || points != 100 || flags != 0x12 || weapon != 0x34 || heavy != 0x5678 || capacity != 8 || rule != 0x9a {
		t.Fatal("field-state fields do not match")
	}
}

type clientMember2012 struct {
	Slot, Team, TeamSlot, Status byte
	Session                      uint16
	Authority, B, Level          byte
	U384, PrideMark              uint32
	Login, Callsign, Pride       string
	GlobalIP                     uint32
	GlobalPort                   uint16
	LocalIP                      uint32
	LocalPort                    uint16
	RoomStatus                   byte
	Power                        [15]uint16
	Red, Blue                    []uint16
	Tail16                       uint16
	Tail8                        byte
}

func readPowerSets2012(r *reader2012, m *clientMember2012) {
	sets := r.u8()
	if sets > 4 {
		sets = 4
	}
	for set := byte(0); set < sets; set++ {
		r.u8()
		for i := range m.Power {
			value := r.u16()
			if set == 0 {
				m.Power[i] = value
			}
		}
		r.u16()
	}
}

func readItemList2012(r *reader2012) []uint16 {
	n := r.u8()
	out := make([]uint16, n)
	for i := range out {
		out[i] = r.u16()
	}
	return out
}

// client2012ParseRosterMember translates the 2013 TR CS_FD_CHARS_ACK member
// loop (sub_3418AE90: slot, skip u32, team, team slot) and the human member
// body (sub_3418A870, which ends with a u8-counted tail record list).
func client2012ParseRosterMember(r *reader2012) clientMember2012 {
	var m clientMember2012
	m.Slot = r.u8()
	r.need(4)
	m.Team, m.TeamSlot = r.u8(), r.u8()
	m.Status, m.Session, m.Authority, m.B, m.Level = r.u8(), r.u16(), r.u8(), r.u8(), r.u8()
	m.U384, m.PrideMark = r.u32(), r.u32()
	m.Login, m.Callsign, m.Pride = r.ascii(), r.utf16(), r.utf16()
	m.GlobalIP, m.GlobalPort, m.LocalIP, m.LocalPort = r.u32(), r.u16(), r.u32(), r.u16()
	m.RoomStatus = r.u8()
	readPowerSets2012(r, &m)
	m.Red, m.Blue = readItemList2012(r), readItemList2012(r)
	m.Tail16, m.Tail8 = r.u16(), r.u8()
	if n := r.u8(); n != 0 {
		panic("unexpected roster tail records")
	}
	return m
}

var sampleMember2012 = RosterMember{
	Slot: 2, Team: 1, TeamSlot: 3, Status: 4, RoomStatus: 1, SessionID: 0x0102, Authority: 13, UnresolvedByte: 7,
	Level: 34, UnresolvedUint32: 0x11223344, PrideMarkID: 9, LoginName: "login", Callsign: "Kurt", PrideName: "P",
	RedCharacter: 1001, BlueCharacter: 1002, RedCharItems: [16]uint16{2001}, BlueCharItems: [16]uint16{},
	PowerItems: [15]uint16{5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
	GlobalIP:   netip.MustParseAddr("1.2.3.4"), GlobalPort: 0x1234,
	LocalIP: netip.MustParseAddr("10.0.0.2"), LocalPort: 0x5678,
}

func checkMember2012(t *testing.T, m clientMember2012) {
	t.Helper()
	if m.Slot != 2 || m.Team != 1 || m.TeamSlot != 3 || m.Session != 0x0102 || m.Authority != 13 ||
		m.B != 7 || m.Level != 34 || m.U384 != 0x11223344 || m.PrideMark != 9 || m.Login != "login" ||
		m.Callsign != "Kurt" || m.Pride != "P" || m.Power[0] != 5 || m.Power[14] != 19 ||
		len(m.Red) != 2 || m.Red[0] != 1001 || m.Red[1] != 2001 || len(m.Blue) != 2 || m.Blue[1] != 1002 ||
		m.GlobalPort != 0x3412 || m.LocalPort != 0x7856 {
		t.Fatalf("client member = %+v", m)
	}
}

func TestRoomRoster2012(t *testing.T) {
	room := RoomSnapshot{
		Number: 7, Master: 2, PowerMaster: true,
		Config: RoomConfig{
			Title: "Gel", Password: "pw", Map: 3, Mode: 0x102, Wins: 5, Time: 600, Points: 100,
			GameFlags: 2, WeaponFlags: 0x34, HeavyWeaponLimit: 0x00c0, Capacity: 8, RuleSetting: 0x55,
			ClassLimitMin: 12, ClassLimitMax: 34, classLimitsSet: true,
		},
	}
	payload, err := RoomRosterPayload(room, []RosterMember{sampleMember2012})
	if err != nil {
		t.Fatal(err)
	}
	// CS_FD_CHARS_ACK room part (sub_342308F0).
	r := newReader2012(t, payload)
	state, master, number, title, password := r.u8(), r.u8(), r.u16(), r.utf16(), r.utf16()
	gameMap, mode, wins := r.u8(), r.u32(), r.u8()
	equip := r.u8()
	r.need(int(equip))
	tm, points, flags, weapon, heavy, capacity := r.u16(), r.u16(), r.u8(), r.u8(), r.u16(), r.u8()
	r.u8()
	r.u8()
	rule, powerMaster := r.u8(), r.u8()
	count := r.u8()
	if state != 1 || master != 2 || number != 7 || title != "Gel" || password != "pw" || gameMap != 3 ||
		mode != 0x102 || wins != 5 || equip != 5 || tm != 600 || points != 100 || flags != 2 || weapon != 0x34 ||
		heavy != 0x00c0 || capacity != 8 || rule != 0x55 || powerMaster != 1 || count != 1 {
		t.Fatalf("roster room fields = %x", payload)
	}
	checkMember2012(t, client2012ParseRosterMember(r))
	if lo, hi := r.u8(), r.u8(); lo != 12 || hi != 34 {
		t.Fatalf("roster class limits = %d/%d", lo, hi)
	}
	r.done()
}

func TestEnterRoom2012(t *testing.T) {
	payload, err := EnterRoomPayload(0, sampleMember2012)
	if err != nil {
		t.Fatal(err)
	}
	// 2013 TR CS_FD_ENTER_ACK (sub_34187040): skip 6, slot, skip 4, team,
	// team slot, ...; ends with the u8-counted tail record list.
	r := newReader2012(t, payload)
	var m clientMember2012
	r.need(6)
	m.Slot = r.u8()
	r.need(4)
	m.Team, m.TeamSlot = r.u8(), r.u8()
	m.Session, m.Authority, m.B, m.Level = r.u16(), r.u8(), r.u8(), r.u8()
	m.U384, m.PrideMark = r.u32(), r.u32()
	m.Login, m.Callsign, m.Pride = r.ascii(), r.utf16(), r.utf16()
	readPowerSets2012(r, &m)
	m.Red, m.Blue = readItemList2012(r), readItemList2012(r)
	m.Tail16, m.Tail8 = r.u16(), r.u8()
	r.u8() // room +150
	r.u8() // room +172
	m.GlobalIP, m.GlobalPort, m.LocalIP, m.LocalPort = r.u32(), r.u16(), r.u32(), r.u16()
	if n := r.u8(); n != 0 {
		t.Fatalf("enter tail records = %d", n)
	}
	r.done()
	checkMember2012(t, m)
}
