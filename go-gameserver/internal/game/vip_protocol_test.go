package game

import (
	"bytes"
	"encoding/binary"
	"testing"

	"wtserver-go/internal/store"
)

func vipEntryBytes(item uint16, periodType byte, stock, sold, value uint16) []byte {
	out := binary.LittleEndian.AppendUint16(nil, item)
	out = append(out, periodType)
	out = binary.LittleEndian.AppendUint16(out, stock)
	out = binary.LittleEndian.AppendUint16(out, sold)
	return binary.LittleEndian.AppendUint16(out, value)
}

// Layouts follow the 2012 CShell parsers listed in docs/protocol/viplounge_2012.md.
func TestVIPLoungeLayouts2012(t *testing.T) {
	// sub_3424CC20: lounge, first-buyer flag, name, u32, u8 n, n x {name, u32, u32}.
	rank := VIPRankListPayload(1, []store.VIPRankEntry{{Name: "AB", Best: 7, Mine: 3}})
	want := []byte{0, 1, 0, 0, 0, 0, 0, 0, 1, 4, 'A', 0, 'B', 0, 7, 0, 0, 0, 3, 0, 0, 0}
	if !bytes.Equal(rank, want) {
		t.Fatalf("rank list = %x, want %x", rank, want)
	}

	// sub_3424D030: lounge, u8 n, n x {u8 period, u16 count}.
	tickets := VIPTicketCountPayload(2, []byte{0, 3}, map[byte]uint16{3: 2})
	if !bytes.Equal(tickets, []byte{0, 2, 2, 0, 0, 0, 3, 2, 0}) {
		t.Fatalf("ticket count = %x", tickets)
	}

	// sub_3424D700: always five tier rows; kind 1 rows carry set groups.
	var page store.VIPPage
	page.MyAmount = 500
	page.Tiers[0] = store.VIPTier{Threshold: 3000, Tickets: 1}
	page.Tiers[4] = store.VIPTier{Threshold: 100000, Kind: 1}
	page.Items = []store.VIPPageItem{
		{Tier: 0, ItemID: 12078, PeriodValue: 30},
		{Tier: 4, Group: 0, ItemID: 14001, PeriodType: 2},
		{Tier: 4, Group: 1, ItemID: 14002, PeriodType: 1, PeriodValue: 5, StockLimit: 10, Sold: 4},
	}
	list := VIPShopItemsPayload(1, 2, page)
	want = []byte{0, 1, 2, 0xf4, 1, 0, 0, 5}
	want = append(want, 0xb8, 0x0b, 0, 0, 1, 0, 0, 1)
	want = append(want, vipEntryBytes(12078, 0, 0, 0, 30)...)
	for tier := 1; tier < 4; tier++ {
		want = append(want, 0, 0, 0, 0, 0, 0, 0, 0)
	}
	want = append(want, 0xa0, 0x86, 1, 0, 0, 0, 1, 2, 1)
	want = append(want, vipEntryBytes(14001, 2, 0, 0, 0)...)
	want = append(want, 1)
	want = append(want, vipEntryBytes(14002, 1, 10, 4, 5)...)
	if !bytes.Equal(list, want) {
		t.Fatalf("item list = %x, want %x", list, want)
	}
	empty := VIPShopItemsPayload(0, 0, store.VIPPage{})
	if len(empty) != 8+5*8 || empty[7] != 5 {
		t.Fatalf("empty item list = %x", empty)
	}

	// sub_342B2130 request: lounge, period, tier, u16 tickets, kind, u8 n, n x {u16, u8, u16}.
	raw := []byte{1, 2, 4, 3, 0, 1, 2}
	raw = append(raw, 0xb1, 0x36, 2, 0, 0, 0xb2, 0x36, 1, 5, 0, 0xee, 0xee) // trailing AES padding
	request, err := ParseVIPBuyRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if request.Lounge != 1 || request.Period != 2 || request.Tier != 4 || request.Tickets != 3 || request.Kind != 1 ||
		len(request.Items) != 2 || request.Items[0].ItemID != 14001 || request.Items[0].PeriodType != 2 ||
		request.Items[1].ItemID != 14002 || request.Items[1].PeriodValue != 5 {
		t.Fatalf("buy request = %+v", request)
	}
	if _, err := ParseVIPBuyRequest([]byte{1, 2, 0, 1, 0, 0, 2, 1, 1, 0, 1, 0, 2, 2, 0, 1, 0}); err == nil {
		t.Fatal("kind 0 with two items must be rejected")
	}
	if _, err := ParseVIPBuyRequest([]byte{1, 2, 5, 1, 0, 0, 1, 1, 1, 0, 1, 0}); err == nil {
		t.Fatal("tier 5 must be rejected")
	}

	// sub_3424B7C0: lounge, period, tier, u16 tickets, kind, u8 n, n x ENTRY.
	reply := VIPBuyReplyPayload(request, 2, page.Items[1:2])
	want = append([]byte{0, 1, 2, 4, 2, 0, 1, 1}, vipEntryBytes(14001, 2, 0, 0, 0)...)
	if !bytes.Equal(reply, want) {
		t.Fatalf("buy reply = %x, want %x", reply, want)
	}
	if !bytes.Equal(VIPBuyFailedPayload(), []byte{1}) || !bytes.Equal(VIPTicketNotifyPayload(2), []byte{0, 2}) {
		t.Fatal("failure / notify payloads")
	}
}
