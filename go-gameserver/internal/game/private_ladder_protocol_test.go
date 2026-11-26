package game

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"wtserver-go/internal/store"
)

func TestPrivateLadderLayoutsMatchProtocol(t *testing.T) {
	rankRequest, err := ParsePrivateLadderRankRequest([]byte{2, 9, 3, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	if err != nil || rankRequest.Page != 2 || rankRequest.PageSize != 9 || rankRequest.TopSize != 3 || rankRequest.Scope != 1 {
		t.Fatalf("rank request = %+v, %v", rankRequest, err)
	}
	// sub_342146E0: u32 mode word, u8 map, u8 capacity.
	search, err := ParsePrivateLadderSearchRequest([]byte{0x12, 4, 0, 0, 7, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	if err != nil || search.ModeWord != 0x412 || search.Map != 7 || search.Capacity != 2 {
		t.Fatalf("search request = %+v, %v", search, err)
	}
	searchEnd, err := ParsePrivateLadderSearchEndRequest([]byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	if err != nil || searchEnd != 1 {
		t.Fatalf("search end request = %d, %v", searchEnd, err)
	}
	if got := PrivateLadderInfoPayload(store.PrivateLadderInfo{Rank: 2, Points: 99}); !bytes.Equal(got, []byte{0, 99, 0, 0, 0, 2, 0, 0, 0}) {
		t.Fatalf("private info = %x", got)
	}

	ranks, err := PrivateLadderRanksPayload([]store.PrivateLadderRank{{
		Username: "u", Callsign: "A", Level: 7, PrideMarkID: 5, Points: 99, Rank: 1, Status: 2,
	}}, nil)
	wantRanks := []byte{
		0, 1, 1, 'u', 2, 'A', 0, 7,
		5, 0, 0, 0, 99, 0, 0, 0, 1, 0, 0, 0, 2,
		0,
	}
	if err != nil || !bytes.Equal(ranks, wantRanks) {
		t.Fatalf("private ranks = %x, %v; want %x", ranks, err, wantRanks)
	}

	start := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	notice, err := PrivateLadderNoticePayload(store.PrivateLadderNotice{Title: "T", StartsOn: &start, Summary: "S", Details: "D"})
	if err != nil {
		t.Fatal(err)
	}
	offset := 1
	if notice[0] != 0 {
		t.Fatalf("notice status = %x", notice)
	}
	if notice[offset] != 2 || string(notice[offset+1:offset+3]) != "T\x00" {
		t.Fatalf("notice title = %x", notice)
	}
	offset += 3 + 6
	if notice[offset] != 8 || string(notice[offset+1:offset+9]) != "20260718" {
		t.Fatalf("notice date = %x", notice)
	}
	offset += 9
	if notice[offset] != 0 {
		t.Fatalf("notice end date = %x", notice)
	}
	offset++
	offset += 6
	if binary.LittleEndian.Uint16(notice[offset:offset+2]) != 2 || string(notice[offset+2:offset+4]) != "S\x00" {
		t.Fatalf("notice summary = %x", notice)
	}

	room := int16(7)
	match, err := PrivateLadderSearchPayload(&room, []PrivateLadderPeer{{
		GlobalIP: netip.MustParseAddr("203.0.113.4"), GlobalPort: 40710,
		LocalIP: netip.MustParseAddr("10.0.0.1"), LocalPort: 5000,
	}}, true)
	wantMatch := []byte{
		0, 1, 0, 7, 0, 1,
		203, 0, 113, 4, 6, 159,
		10, 0, 0, 1, 136, 19,
		1,
	}
	if err != nil || !bytes.Equal(match, wantMatch) {
		t.Fatalf("private match = %x, %v; want %x", match, err, wantMatch)
	}

	receive, err := PrivateChallengeReceivePayload(0, 3, "u", "A", 7, 99, 4, 2)
	wantReceive := []byte{
		0, 0, 0, 3, 0, 1, 'u', 2, 'A', 0, 7,
		99, 0, 0, 0, 4, 0, 0, 0, 2, 0, 0, 0,
	}
	if err != nil || !bytes.Equal(receive, wantReceive) {
		t.Fatalf("challenge receive = %x, %v; want %x", receive, err, wantReceive)
	}
	// Packet: token(u16)=3 + name_len(u8)=2 + name_UTF16("A")=2bytes + decision(u8)=9 + rule(u16)=0x0100
	response, err := ParsePrivateChallengeResponse([]byte{3, 0, 2, 'A', 0, 9, 0, 1})
	if err != nil || response.Token != 3 || response.Challenger != "A" || response.Decision != 9 || response.Rule != 256 {
		t.Fatalf("challenge response = %+v, %v", response, err)
	}
	if got := PrivateChallengeResultPayload(0, 1, 9); !bytes.Equal(got, []byte{0, 0, 0, 1, 9, 0}) {
		t.Fatalf("challenge result = %x", got)
	}
}

func TestPrivateLadderControlPayloadsHaveExpectedLength(t *testing.T) {
	tests := []struct {
		name string
		got  []byte
		want []byte
	}{
		{name: "search end", got: PrivateLadderSearchEndPayload(3), want: []byte{0, 3}},
		{name: "notify active", got: PrivateLadderNotifyPayload(true), want: []byte{0, 1}},
		{name: "notify inactive", got: PrivateLadderNotifyPayload(false), want: []byte{0, 0}},
		{name: "level", got: PrivateLadderLevelPayload(), want: []byte{0}},
		{name: "login unrestricted", got: LoginRestrictPayload(0), want: []byte{0, 0}},
		{name: "login restricted", got: LoginRestrictPayload(2), want: []byte{0, 2}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !bytes.Equal(test.got, test.want) {
				t.Fatalf("payload = %x, want %x", test.got, test.want)
			}
		})
	}
}
