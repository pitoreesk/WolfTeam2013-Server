package store

import (
	"context"
	"fmt"
)

// PremiumShopItem is one purchasable premium duration of a shopdata item.
type PremiumShopItem struct {
	ItemID         int
	Days           int
	Price          uint32
	WolfCoinReward uint32
}

// PremiumShop is the per-user view of the 2013 TR Premium Shop.
type PremiumShop struct {
	Enabled bool
	Target  uint32 // Cash spend needed this month
	Used    uint32 // Cash spent this month (VIP Lounge Cash, monthly period)
	Items   []PremiumShopItem
}

// Reached reports whether the player may buy premium items.
func (p PremiumShop) Reached() bool { return p.Enabled && p.Used >= p.Target }

// Find returns the premium entry for an item and duration.
func (p PremiumShop) Find(itemID, days int) (PremiumShopItem, bool) {
	for _, item := range p.Items {
		if item.ItemID == itemID && item.Days == days {
			return item, true
		}
	}
	return PremiumShopItem{}, false
}

// PremiumShopStore is the persistent Premium Shop configuration.
type PremiumShopStore interface {
	PremiumShop(ctx context.Context, userID int) (PremiumShop, error)
}

func (s *PostgresStore) PremiumShop(ctx context.Context, userID int) (PremiumShop, error) {
	var shop PremiumShop
	var target int64
	err := s.pool.QueryRow(ctx, `SELECT enabled, target_cash FROM premium_shop_settings WHERE id = 1`).Scan(&shop.Enabled, &target)
	if err != nil {
		return PremiumShop{}, fmt.Errorf("load premium shop settings: %w", err)
	}
	shop.Target = uint32(target)
	// Monthly Cash spend is already tracked for the Cash VIP Lounge (lounge 0,
	// period 3) by RecordVIPSpend.
	var used int64
	err = s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount), 0) FROM vip_user_spend
		WHERE user_id = $1 AND lounge = 0 AND period = 3 AND period_key = $2`,
		userID, vipPeriodKey(3, s.now())).Scan(&used)
	if err != nil {
		return PremiumShop{}, fmt.Errorf("load premium shop spend: %w", err)
	}
	shop.Used = uint32(min(used, int64(^uint32(0))))
	rows, err := s.pool.Query(ctx, `
		SELECT item_id, days, price, wc_reward FROM premium_shop_items
		WHERE enabled ORDER BY sort_order, item_id, days`)
	if err != nil {
		return PremiumShop{}, fmt.Errorf("load premium shop items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item PremiumShopItem
		var price, reward int64
		if err := rows.Scan(&item.ItemID, &item.Days, &price, &reward); err != nil {
			return PremiumShop{}, fmt.Errorf("scan premium shop item: %w", err)
		}
		item.Price, item.WolfCoinReward = uint32(price), uint32(reward)
		shop.Items = append(shop.Items, item)
	}
	return shop, rows.Err()
}
