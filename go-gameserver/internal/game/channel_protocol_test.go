package game

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"wtserver-go/internal/store"
)

func TestParseLoginCredentials(t *testing.T) {
	payload := []byte{0, 0, 2, 0xaa, 0xbb, 4, 'u', 's', 'e', 'r', 4, 'p', 'a', 's', 's'}
	got, err := ParseLoginCredentials(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "user" || got.Password != "pass" {
		t.Fatalf("credentials = %+v", got)
	}
}

func TestLoginReplyLayout(t *testing.T) {
	payload, err := LoginReplyPayload(0, 0x0831, 42, store.ChannelTypeNormal, store.AccountSnapshot{Callsign: "Call", Gold: 10, WC: 20, Cash: 30})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0,
		0x31, 0x08,
		42, 0, 0, 0,
		10, 0, 0, 0,
		0, 0, 0, 0,
		0, 0,
		20, 0, 0, 0,
		0,
		0, // EventTopBanner (0 = hidden; sub_3408DA10 -> manager vtable +228)
		30, 0, 0, 0,
		0, 0, 0,
	}
	if !bytes.Equal(payload, want) {
		t.Fatalf("login payload = %x, want %x", payload, want)
	}
	if bytes.Contains(payload, []byte("Call")) {
		t.Fatalf("login payload incorrectly contains callsign: %x", payload)
	}
}

func TestLoginReplyUsesConfiguredChannelType(t *testing.T) {
	payload, err := LoginReplyPayload(0, 0x0831, 42, store.ChannelTypePrivateLadder, store.AccountSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint16(payload[15:17]); got != uint16(store.ChannelTypePrivateLadder) {
		t.Fatalf("channel type = %d, want %d", got, store.ChannelTypePrivateLadder)
	}
}

func TestLoginReplyCarriesActiveMuteIntervalAndReason(t *testing.T) {
	startedAt := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	payload, err := LoginReplyPayload(0, 0x0831, 42, store.ChannelTypeNormal, store.AccountSnapshot{
		Muted:         true,
		MuteStartedAt: startedAt,
		MuteExpiresAt: startedAt.Add(2 * time.Hour),
		MuteReason:    "spam",
	})
	if err != nil {
		t.Fatal(err)
	}
	if payload[21] != 1 || payload[27] != 13 ||
		string(payload[28:41]) != "202607291400\x00" ||
		payload[41] != 13 || string(payload[42:55]) != "202607291600\x00" ||
		payload[55] != 8 || string(payload[56:]) != "s\x00p\x00a\x00m\x00" {
		t.Fatalf("login mute block = %x", payload[21:])
	}
}

func TestLoginReplyRejectsInvalidChannelType(t *testing.T) {
	if _, err := LoginReplyPayload(0, 0x0831, 42, store.ChannelType(5), store.AccountSnapshot{}); err == nil {
		t.Fatal("expected invalid channel type error")
	}
}

func TestChannelKindWireValues(t *testing.T) {
	kinds := []channelKind{
		channelKindNormal,
		channelKindSpat,
		channelKindPrideBattle,
		channelKindLadder,
		channelKindPrivateLadder,
	}
	for want, got := range kinds {
		if got != channelKind(want) {
			t.Fatalf("channel kind %d = %d", want, got)
		}
	}
}

func TestUserInfoStartsWithNameLengthAndKeepsLevelAligned(t *testing.T) {
	payload, err := UserInfoPayload("Player", store.AccountSnapshot{
		Authority: 13, Level: 34, GP: 999999, Gold: 1234, WC: 5678, Cash: 9012,
		Wins: 9, Losses: 4, Draws: 5, Kills: 42, Deaths: 7,
		HeadshotKills: 6, SpecialWeaponKills: 7, WolfKills: 8, PlayTimeSeconds: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	nameBytes := len("Player") * 2
	if payload[0] != 0 || payload[1] != byte(nameBytes) || string(payload[2:2+nameBytes]) != "P\x00l\x00a\x00y\x00e\x00r\x00" {
		t.Fatalf("user-info name prefix = %x", payload[:2+nameBytes])
	}
	tail := payload[2+nameBytes:]
	// 2013 TR layout (TestUserInfo2013 checks it byte for byte): 12 u32 after
	// the level (play time last), a P.T. bonus byte, nothing after Pride ID 0.
	if len(tail) != 90 || tail[0] != 13 || tail[1] != 0 ||
		binary.LittleEndian.Uint32(tail[6:10]) != 1234 ||
		binary.LittleEndian.Uint32(tail[10:14]) != 5678 ||
		binary.LittleEndian.Uint32(tail[14:18]) != 9012 ||
		binary.LittleEndian.Uint32(tail[22:26]) != 999999 ||
		binary.LittleEndian.Uint32(tail[26:30]) != 389000 || tail[31] != 34 ||
		binary.LittleEndian.Uint32(tail[44:48]) != 9 || binary.LittleEndian.Uint32(tail[48:52]) != 4 ||
		binary.LittleEndian.Uint32(tail[56:60]) != 42 || binary.LittleEndian.Uint32(tail[60:64]) != 7 ||
		binary.LittleEndian.Uint32(tail[40:44]) != 5 ||
		binary.LittleEndian.Uint32(tail[64:68]) != 7 ||
		binary.LittleEndian.Uint32(tail[68:72]) != 6 ||
		binary.LittleEndian.Uint32(tail[72:76]) != 8 ||
		tail[85] != 0 || binary.LittleEndian.Uint32(tail[86:90]) != 0 {
		t.Fatalf("user-info aligned fields = %x", tail)
	}
	withSchool, err := userInfoWithPrideAndPlayTimePayload("Player", store.AccountSnapshot{}, store.Pride{}, store.PrideMember{}, 7, 300)
	if err != nil {
		t.Fatal(err)
	}
	if schoolTail := withSchool[2+nameBytes:]; binary.LittleEndian.Uint32(schoolTail[2:6]) != 7 {
		t.Fatalf("user-info school = %x", schoolTail[2:6])
	}
}

func TestUserInfoRestoresPersistedPrideState(t *testing.T) {
	createdAt := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	payload, err := UserInfoWithPridePayload("Admin", store.AccountSnapshot{Level: 28}, store.Pride{
		ID: 22, Name: "test", Master: "Admin", RankPoints: 44, MemberCount: 1,
		Intro: "This is a test", Notice: "Notice", CreatedAt: createdAt,
	}, store.PrideMember{Role: 8})
	if err != nil {
		t.Fatal(err)
	}
	tailOffset := 2 + len("Admin")*2
	prideOffset := tailOffset + 86 // 2013 TR: 12 stat u32 + P.T. bonus byte
	if binary.LittleEndian.Uint32(payload[prideOffset:prideOffset+4]) != 22 {
		t.Fatalf("user-info pride id = %x", payload[prideOffset:])
	}
	offset := prideOffset + 4
	name, offset, err := readPrideUTF16(payload, offset)
	if err != nil {
		t.Fatal(err)
	}
	master, offset, err := readPrideUTF16(payload, offset)
	if err != nil {
		t.Fatal(err)
	}
	if name != "test" || master != "Admin" || binary.LittleEndian.Uint32(payload[offset:offset+4]) != 44 {
		t.Fatalf("user-info pride identity = %q/%q/%x", name, master, payload[offset:])
	}
	offset += 4
	if payload[offset] != 1 {
		t.Fatalf("user-info pride member count byte = %d", payload[offset])
	}
	offset++
	date, offset, err := readPrideASCII(payload, offset)
	if err != nil {
		t.Fatal(err)
	}
	offset += 2 // pride grade
	intro, offset, err := readPrideUTF16(payload, offset)
	if err != nil {
		t.Fatal(err)
	}
	notice, offset, err := readPrideUTF16(payload, offset)
	if err != nil {
		t.Fatal(err)
	}
	if date != "20260718" || intro != "This is a test" || notice != "Notice" || payload[offset] != 0 || payload[offset+1] != 8 { // grade 0 (master), then the unread role copy
		t.Fatalf("user-info pride details = date %q intro %q notice %q tail %x", date, intro, notice, payload[offset:])
	}
}

func TestUDPSuccessLayout(t *testing.T) {
	payload, err := UDPSuccessPayload(netip.MustParseAddr("127.0.0.1"), 40707)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 0x7f, 0, 0, 1, 0x9f, 0x03, 0x7f, 0, 0, 1, 0x9f, 0x03}
	if !bytes.Equal(payload, want) {
		t.Fatalf("UDP success = %x, want %x", payload, want)
	}
}

func TestChannelControlPayloadsHaveExpectedLength(t *testing.T) {
	selected, err := SelectCharPayload(netip.MustParseAddr("127.0.0.1"), 40707)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		got  []byte
		want []byte
	}{
		{name: "UDP address", got: UDPAddressPayload(), want: []byte{0}},
		{name: "select character", got: selected, want: []byte{0, 0x7f, 0, 0, 1, 0x9f, 0x03, 0}},
		{name: "disconnect", got: DisconnectPayload(), want: []byte{0}},
		{name: "logout", got: LogoutPayload(), want: []byte{0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !bytes.Equal(test.got, test.want) {
				t.Fatalf("payload = %x, want %x", test.got, test.want)
			}
		})
	}
}
