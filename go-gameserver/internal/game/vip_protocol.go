package game

import (
	"encoding/binary"
	"fmt"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

// 2012 VIP Lounge wire formats (docs/protocol/viplounge_2012.md). Every
// payload starts with the common result byte; a failed Take sends a nonzero
// result so the client keeps its ticket count.

// appendVIPName writes a u8 byte length + UTF-16 name capped at 12 units (the
// client copies names into 26-byte stack buffers).
func appendVIPName(writer *protocol.Writer, name string) {
	units := utf16.Encode([]rune(name))
	if len(units) > 12 {
		units = units[:12]
	}
	writer.U8(byte(2 * len(units)))
	for _, unit := range units {
		writer.U16(unit)
	}
}

// VIPRankListPayload encodes CS_IN_VIP_LOUNGE_RANKLIST_ACK (sub_3424CC20):
// lounge, u8 first-buyer flag, first-buyer name, u32 amount, u8 n (<= 6),
// n x {name, u32 best amount, u32 my amount} for Daily..Year.
func VIPRankListPayload(lounge byte, entries []store.VIPRankEntry) []byte {
	writer := protocol.NewWriter(96).U8(0).U8(lounge).U8(0)
	appendVIPName(writer, "")
	writer.U32(0)
	if len(entries) > 6 {
		entries = entries[:6]
	}
	writer.U8(byte(len(entries)))
	for _, entry := range entries {
		appendVIPName(writer, entry.Name)
		writer.U32(entry.Best).U32(entry.Mine)
	}
	return writer.Clone()
}

// VIPTicketCountPayload encodes CS_IN_VIP_LOUNGE_MYTICKET_COUNT_ACK
// (sub_3424D030): lounge, u8 n, n x {u8 period, u16 unused tickets}. The
// client only keeps periods that EVENTSHOPINFO enabled.
func VIPTicketCountPayload(lounge byte, periods []byte, counts map[byte]uint16) []byte {
	writer := protocol.NewWriter(3 + 3*len(periods)).U8(0).U8(lounge).U8(byte(len(periods)))
	for _, period := range periods {
		writer.U8(period).U16(counts[period])
	}
	return writer.Clone()
}

func appendVIPEntry(writer *protocol.Writer, item store.VIPPageItem) {
	writer.U16(uint16(item.ItemID)).U8(item.PeriodType).U16(item.StockLimit).U16(item.Sold).U16(uint16(item.PeriodValue))
}

// VIPShopItemsPayload encodes CS_IN_VIP_LOUNGE_ITEMLIST_ACK (sub_3424D700):
// lounge, period, u32 my spend, u8 5, then five tier rows {u32 threshold,
// u16 tickets, u8 kind, kind 0: u8 n x ENTRY, kind 1: u8 g (<= 3) x {u8 n x
// ENTRY}}; ENTRY = u16 item, u8 period type, u16 stock limit, u16 sold,
// u16 period value. The page only draws with exactly five rows.
func VIPShopItemsPayload(lounge, period byte, page store.VIPPage) []byte {
	writer := protocol.NewWriter(48 + 9*len(page.Items)).
		U8(0).U8(lounge).U8(period).U32(page.MyAmount).U8(store.VIPTierCount)
	for tier := byte(0); tier < store.VIPTierCount; tier++ {
		row := page.Tiers[tier]
		writer.U32(row.Threshold).U16(row.Tickets).U8(row.Kind)
		var groups [3][]store.VIPPageItem
		for _, item := range page.Items {
			if item.Tier != tier {
				continue
			}
			group := item.Group
			if row.Kind == 0 {
				group = 0
			}
			if group < 3 {
				groups[group] = append(groups[group], item)
			}
		}
		if row.Kind == 0 {
			items := groups[0]
			if len(items) > 255 {
				items = items[:255]
			}
			writer.U8(byte(len(items)))
			for _, item := range items {
				appendVIPEntry(writer, item)
			}
			continue
		}
		// Sets must be contiguous from A: count the leading non-empty groups.
		count := 0
		for count < 3 && len(groups[count]) > 0 {
			count++
		}
		writer.U8(byte(count))
		for index := 0; index < count; index++ {
			writer.U8(byte(len(groups[index])))
			for _, item := range groups[index] {
				appendVIPEntry(writer, item)
			}
		}
	}
	return writer.Clone()
}

// VIPBuyRequest is CS_IN_VIP_LOUNGE_BUYITEM_REQ (sub_342B2130): lounge,
// period, tier, u16 client ticket count (advisory), kind, u8 n, n x {u16 item,
// u8 period type, u16 period value}. The chosen set index is not sent.
type VIPBuyRequest struct {
	Lounge, Period, Tier byte
	Tickets              uint16
	Kind                 byte
	Items                []store.VIPPageItem
}

func ParseVIPBuyRequest(payload []byte) (VIPBuyRequest, error) {
	if len(payload) < 7 {
		return VIPBuyRequest{}, fmt.Errorf("VIP purchase payload is too short")
	}
	count := int(payload[6])
	if count == 0 || 7+count*5 > len(payload) {
		return VIPBuyRequest{}, fmt.Errorf("VIP purchase count %d exceeds payload", count)
	}
	request := VIPBuyRequest{
		Lounge: payload[0], Period: payload[1], Tier: payload[2],
		Tickets: binary.LittleEndian.Uint16(payload[3:5]), Kind: payload[5],
	}
	if request.Tier >= store.VIPTierCount || request.Kind > 1 || (request.Kind == 0 && count != 1) {
		return VIPBuyRequest{}, fmt.Errorf("VIP purchase tier/kind %d/%d is invalid", request.Tier, request.Kind)
	}
	for index := 0; index < count; index++ {
		offset := 7 + index*5
		request.Items = append(request.Items, store.VIPPageItem{
			Tier:        request.Tier,
			ItemID:      int(binary.LittleEndian.Uint16(payload[offset : offset+2])),
			PeriodType:  payload[offset+2],
			PeriodValue: int(binary.LittleEndian.Uint16(payload[offset+3 : offset+5])),
		})
	}
	return request, nil
}

// VIPBuyReplyPayload encodes CS_IN_VIP_LOUNGE_BUYITEM_ACK (sub_3424B7C0):
// lounge, period (skipped), tier, u16 remaining tier tickets, kind, u8 n,
// n x {u16 item, u8 (skipped), u16 stock limit, u16 sold, u16 (skipped)}.
func VIPBuyReplyPayload(request VIPBuyRequest, tickets uint16, items []store.VIPPageItem) []byte {
	writer := protocol.NewWriter(8 + 9*len(items)).U8(0).U8(request.Lounge).U8(request.Period).U8(request.Tier).
		U16(tickets).U8(request.Kind).U8(byte(len(items)))
	for _, item := range items {
		appendVIPEntry(writer, item)
	}
	return writer.Clone()
}

// VIPBuyFailedPayload is a result-only failure: the handler is not called.
func VIPBuyFailedPayload() []byte { return []byte{1} }

// VIPTicketNotifyPayload encodes the 0x1467 push (sub_341D15E0): u8 frame of
// VIPticket.dtx, sent with the lounge whose ticket was granted.
func VIPTicketNotifyPayload(lounge byte) []byte { return []byte{0, lounge} }
