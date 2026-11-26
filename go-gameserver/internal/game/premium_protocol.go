package game

import (
	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

// premiumDurations are the shopdata duration slots; the premium ACK fills the
// price arrays by index (fill mode 0), exactly like shopdata.
var premiumDurations = [...]int{1, 7, 30, 90, 180, 0}

// PremiumShopPayload encodes 2013 TR CS_IN_PREMIUMSHOP_ITEMLIST_ACK (0x146D,
// sub_34178E50, channel-lobby dispatcher):
//
//	u8 result, u16 (skipped), u8 goal reached, u32 target Cash (shown as
//	TargetAmountCash and in the detail text), u32 my used Cash,
//	u8 n, n x {u16 item, u8 shopdata+29, u8 shopdata+25, u8 discount flag,
//	u8, u32 original, u32 discounted, u8 fill mode (+61), u8 currency kind
//	(0 Cash, 1 Gold, 2 WC), u8 m, m x {u16 value, u32 price, u32 WC reward}}
//
// Kind 0 with fill mode 0 gives pricing mode 2 and fills the Cash price of
// duration slot k from entry k; the client only lists items that exist in
// shopdata (sub_340673F0). meta returns the shopdata +29/+25 bytes.
func PremiumShopPayload(shop store.PremiumShop, meta func(itemID int) (byte, byte, bool)) []byte {
	type entry struct {
		itemID int
		prices [len(premiumDurations)]store.PremiumShopItem
	}
	var entries []*entry
	index := map[int]*entry{}
	for _, item := range shop.Items {
		slot := -1
		for k, days := range premiumDurations {
			if days == item.Days {
				slot = k
			}
		}
		if slot < 0 {
			continue
		}
		if _, _, ok := meta(item.ItemID); !ok {
			continue
		}
		e := index[item.ItemID]
		if e == nil {
			e = &entry{itemID: item.ItemID}
			index[item.ItemID] = e
			entries = append(entries, e)
		}
		e.prices[slot] = item
	}
	if len(entries) > 255 {
		entries = entries[:255]
	}
	writer := protocol.NewWriter(16 + len(entries)*80).U8(0).U16(0).
		Bool(shop.Reached()).U32(shop.Target).U32(shop.Used).U8(byte(len(entries)))
	for _, e := range entries {
		meta29, meta25, _ := meta(e.itemID)
		writer.U16(uint16(e.itemID)).U8(meta29).U8(meta25).U8(0).U8(0).U32(0).U32(0).
			U8(0).U8(0).U8(byte(len(premiumDurations)))
		for k := range premiumDurations {
			writer.U16(uint16(k + 1)).U32(e.prices[k].Price).U32(e.prices[k].WolfCoinReward)
		}
	}
	return writer.Clone()
}

// DailyUseMoneyPayload encodes CS_IN_DAILY_USE_MONEY_ACK (0x1473,
// sub_34229A60): u32 used Cash, then 8 bytes the client skips.
func DailyUseMoneyPayload(used uint32) []byte {
	return protocol.NewWriter(13).U8(0).U32(used).U32(0).U32(0).Clone()
}
