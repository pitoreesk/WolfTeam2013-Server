package game

import (
	"encoding/binary"
	"fmt"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

type PowerUserSettings struct {
	Color uint32
	Label string
}

// ParsePowerUserSettings parses a 32-bit chat color followed by a byte-counted
// UTF-16 call-sign decoration.
func ParsePowerUserSettings(payload []byte) (PowerUserSettings, error) {
	if len(payload) < 5 {
		return PowerUserSettings{}, fmt.Errorf("power-user settings payload is too short")
	}
	labelBytes := int(payload[4])
	if labelBytes%2 != 0 || 5+labelBytes > len(payload) {
		return PowerUserSettings{}, fmt.Errorf("invalid power-user label length %d", labelBytes)
	}
	return PowerUserSettings{
		Color: binary.LittleEndian.Uint32(payload[:4]),
		Label: decodeUTF16Low(payload[5 : 5+labelBytes]),
	}, nil
}

// PowerUserFeatureMask is the full Power User feature mask (account +0x248,
// CS_CH_GETPOWERUSERINFO_ACK sub_3408B6F0). The client's own "is Power User"
// test is mask == 0x1F (sub_34107F20; it gates the 20% renewal price of
// 14032/14227). CShell reads only bits 0x02 chat colour (sub_34107ED0), 0x08
// death shield (sub_34107F30) and 0x10 call-sign decoration (sub_34107F40);
// the raw mask leaves it through WolfInterfaceManager vtable[61]
// (sub_34133C70) for the game module, which owns in-match perks such as the
// automatic backpack search. The old partial mask 0x12 left those off.
const PowerUserFeatureMask uint32 = 0x1f

// PowerUserInfoPayload carries three 32-bit fields (enabled, death allowance,
// chat colour), a byte-counted UTF-16 label, and the feature mask.
func PowerUserInfoPayload(enabled bool, color uint32, label string) ([]byte, error) {
	units := utf16.Encode([]rune(label))
	if len(units)*2 > 255 {
		return nil, fmt.Errorf("power-user label exceeds one-byte UTF-16 limit")
	}
	// The common ACK layer consumes the leading success byte.
	writer := protocol.NewWriter(18 + len(units)*2).U8(0)
	if enabled {
		writer.U32(1).U32(1).U32(color).U8(byte(len(units) * 2)).UTF16LE(label).U32(PowerUserFeatureMask)
	} else {
		writer.U32(0).U32(0).U32(color).U8(byte(len(units) * 2)).UTF16LE(label).U32(0)
	}
	return writer.Clone(), nil
}

func PowerUserStatusPayload(status byte) []byte { return []byte{status} }

type PowerRoomItem struct {
	UniqueID uint32
	ItemID   uint16
}

// PowerMasterItemsPayload removes matching items when mode is zero and inserts
// records when it is one.
func PowerMasterItemsPayload(mode byte, items []PowerRoomItem) ([]byte, error) {
	if mode > 1 {
		return nil, fmt.Errorf("invalid Power Master item mode %d", mode)
	}
	if len(items) > 255 {
		return nil, fmt.Errorf("Power Master item count exceeds one-byte limit")
	}
	writer := protocol.NewWriter(3 + len(items)*8).U8(0).U8(mode).U8(byte(len(items)))
	for _, item := range items {
		if item.ItemID == 0 || item.UniqueID == 0 {
			return nil, fmt.Errorf("invalid Power Master item record")
		}
		// Temporary records use the non-expiring 0xffff use count.
		writer.U32(item.UniqueID).U16(item.ItemID).U16(0xffff)
	}
	return writer.Clone(), nil
}

// MacroMessage is one GETMACROMESSAGE_ACK entry (sub_3408F120): u8 slot
// (0..4), u8 flag (stored at macro +28 and echoed by SETMACROMESSAGE), u8 n +
// UTF-16 text.
type MacroMessage struct {
	Slot byte
	Flag byte
	Text string
}

// MacroMessagePayload carries the configured macro-message slots.
func MacroMessagePayload(messages []MacroMessage) ([]byte, error) {
	if len(messages) > 255 {
		return nil, fmt.Errorf("macro message count exceeds one-byte limit")
	}
	// The common ACK layer consumes the leading success byte.
	writer := protocol.NewWriter(2 + len(messages)*4).U8(0).U8(byte(len(messages)))
	for _, message := range messages {
		if message.Slot >= 5 {
			return nil, fmt.Errorf("macro slot %d is outside protocol range", message.Slot)
		}
		units := utf16.Encode([]rune(message.Text))
		if len(units)*2 > 255 {
			return nil, fmt.Errorf("macro text exceeds one-byte UTF-16 limit")
		}
		writer.U8(message.Slot).U8(message.Flag).
			U8(byte(len(units) * 2)).UTF16LE(message.Text)
	}
	return writer.Clone(), nil
}

// SNSAccountPayload reports an intentionally disconnected SNS account. A
// provider value of two is followed by two strings; zero has no tail.
func SNSAccountPayload() []byte { return []byte{0, 0} }

// EventShopPayload controls which event, VIP Lounge, and Item Forge entries
// are available in the shop interface.
// EventShopPayload encodes CS_CH_EVENTSHOPINFO_ACK (2013 TR sub_34182BA0).
// List 1, one record per VIP lounge (0 Cash, 1 Gold, 2 WC): u8 lounge,
// u8 len + ASCII start "YYYYMMDD", u8 len + ASCII end, u8 visible (1 = shown
// in the shop's VipLoungeComboBox), u8 m (<= 8), m x {u8 period, u8 enabled};
// only enabled periods get a tab and a ticket counter.
func EventShopPayload(lounges []store.VIPLounge, premium bool) []byte {
	writer := protocol.NewWriter(96).U8(0).U8(3)
	for lounge := byte(0); lounge < 3; lounge++ {
		var config store.VIPLounge
		for _, candidate := range lounges {
			if candidate.Lounge == lounge {
				config = candidate
			}
		}
		start, end := config.Start, config.End
		if len(start) > 14 {
			start = start[:14]
		}
		if len(end) > 14 {
			end = end[:14]
		}
		periods := config.Periods
		if len(periods) > 8 {
			periods = periods[:8]
		}
		writer.U8(lounge).U8(byte(len(start))).ASCII(start).U8(byte(len(end))).ASCII(end).
			Bool(config.Visible && len(periods) > 0).U8(byte(len(periods)))
		for _, period := range periods {
			writer.U8(period).U8(1)
		}
	}

	// 2013 TR CS_CH_EVENTSHOPINFO_ACK (sub_34182BA0): the second list has
	// 3-byte {index, show, grade} entries (grade 61 as in the 2013 reference) (account +2828+2i / +2829+2i; 2012
	// used 2-byte pairs). show == 1 adds the Cash/Gold/WC Forge entry to the
	// lobby shop menu (sub_3415F620); grade only keys the restriction message
	// (sub_3415FA70 event -9). All three have a forge catalog (migration 039).
	writer.U8(3) // Item Forge categories: AP/Cash, Gold, and WC
	// Only the N (Cash) forge is shown: the forge window has no category
	// switcher (wolfitemforgemanager.txt), so each enabled category adds its
	// own lobby menu entry.
	for category := byte(0); category < 3; category++ {
		if Client2012 {
			writer.U8(category).Bool(category == 0)
		} else {
			writer.U8(category).Bool(category == 0).U8(61)
		}
	}
	if !Client2012 {
		// Final u8 -> account +2792: shows the Premium shop lobby menu entry
		// (a_lb_icon_Premiumshop.dtx, sub_3415F620).
		writer.Bool(premium)
	}
	return writer.Clone()
}

// LimitedItemsPayload carries an empty limited-item list.
func LimitedItemsPayload() []byte { return []byte{0, 0} }

// eventBonusListID is the event id carried by 0x1459. The client stores it
// in every row (+8) and echoes it in BUYITEM/BUY_BASKETITEM; nothing else
// reads it.
const eventBonusListID = 1

// EventRewardListPayload encodes 0x1459 CS_IN_EVENT_REWARD_LIST_ACK
// (sub_34177130): u32 event id, u8 count, then per row u16 item, u8 required
// unit, u16 required value, u16 bonus item, u8 bonus unit, u16 bonus amount.
// Units: 0 days, 1 count (sub_3407D0C0).
func EventRewardListPayload(bonuses []store.EventBonus) []byte {
	if len(bonuses) > 255 {
		bonuses = bonuses[:255]
	}
	eventID := uint32(0)
	if len(bonuses) != 0 {
		eventID = eventBonusListID
	}
	writer := protocol.NewWriter(6 + len(bonuses)*10).U8(0).U32(eventID).U8(byte(len(bonuses)))
	for _, bonus := range bonuses {
		writer.U16(uint16(bonus.ItemID)).U8(bonus.RequiredUnit).U16(uint16(bonus.RequiredValue)).
			U16(uint16(bonus.BonusItemID)).U8(bonus.BonusUnit).U16(uint16(bonus.BonusAmount))
	}
	return writer.Clone()
}

// WPackSubItem is one row of the W-Pack dialog; Value is shown as days
// ("%d gün") for day-priced items or as a count for count-priced ones.
type WPackSubItem struct {
	ItemID uint16
	Value  uint16
}

// WPackSubItemsPayload encodes 2012 CS_IN_W_PACK_SUB_ITEM_ACK (sub_341C1F10):
// u8 count, count x {u16 item, u16 value}, in pack order.
func WPackSubItemsPayload(items []WPackSubItem) ([]byte, error) {
	if len(items) > 255 {
		return nil, fmt.Errorf("W-pack sub-item count exceeds one-byte limit")
	}
	writer := protocol.NewWriter(2 + 4*len(items)).U8(0).U8(byte(len(items)))
	for _, item := range items {
		writer.U16(item.ItemID).U16(item.Value)
	}
	return writer.Clone(), nil
}

// WPackListPayload encodes 2013 TR CS_IN_WPACK_LIST_ACK (0x146F,
// sub_34177400, channel-lobby dispatcher sub_3415FFE0): u8 n, n x u16 pack
// ID. The client stores the IDs at item manager +0xD8; a non-empty list adds
// the W-Pack entry (a_lb_icon_Wpack.dtx) to the lobby shop menu (sub_3415F620).
func WPackListPayload(packIDs []uint16) []byte {
	if len(packIDs) > 255 {
		packIDs = packIDs[:255]
	}
	writer := protocol.NewWriter(2 + 2*len(packIDs)).U8(0).U8(byte(len(packIDs)))
	for _, id := range packIDs {
		writer.U16(id)
	}
	return writer.Clone()
}

// PrideResultPayload carries six 32-bit summary fields, one byte field, and an
// empty result table.
func PrideResultPayload() []byte {
	// The common ACK layer consumes the leading success byte.
	return protocol.NewWriter(27).U8(0).U32(0).U32(0).U32(0).U8(0).U32(0).U32(0).U32(0).U8(0).Clone()
}

func InitFieldPayload() []byte { return []byte{0} }

func TunnelingPayload(enabled bool) []byte {
	return protocol.NewWriter(2).U8(0).Bool(enabled).Clone()
}

// ParseSetMacroRequest decodes CS_CH_SETMACROMESSAGE_REQ (0x132E).
func ParseSetMacroRequest(payload []byte) ([]store.Macro, error) {
	if len(payload) < 1 {
		return nil, fmt.Errorf("macro payload is too short")
	}
	count, pos := int(payload[0]), 1
	macros := make([]store.Macro, 0, count)
	for i := 0; i < count; i++ {
		if pos+3 > len(payload) {
			return nil, fmt.Errorf("macro %d exceeds payload", i)
		}
		slot, flag, length := payload[pos], payload[pos+1], int(payload[pos+2])
		pos += 3
		if length%2 != 0 || pos+length > len(payload) || slot > 4 {
			return nil, fmt.Errorf("macro %d is invalid", i)
		}
		text := truncateUTF16(decodeUTF16Low(payload[pos:pos+length]), 127)
		pos += length
		macros = append(macros, store.Macro{Slot: slot, Flag: flag, Text: text})
	}
	return macros, nil
}
