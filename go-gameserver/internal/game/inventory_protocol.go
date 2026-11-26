package game

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"sort"
	"time"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/shopdata"
	"wtserver-go/internal/store"
)

const (
	itemPriceVersionA = uint32(0)
	itemPriceVersionB = uint32(0)
)

func ItemListPayload(items []store.Item) ([]byte, error) {
	return itemListPayload(items, nil, nil)
}

// ItemListPayloadWithNewCharacters is the post-purchase 0x1431 refresh. The
// 2013 handler (sub_34186D30) clears only the item list (+616); each record
// goes through sub_3410BD90, which appends character IDs (sub_3405AD30:
// 30001..34000) to the character list (+1936) without clearing or
// deduplicating it. Only characters the client does not hold yet may be
// listed, so the lobby shows set/W-Pack character parts without a relog.
func ItemListPayloadWithNewCharacters(items []store.Item, characters []store.Item) ([]byte, error) {
	return itemListPayload(items, nil, characters)
}

// ItemListPayloadWithPowerRoomItems includes the room's temporary weapons in
// the authoritative inventory snapshot.
func ItemListPayloadWithPowerRoomItems(items []store.Item, temporary []PowerRoomItem) ([]byte, error) {
	return itemListPayload(items, temporary, nil)
}

func itemListPayload(items []store.Item, temporary []PowerRoomItem, characters []store.Item) ([]byte, error) {
	byID := make(map[int]store.Item)
	quantities := make(map[int]int)
	order := make([]int, 0, len(items))
	for _, item := range items {
		if item.Type == 1 {
			continue
		}
		item.Expiry = string(expiryStampFrom(item.Expiry))
		quantity := item.Quantity
		if quantity < 1 {
			quantity = 1
		}
		quantities[item.ID] += quantity
		previous, exists := byID[item.ID]
		if !exists {
			order = append(order, item.ID)
		}
		if !exists || item.Expiry > previous.Expiry {
			byID[item.ID] = item
		}
	}
	weapons := make([]store.Item, 0, len(order))
	for _, id := range order {
		weapons = append(weapons, byID[id])
	}
	if len(weapons)+len(temporary)+len(characters) > 255 {
		return nil, fmt.Errorf("weapon count exceeds one-byte protocol limit")
	}
	// The leading status and item count are followed by records and a refresh
	// flag.
	now := time.Now()
	writer := protocol.NewWriter(3 + (len(weapons)+len(characters))*33 + len(temporary)*19).
		U8(0).U8(byte(len(weapons) + len(temporary) + len(characters)))
	for _, item := range weapons {
		writer.U32(uint32(2_000_000_000 + item.ID)).U16(uint16(item.ID))
		for slot := range 4 {
			writer.U16(uint16(uint64(item.Upgrade) >> (slot * 16) & 0xffff))
		}
		units := expiryStamp(item.Expiry).weaponUnits(quantities[item.ID], now)
		inventoryWireLifetime{
			ExpiryText: item.Expiry,
			Count:      uint16(units),
			State:      byte(units >> 16),
			Flag:       byte(units >> 24),
		}.appendTo(writer)
	}
	for _, item := range temporary {
		if item.ItemID == 0 || item.UniqueID == 0 {
			return nil, fmt.Errorf("invalid Power Master item record")
		}
		writer.U32(item.UniqueID).U16(item.ItemID)
		for range 4 {
			writer.U16(0)
		}
		// Temporary items have no expiry text, use count 0xffff, and state 2.
		inventoryWireLifetime{Count: 0xffff, State: 2}.appendTo(writer)
	}
	for _, item := range characters {
		// Same record as CS_IN_CHARITEMLIST_ACK: no upgrade slots, character units.
		expiry := expiryStampFrom(item.Expiry)
		writer.U32(uint32(2_000_000_000 + item.ID)).U16(uint16(item.ID))
		for range 4 {
			writer.U16(0)
		}
		inventoryWireLifetime{ExpiryText: string(expiry), Count: expiry.characterUnits(now)}.appendTo(writer)
	}
	writer.U8(0)
	return writer.Clone(), nil
}

func CharacterItemListPayload(items []store.Item) ([]byte, error) {
	seen := make(map[int]struct{})
	characters := make([]store.Item, 0, len(items))
	for _, item := range items {
		if item.Type != 1 {
			continue
		}
		if _, exists := seen[item.ID]; exists {
			continue
		}
		seen[item.ID] = struct{}{}
		characters = append(characters, item)
	}
	if len(characters) > 255 {
		return nil, fmt.Errorf("character count exceeds one-byte protocol limit")
	}
	// The leading status is followed by little-endian reset and count values.
	now := time.Now()
	writer := protocol.NewWriter(9 + len(characters)*25).U8(0).U32(1).U32(uint32(len(characters)))
	for _, item := range characters {
		expiry := expiryStampFrom(item.Expiry)
		writer.U32(uint32(2_000_000_000 + item.ID)).U16(uint16(item.ID))
		inventoryWireLifetime{
			ExpiryText: string(expiry),
			Count:      expiry.characterUnits(now),
		}.appendTo(writer)
	}
	return writer.Clone(), nil
}

func CharacterInfoPayload(redCharacter, blueCharacter int, slots map[int]int, equipment map[int]store.CharacterEquipment) []byte {
	// The leading status is followed by every saved selector record.
	recordCount := 0
	for slot := 1; slot <= 10; slot++ {
		if slots[slot] > 0 {
			recordCount++
		}
	}
	writer := protocol.NewWriter(2 + recordCount*70).U8(0).U8(byte(recordCount))
	for slot := 1; slot <= 10; slot++ {
		characterID := slots[slot]
		if characterID <= 0 {
			continue
		}
		installPosition := byte(0)
		switch characterID {
		case redCharacter:
			installPosition = 1
		case blueCharacter:
			installPosition = 2
		}
		appendCharacterInfoRecord(writer, slot, characterID, installPosition, equipment[characterID])
	}
	return writer.Clone()
}

func appendCharacterInfoRecord(writer *protocol.Writer, slot, characterID int, installPosition byte, equipment store.CharacterEquipment) {
	writer.U8(byte(slot)).U8(installPosition).U32(uint32(2_000_000_000 + characterID))
	for _, uniqueID := range equipment {
		writer.U32(uint32(uniqueID))
	}
}

func CurrencyPayload(info store.AccountSnapshot) []byte {
	// The currency message carries the complete account progression tuple, not
	// only the three currencies.
	return protocol.NewWriter(26).U8(0).
		U32(uint32(info.Gold)).U32(uint32(info.WC)).U32(uint32(info.Cash)).
		U32(uint32(info.GP)).U32(uint32(store.LevelBaseThreshold(info.Level))).
		U32(uint32(store.NextLevelThreshold(info.Level))).U8(byte(info.Level)).
		Clone()
}

type BuyRequest struct {
	ItemID         int
	Days           int
	ItemType       byte
	PurchaseMode   int
	Currency       int // authoritative currency; populated by server-side catalogs or VIP records
	Price          uint32
	WolfCoinReward uint32
}

// GenericUseItemRequest contains an item identity followed by context text
// that is echoed in the reply.
type GenericUseItemRequest struct {
	UniqueID int
	ItemID   int
	Context  uint32
	Text     string
}

func ParseGenericUseItemRequest(payload []byte) (GenericUseItemRequest, error) {
	if len(payload) < 11 {
		return GenericUseItemRequest{}, fmt.Errorf("use-item payload is too short")
	}
	textBytes := int(payload[10])
	if textBytes%2 != 0 || 11+textBytes > len(payload) {
		return GenericUseItemRequest{}, fmt.Errorf("use-item text length %d exceeds payload", textBytes)
	}
	request := GenericUseItemRequest{
		UniqueID: int(int32(binary.LittleEndian.Uint32(payload[0:4]))),
		ItemID:   int(binary.LittleEndian.Uint16(payload[4:6])),
		Context:  binary.LittleEndian.Uint32(payload[6:10]),
		Text:     decodeUTF16Low(payload[11 : 11+textBytes]),
	}
	if request.UniqueID <= 0 || request.ItemID <= 0 {
		return GenericUseItemRequest{}, fmt.Errorf("use-item identifiers are invalid")
	}
	return request, nil
}

// GenericUseItemReplyPayload carries balances, rank progress, updated item
// state, echoed context text, and WC after the common status.
func GenericUseItemReplyPayload(status byte, request GenericUseItemRequest, remaining int, info store.AccountSnapshot) []byte {
	if status != 0 {
		return []byte{status}
	}
	if remaining < 0 {
		remaining = 0
	}
	if remaining > 0x7fff {
		remaining = 0x7fff
	}
	textUnits := utf16.Encode([]rune(request.Text))
	if len(textUnits) > 127 {
		textUnits = textUnits[:127]
	}
	// 2013 TR CS_IN_USEITEM_ACK (sub_34226820; pump parser sub_34207D80 skips
	// 21 bytes): Gold, Cash, GP, u32, next-level GP, level (the 2013 reference
	// layout; 2012 had only Gold and Cash).
	writer := protocol.NewWriter(39 + len(textUnits)*2).U8(0).
		U32(uint32(info.Gold)).U32(uint32(info.Cash))
	if !Client2012 {
		writer.U32(uint32(info.GP)).U32(uint32(store.LevelBaseThreshold(info.Level))).
			U32(uint32(store.NextLevelThreshold(info.Level))).U8(byte(info.Level))
	}
	writer.U32(uint32(request.UniqueID)).U16(uint16(remaining)).U16(uint16(request.ItemID)).U32(request.Context).
		U8(byte(len(textUnits) * 2))
	for _, unit := range textUnits {
		writer.U16(unit)
	}
	writer.U32(uint32(info.WC))
	return writer.Clone()
}

// DeletedItemsPayload carries up to eight {unique-id, item-id} records.
func DeletedItemsPayload(itemIDs []int) ([]byte, error) {
	if len(itemIDs) > 8 {
		return nil, fmt.Errorf("deleted item count exceeds eight-record limit")
	}
	writer := protocol.NewWriter(2 + len(itemIDs)*6).U8(0).U8(byte(len(itemIDs)))
	for _, itemID := range itemIDs {
		if itemID < 1 || itemID > 0xffff {
			return nil, fmt.Errorf("deleted item ID %d is outside protocol range", itemID)
		}
		writer.U32(uint32(2_000_000_000 + itemID)).U16(uint16(itemID))
	}
	return writer.Clone(), nil
}

func ParseBuyRequest(payload []byte) (BuyRequest, error) {
	if len(payload) < 14 {
		return BuyRequest{}, fmt.Errorf("buy-item payload is too short")
	}
	return BuyRequest{
		ItemID:         int(binary.LittleEndian.Uint16(payload[:2])),
		Days:           int(binary.LittleEndian.Uint16(payload[2:4])),
		ItemType:       payload[4],
		PurchaseMode:   int(payload[5]),
		Price:          binary.LittleEndian.Uint32(payload[6:10]),
		WolfCoinReward: binary.LittleEndian.Uint32(payload[10:14]),
	}, nil
}

// BuyRequestPayment is the compatibility fallback used when shopdata is not
// configured. PurchaseMode identifies the shop context rather than currency;
// only the catalog can distinguish every normal-shop Gold/AP edge case.
func BuyRequestPayment(request BuyRequest) (currency int, price uint32, err error) {
	if request.Price == 0 {
		return 0, 0, fmt.Errorf("buy-item packet has no price")
	}
	switch request.PurchaseMode {
	case 1:
		if request.WolfCoinReward > 0 {
			return 1, request.Price, nil
		}
		return 4, request.Price, nil
	case 4:
		return 2, request.Price, nil
	default:
		return 0, 0, fmt.Errorf("buy-item purchase mode %d requires shopdata", request.PurchaseMode)
	}
}

func BuyItemReplyPayload(request BuyRequest, result store.PurchaseReceipt, status byte, silent bool) []byte {
	// 2013 TR CS_IN_BUYITEM_ACK (sub_3418A1F0): a reserved u16, WC, Gold,
	// Cash, then one inventory record (2012 had no u16 and no WC).
	expiry := expiryStampFrom(result.Expiry)
	writer := protocol.NewWriter(41).U8(status)
	if !Client2012 {
		writer.U16(0).U32(uint32(result.WC))
	}
	writer.U32(uint32(result.Gold)).U32(uint32(result.Cash)).
		U32(uint32(2_000_000_000 + request.ItemID)).U16(uint16(request.ItemID))
	useCount := uint16(max(0, request.Days))
	useMode := byte(request.PurchaseMode)
	if request.ItemID > 30000 && request.ItemID < 31000 {
		useCount = expiry.characterUnits(time.Now())
		useMode = 0
	}
	notification := byte(0)
	if status != 0 || silent {
		notification = 0
	} else {
		notification = 2
	}
	inventoryWireLifetime{
		ExpiryText: string(expiry),
		Count:      useCount,
		Flag:       notification,
	}.appendTo(writer)
	writer.U8(useMode)
	return writer.Clone()
}

type BasketPurchaseRequest struct {
	Mode  byte
	Items []BuyRequest
}

func ParseBasketPurchaseRequest(payload []byte) (BasketPurchaseRequest, error) {
	if len(payload) < 2 {
		return BasketPurchaseRequest{}, fmt.Errorf("basket purchase payload is too short")
	}
	count := int(payload[1])
	if count == 0 || 2+count*23 > len(payload) {
		return BasketPurchaseRequest{}, fmt.Errorf("basket item count %d exceeds payload", count)
	}
	request := BasketPurchaseRequest{Mode: payload[0], Items: make([]BuyRequest, 0, count)}
	for index := 0; index < count; index++ {
		offset := 2 + index*23
		item, err := ParseBuyRequest(payload[offset : offset+23])
		if err != nil {
			return BasketPurchaseRequest{}, fmt.Errorf("basket item %d: %w", index, err)
		}
		request.Items = append(request.Items, item)
	}
	return request, nil
}

func BasketPurchaseReplyPayload(request BasketPurchaseRequest, result store.BasketPurchaseResult) []byte {
	// The common ACK layer consumes the leading success byte.
	writer := protocol.NewWriter(17 + len(request.Items)*32).U8(0).U8(byte(len(request.Items)))
	if !result.Success || len(result.Items) != len(request.Items) {
		for range request.Items {
			writer.U8(1)
		}
	} else {
		for index, item := range request.Items {
			purchase := result.Items[index]
			writer.U8(0).U8(0).U8(1).U16(uint16(item.ItemID)).
				U32(uint32(2_000_000_000 + item.ItemID)).U8(14).
				ASCII(string(expiryStampFrom(purchase.Expiry))).U16(uint16(max(0, item.Days))).U8(0)
		}
	}
	writer.U32(uint32(result.Gold)).U32(uint32(result.WC)).U32(uint32(result.Cash))
	return writer.Clone()
}

func ChangeInventoryPayload(request []byte) ([]byte, error) {
	if len(request) < 9 {
		return nil, fmt.Errorf("change-inventory payload is too short")
	}
	// An operation result, item ID, and unique ID follow the common result byte.
	itemID := binary.LittleEndian.Uint16(request[7:9])
	uniqueID := binary.LittleEndian.Uint32(request[3:7])
	return protocol.NewWriter(9).
		U8(0).
		U16(0).
		U16(itemID).
		U32(uniqueID).
		Clone(), nil
}

func BuyNowListPayload(items [2]uint16) []byte {
	// The common ACK layer consumes the leading success byte.
	return protocol.NewWriter(5).U8(0).U16(items[0]).U16(items[1]).Clone()
}

// CheckBuyNowOpenPayload encodes CS_CK_CHECK_BUYNOW_OPEN_ACK (0x1215,
// sub_341835F0): u8 open. The lobby (sub_34213880) and room (sub_34250C90)
// dispatchers open the purchase dialog (sub_3427DBA0) for the BuyNow item the
// player clicked (buttons 922/923) only when it is non-zero; zero hides the
// BuyNow panel (sub_3420EDF0(0)).
func CheckBuyNowOpenPayload(open bool) []byte {
	return protocol.NewWriter(2).U8(0).Bool(open).Clone()
}

// ItemPriceVersionPayload encodes CS_IN_ITEMPRICE_VERSION_ACK (0x1441,
// sub_341D23D0): two u32 cache versions. The client re-applies its cached
// IP_KSG.dat when both match and only requests 0x143E when they differ.
func ItemPriceVersionPayload(versionA, versionB uint32) []byte {
	return protocol.NewWriter(9).U8(0).U32(versionA).U32(versionB).Clone()
}

// itemPriceOverrideEntries encodes the 2012 CS_IN_ITEMPRICE_INFO_ACK entries
// (sub_341D25D0 / sub_341D7940): u16 item, u8 kind|0x80, u8 +29, u8 +25,
// i8 +61, u8 +89, u8 n, n x {u8 slot, u32 price, u32 WC reward, u16}. An
// entry with bit 7 marks the item on sale (shop entry +200 = 1).
func itemPriceOverrideEntries(overrides []shopdata.PriceOverride) []byte {
	writer := protocol.NewWriter(19 * len(overrides))
	for _, override := range overrides {
		writer.U16(uint16(override.ItemID)).U8(override.Kind).
			U8(override.Meta[0]).U8(override.Meta[1]).U8(override.Meta[2]).U8(override.Meta[3]).
			U8(1).U8(override.Slot).U32(override.Price).U32(override.WolfCoinReward).U16(0)
	}
	return writer.Clone()
}

// ItemPriceOverrideVersions derives the cache versions from the override set,
// so clients refetch whenever it changes; no overrides keep the 0/0 default.
func ItemPriceOverrideVersions(overrides []shopdata.PriceOverride) (uint32, uint32) {
	if len(overrides) == 0 {
		return itemPriceVersionA, itemPriceVersionB
	}
	return 2012, crc32.ChecksumIEEE(itemPriceOverrideEntries(overrides))
}

// ItemPriceInfoPayload encodes CS_IN_ITEMPRICE_INFO_ACK (0x143F).
func ItemPriceInfoPayload(overrides []shopdata.PriceOverride) []byte {
	versionA, versionB := ItemPriceOverrideVersions(overrides)
	if len(overrides) > 255 {
		overrides = overrides[:255]
	}
	entries := itemPriceOverrideEntries(overrides)
	return protocol.NewWriter(10 + len(entries)).
		U8(0).U32(versionA).U32(versionB).
		U8(byte(len(overrides))).Bytes(entries).
		Clone()
}

func EventGoldPayload() []byte {
	// Each event occupies 28 bytes after the count.
	return protocol.NewWriter(30).
		U8(0).
		U8(1).
		U32(0).
		Bytes(make([]byte, 8)).
		U32(10).
		Bytes(make([]byte, 12)).
		Clone()
}

// InventoryStatusPayload is used by ACK handlers that consume no
// message-specific fields. The encrypted transport supplies block padding.
func InventoryStatusPayload(status byte) []byte { return []byte{status} }

type ItemUpgradeRequest struct {
	UniqueID  uint32
	ItemID    uint16
	Slot      byte
	UpgradeID uint16
}

func ParseItemUpgradeRequest(payload []byte, downgrade bool) (ItemUpgradeRequest, error) {
	want := 7
	if downgrade {
		want = 5
	}
	if len(payload) < want {
		return ItemUpgradeRequest{}, fmt.Errorf("item upgrade payload is too short")
	}
	request := ItemUpgradeRequest{UniqueID: binary.LittleEndian.Uint32(payload[0:4]), Slot: payload[4]}
	if request.UniqueID > 2_000_000_000 && request.UniqueID-2_000_000_000 <= 0xffff {
		request.ItemID = uint16(request.UniqueID - 2_000_000_000)
	} else if request.UniqueID > 0 && request.UniqueID <= 0xffff {
		request.ItemID = uint16(request.UniqueID)
		request.UniqueID = uint32(2_000_000_000) + uint32(request.ItemID)
	}
	if !downgrade {
		// Upgrade requests encode the selected value as a u16.
		request.UpgradeID = binary.LittleEndian.Uint16(payload[5:7])
	}
	if request.Slot >= 4 || request.ItemID == 0 {
		return ItemUpgradeRequest{}, fmt.Errorf("invalid item upgrade identifiers: unique=%d item=%d slot=%d", request.UniqueID, request.ItemID, request.Slot)
	}
	if !downgrade && request.UpgradeID > 0xffff {
		return ItemUpgradeRequest{}, fmt.Errorf("upgrade ID %d exceeds persisted range", request.UpgradeID)
	}
	return request, nil
}

func ItemUpgradeReplyPayload(info store.AccountSnapshot, request ItemUpgradeRequest, success bool) []byte {
	// The common ACK layer consumes the leading success byte.
	writer := protocol.NewWriter(14).U8(0).U32(uint32(info.Gold)).U32(uint32(info.Cash))
	if success {
		writer.U32(request.UniqueID).U8(request.Slot)
	} else {
		writer.U32(0).U8(0)
	}
	return writer.Clone()
}

func ItemDowngradeReplyPayload(request ItemUpgradeRequest, success bool) []byte {
	// The common ACK layer consumes the leading success byte.
	writer := protocol.NewWriter(6).U8(0)
	if success {
		writer.U32(request.UniqueID).U8(request.Slot)
	} else {
		writer.U32(0).U8(0)
	}
	return writer.Clone()
}

type EquipTitleRequest struct {
	Slot  byte
	Title string
}

func ItemIDFromUniquePayload(payload []byte) (uint32, int, error) {
	if len(payload) < 4 {
		return 0, 0, fmt.Errorf("inventory unique-id payload is too short")
	}
	uniqueID := binary.LittleEndian.Uint32(payload[:4])
	if uniqueID <= 2_000_000_000 || uniqueID-2_000_000_000 > 65535 {
		return 0, 0, fmt.Errorf("inventory unique ID %d is invalid", uniqueID)
	}
	return uniqueID, int(uniqueID - 2_000_000_000), nil
}

func SellItemReplyPayload(result store.SellResult, uniqueID uint32) []byte {
	// The common ACK layer consumes the leading success byte.
	writer := protocol.NewWriter(13).U8(0).U32(uint32(result.Gold)).U32(uint32(result.Cash))
	if result.Success {
		writer.U32(uniqueID)
	} else {
		writer.U32(0)
	}
	return writer.Clone()
}

func RefundItemReplyPayload(refundedCash, refundedOther int) []byte {
	// The common ACK layer consumes the leading success byte.
	return protocol.NewWriter(9).U8(0).U32(uint32(max(0, refundedCash))).U32(uint32(max(0, refundedOther))).Clone()
}

func PermanentTicketInfoPayload(ticketUnique uint32, info store.PermanentTicketInfo) ([]byte, error) {
	weapons := make([]uint16, 0, len(info.Eligible))
	characters := make([]uint16, 0, len(info.Eligible))
	for _, item := range info.Eligible {
		if item.ID < 1 || item.ID > 65535 {
			continue
		}
		if item.Type == 1 {
			characters = append(characters, uint16(item.ID))
		} else {
			weapons = append(weapons, uint16(item.ID))
		}
	}
	if len(weapons) > 255 || len(characters) > 255 {
		return nil, fmt.Errorf("permanent ticket candidate count exceeds one-byte limit")
	}
	// The common ACK layer consumes the leading success byte.
	writer := protocol.NewWriter(8 + (len(weapons)+len(characters))*2).U8(0).U32(ticketUnique)
	kind := byte(0xff)
	if info.Valid && info.TargetType >= 0 && info.TargetType <= 1 {
		kind = byte(info.TargetType)
	}
	writer.U8(kind).U8(byte(len(weapons)))
	for _, itemID := range weapons {
		writer.U16(itemID)
	}
	writer.U8(byte(len(characters)))
	for _, itemID := range characters {
		writer.U16(itemID)
	}
	return writer.Clone(), nil
}

func PermanentTicketUsePayload(ticketUnique, targetUnique uint32, targetItemID uint16, success bool) []byte {
	// The common ACK layer consumes the leading success byte.
	if !success {
		return InventoryStatusPayload(1)
	}
	writer := protocol.NewWriter(18).U8(0)
	writer.U8(2).U32(ticketUnique).U32(targetUnique).U8(1).U32(targetUnique).U16(targetItemID).U8(0) // no character/title refresh records
	return writer.Clone()
}

func ParseEquipTitleRequest(payload []byte) (EquipTitleRequest, error) {
	if len(payload) < 2 {
		return EquipTitleRequest{}, fmt.Errorf("equip-title payload is too short")
	}
	length := int(payload[1])
	if payload[0] >= 4 || length%2 != 0 || 2+length > len(payload) {
		return EquipTitleRequest{}, fmt.Errorf("invalid equip-title payload")
	}
	return EquipTitleRequest{Slot: payload[0], Title: decodeUTF16Low(payload[2 : 2+length])}, nil
}

func LuckyBoxPreviewPayload(boxUnique, rewardItem, days int) []byte {
	// The three zero u16 fields are the secondary duration and optional jackpot
	// reward/duration. The following
	// byte selects the moon phase. The final uint32 uses a scaled probability,
	// so raw 100 represents an initial 5% fullmoon probability.
	return protocol.NewWriter(20).
		U8(0).
		U32(uint32(boxUnique)).
		U16(uint16(rewardItem)).
		U16(uint16(max(1, days))).
		U16(0).
		U16(0).
		U16(0).
		U8(0).
		U32(100).
		Clone()
}

func LuckyBoxTakePayload(boxUnique, remaining, rewardItem, days int) []byte {
	// Four values are followed by one reserved u16 and four reserved bytes.
	return protocol.NewWriter(17).
		U8(0).
		U32(uint32(boxUnique)).
		U16(uint16(max(0, remaining))).
		U16(uint16(rewardItem)).
		U16(uint16(max(1, days))).
		U16(0).
		U32(0).
		Clone()
}

func ApplyEquipment(ctx context.Context, users store.GameRepository, userID int, payload []byte) error {
	updates, err := equipmentUpdates(payload)
	if err != nil || len(updates) == 0 {
		return err
	}
	if batch, ok := users.(store.EquipmentBatchStore); ok {
		return batch.SetEquipmentBatch(ctx, userID, updates)
	}
	// Keep compatibility with stores that only implement the original
	// single-slot persistence boundary. Sort the keys so overlapping requests
	// acquire backend locks in a consistent order.
	slots := make([]int, 0, len(updates))
	for slot := range updates {
		slots = append(slots, slot)
	}
	sort.Ints(slots)
	var saveErrors []error
	for _, slot := range slots {
		if err := users.SetEquipment(ctx, fmt.Sprintf("%d", slot), updates[slot], userID); err != nil {
			saveErrors = append(saveErrors, fmt.Errorf("store equipment slot %d: %w", slot, err))
		}
	}
	return errors.Join(saveErrors...)
}

func equipmentUpdates(payload []byte) (map[int]int, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("equip payload is empty")
	}
	count := int(int8(payload[0]))
	if count < 0 {
		return nil, nil
	}
	updates := make(map[int]int, 34)
	for index := 0; index < count; index++ {
		base := index * 89
		if base+90 > len(payload) {
			break
		}
		set := int(payload[1+base])
		if set < 0 || set > 3 {
			continue
		}
		values := []struct {
			offset int
			slot   int
		}{
			{2, set*6 + 4},  // knife
			{7, set*6 + 1},  // purchased second main weapon
			{12, set * 6},   // primary
			{17, set*6 + 5}, // grenade 1
			{21, set*6 + 3}, // grenade 2
			{85, set*6 + 2}, // secondary
		}
		if set == 0 {
			for power := 0; power < 10; power++ {
				values = append(values, struct {
					offset int
					slot   int
				}{25 + power*4, 24 + power})
			}
		}
		for _, value := range values {
			uniqueID := int(int32(binary.LittleEndian.Uint32(payload[base+value.offset : base+value.offset+4])))
			updates[value.slot] = uniqueID
		}
	}
	return updates, nil
}

func filterItems(items []store.Item, itemType int) []store.Item {
	filtered := make([]store.Item, 0, len(items))
	for _, item := range items {
		if item.Type == itemType {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func canonicalCharacterSlots(saved map[int]int, owned []int) map[int]int {
	ownedSet := make(map[int]struct{}, len(owned))
	for _, characterID := range owned {
		ownedSet[characterID] = struct{}{}
	}
	result := make(map[int]int)
	used := make(map[int]struct{})
	for slot := 1; slot <= 10; slot++ {
		characterID := saved[slot]
		if _, ok := ownedSet[characterID]; !ok {
			continue
		}
		if _, duplicate := used[characterID]; duplicate {
			continue
		}
		result[slot] = characterID
		used[characterID] = struct{}{}
	}
	for _, characterID := range owned {
		if len(result) >= 10 {
			break
		}
		if _, duplicate := used[characterID]; duplicate {
			continue
		}
		for slot := 1; slot <= 10; slot++ {
			if _, occupied := result[slot]; !occupied {
				result[slot] = characterID
				used[characterID] = struct{}{}
				break
			}
		}
	}
	return result
}

func characterIDs(items []store.Item) []int {
	seen := make(map[int]struct{})
	result := make([]int, 0, len(items))
	for _, item := range items {
		if item.Type != 1 || !selectableCharacterID(item.ID) {
			continue
		}
		if _, exists := seen[item.ID]; exists {
			continue
		}
		seen[item.ID] = struct{}{}
		result = append(result, item.ID)
	}
	return result
}

// Selectable character identifiers occupy the inclusive range 30001..31000.
func selectableCharacterID(itemID int) bool {
	return itemID >= 30001 && itemID <= 31000
}

func sameCharacterSlots(first, second map[int]int) bool {
	if len(first) != len(second) {
		return false
	}
	keys := make([]int, 0, len(first))
	for key := range first {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	for _, key := range keys {
		if second[key] != first[key] {
			return false
		}
	}
	return true
}
