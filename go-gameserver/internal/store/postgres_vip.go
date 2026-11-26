package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// 2012 VIP Lounge (docs/protocol/viplounge_2012.md): lounges 0 Cash, 1 Gold,
// 2 WolfCoin; periods 0 FirstBuyer .. 7 VVip; five tiers per period.
const (
	VIPLoungeCount = 3
	VIPPeriodCount = 8
	VIPTierCount   = 5
)

// ErrVIPRedeemRejected marks a Take the catalog or ticket state refuses.
var ErrVIPRedeemRejected = errors.New("VIP Lounge redeem rejected")

type VIPLounge struct {
	Lounge     byte
	Visible    bool
	Start, End string // YYYYMMDD
	Periods    []byte // enabled periods
}

type VIPTier struct {
	Threshold uint32
	Kind      byte // 0 piece items, 1 sets A/B/C
	Tickets   uint16
}

type VIPPageItem struct {
	Tier        byte
	Group       byte // set index for kind 1
	ItemID      int
	PeriodType  byte // 0 days, 1 uses, 2 permanent
	PeriodValue int
	StockLimit  uint16
	Sold        uint16
}

type VIPPage struct {
	MyAmount uint32
	Tiers    [VIPTierCount]VIPTier
	Items    []VIPPageItem
}

type VIPRankEntry struct {
	Name       string
	Best, Mine uint32
}

// VIPLoungeStore is the persistent 2012 VIP Lounge state.
type VIPLoungeStore interface {
	VIPLounges(context.Context) ([]VIPLounge, error)
	VIPPage(ctx context.Context, userID int, lounge, period byte) (VIPPage, error)
	VIPTicketCounts(ctx context.Context, userID int, lounge byte) (map[byte]uint16, error)
	RedeemVIPTicket(ctx context.Context, userID int, lounge, period, tier byte, items []VIPPageItem) (uint16, []VIPPageItem, error)
	VIPRankList(ctx context.Context, userID int, lounge byte) ([]VIPRankEntry, error)
	RecordVIPSpend(ctx context.Context, userID, currency, amount int) ([]byte, error)
}

// VIPLoungeForCurrency maps a purchase currency code (1 Cash, 4 Gold, 2 WC)
// to its lounge.
func VIPLoungeForCurrency(currency int) (byte, bool) {
	switch currency {
	case 1:
		return 0, true
	case 4:
		return 1, true
	case 2:
		return 2, true
	}
	return 0, false
}

// vipPeriodKey is the start of the period window that contains now.
func vipPeriodKey(period byte, now time.Time) time.Time {
	now = now.UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	switch period {
	case 1: // Daily
		return day
	case 2: // Weekly (ISO, Monday)
		offset := (int(day.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -offset)
	case 3: // Monthly
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	case 4: // Quarter
		return time.Date(now.Year(), time.Month((int(now.Month())-1)/3*3+1), 1, 0, 0, 0, 0, time.UTC)
	case 5: // HalfYear
		return time.Date(now.Year(), time.Month((int(now.Month())-1)/6*6+1), 1, 0, 0, 0, 0, time.UTC)
	case 6: // Year
		return time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	default: // VVip (lifetime) and FirstBuyer
		return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	}
}

func (s *PostgresStore) VIPLounges(ctx context.Context) ([]VIPLounge, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT l.lounge, l.visible, l.start_date, l.end_date,
		       COALESCE(array_agg(p.period ORDER BY p.period) FILTER (WHERE p.enabled), '{}')
		FROM vip_lounges l LEFT JOIN vip_lounge_periods p ON p.lounge = l.lounge
		GROUP BY l.lounge ORDER BY l.lounge`)
	if err != nil {
		return nil, fmt.Errorf("load VIP lounges: %w", err)
	}
	defer rows.Close()
	var lounges []VIPLounge
	for rows.Next() {
		var lounge VIPLounge
		var id int16
		var periods []int16
		if err := rows.Scan(&id, &lounge.Visible, &lounge.Start, &lounge.End, &periods); err != nil {
			return nil, fmt.Errorf("scan VIP lounge: %w", err)
		}
		lounge.Lounge = byte(id)
		for _, period := range periods {
			lounge.Periods = append(lounge.Periods, byte(period))
		}
		lounges = append(lounges, lounge)
	}
	return lounges, rows.Err()
}

func (s *PostgresStore) VIPPage(ctx context.Context, userID int, lounge, period byte) (VIPPage, error) {
	var page VIPPage
	key := vipPeriodKey(period, s.now())
	var amount int64
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE((SELECT amount FROM vip_user_spend
		WHERE user_id = $1 AND lounge = $2 AND period = $3 AND period_key = $4), 0)`,
		userID, lounge, period, key).Scan(&amount); err != nil {
		return page, fmt.Errorf("load VIP spend: %w", err)
	}
	page.MyAmount = uint32(min(amount, int64(^uint32(0))))
	rows, err := s.pool.Query(ctx, `
		SELECT t.tier, t.threshold, t.row_kind,
		       COALESCE(u.granted - u.used, 0)
		FROM vip_lounge_tiers t
		LEFT JOIN vip_user_tickets u ON u.user_id = $1 AND u.lounge = t.lounge AND u.period = t.period
		     AND u.tier = t.tier AND u.period_key = $4
		WHERE t.lounge = $2 AND t.period = $3`, userID, lounge, period, key)
	if err != nil {
		return page, fmt.Errorf("load VIP tiers: %w", err)
	}
	for rows.Next() {
		var tier int16
		var threshold int64
		var kind int16
		var tickets int
		if err := rows.Scan(&tier, &threshold, &kind, &tickets); err != nil {
			rows.Close()
			return page, fmt.Errorf("scan VIP tier: %w", err)
		}
		if tier >= 0 && tier < VIPTierCount {
			page.Tiers[tier] = VIPTier{Threshold: uint32(threshold), Kind: byte(kind), Tickets: uint16(max(0, min(tickets, 0xffff)))}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return page, err
	}
	items, err := s.pool.Query(ctx, `
		SELECT i.slot, i.group_id, i.item_id, i.period_type, i.days, i.stock_limit, COALESCE(sa.sold, 0)
		FROM vip_shop_items i
		LEFT JOIN vip_item_sales sa ON sa.user_id = $1 AND sa.lounge = i.section AND sa.period = i.page
		     AND sa.tier = i.slot AND sa.group_id = i.group_id AND sa.item_id = i.item_id AND sa.period_key = $4
		WHERE i.section = $2 AND i.page = $3 AND i.enabled
		ORDER BY i.slot, i.group_id, i.item_id`, userID, lounge, period, key)
	if err != nil {
		return page, fmt.Errorf("load VIP items: %w", err)
	}
	defer items.Close()
	for items.Next() {
		var slot, periodType int16
		var group, itemID, days, stock, sold int
		if err := items.Scan(&slot, &group, &itemID, &periodType, &days, &stock, &sold); err != nil {
			return page, fmt.Errorf("scan VIP item: %w", err)
		}
		page.Items = append(page.Items, VIPPageItem{
			Tier: byte(slot), Group: byte(group), ItemID: itemID, PeriodType: byte(periodType),
			PeriodValue: days, StockLimit: uint16(stock), Sold: uint16(min(sold, 0xffff)),
		})
	}
	return page, items.Err()
}

func (s *PostgresStore) VIPTicketCounts(ctx context.Context, userID int, lounge byte) (map[byte]uint16, error) {
	counts := make(map[byte]uint16)
	now := s.now()
	for period := byte(0); period < VIPPeriodCount; period++ {
		var total int
		if err := s.pool.QueryRow(ctx, `
			SELECT COALESCE(SUM(granted - used), 0) FROM vip_user_tickets
			WHERE user_id = $1 AND lounge = $2 AND period = $3 AND period_key = $4`,
			userID, lounge, period, vipPeriodKey(period, now)).Scan(&total); err != nil {
			return nil, fmt.Errorf("load VIP ticket count: %w", err)
		}
		counts[period] = uint16(max(0, min(total, 0xffff)))
	}
	return counts, nil
}

// RedeemVIPTicket consumes one ticket of the tier and grants the requested
// piece item (kind 0) or the whole set (kind 1) in one transaction. It returns
// the tier's remaining tickets and the redeemed catalog rows with updated
// sold counts.
func (s *PostgresStore) RedeemVIPTicket(ctx context.Context, userID int, lounge, period, tier byte, requested []VIPPageItem) (uint16, []VIPPageItem, error) {
	if userID < 1 || lounge >= VIPLoungeCount || period >= VIPPeriodCount || tier >= VIPTierCount || len(requested) == 0 {
		return 0, nil, ErrVIPRedeemRejected
	}
	var remaining uint16
	var redeemed []VIPPageItem
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		remaining, redeemed = 0, nil
		now := s.now()
		key := vipPeriodKey(period, now)
		var kind int16
		if err := tx.QueryRow(ctx, `
			SELECT t.row_kind FROM vip_lounge_tiers t
			JOIN vip_lounge_periods p ON p.lounge = t.lounge AND p.period = t.period AND p.enabled
			WHERE t.lounge = $1 AND t.period = $2 AND t.tier = $3`, lounge, period, tier).Scan(&kind); err != nil {
			if noRows(err) {
				return ErrVIPRedeemRejected
			}
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT group_id, item_id, period_type, days, stock_limit FROM vip_shop_items
			WHERE section = $1 AND page = $2 AND slot = $3 AND mode = $4 AND enabled`,
			lounge, period, tier, kind)
		if err != nil {
			return err
		}
		catalog := map[int][]VIPPageItem{}
		for rows.Next() {
			var item VIPPageItem
			var group, days, stock int
			var periodType int16
			if err := rows.Scan(&group, &item.ItemID, &periodType, &days, &stock); err != nil {
				rows.Close()
				return err
			}
			item.Tier, item.Group, item.PeriodType, item.PeriodValue, item.StockLimit = tier, byte(group), byte(periodType), days, uint16(stock)
			catalog[group] = append(catalog[group], item)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		matches := func(want VIPPageItem, have VIPPageItem) bool {
			return want.ItemID == have.ItemID && want.PeriodType == have.PeriodType && want.PeriodValue == have.PeriodValue
		}
		if kind == 0 {
			if len(requested) != 1 {
				return ErrVIPRedeemRejected
			}
			for _, item := range catalog[0] {
				if matches(item, requested[0]) {
					redeemed = []VIPPageItem{item}
				}
			}
		} else {
			// The client sends the chosen set's items, not its index.
			for _, group := range catalog {
				if len(group) != len(requested) {
					continue
				}
				used := make([]bool, len(group))
				all := true
				for _, want := range requested {
					found := false
					for index, have := range group {
						if !used[index] && matches(have, want) {
							used[index], found = true, true
							break
						}
					}
					if !found {
						all = false
						break
					}
				}
				if all {
					redeemed = append([]VIPPageItem(nil), group...)
					break
				}
			}
		}
		if len(redeemed) == 0 {
			return ErrVIPRedeemRejected
		}
		var left int
		if err := tx.QueryRow(ctx, `
			UPDATE vip_user_tickets SET used = used + 1
			WHERE user_id = $1 AND lounge = $2 AND period = $3 AND tier = $4 AND period_key = $5 AND used < granted
			RETURNING granted - used`, userID, lounge, period, tier, key).Scan(&left); err != nil {
			if noRows(err) {
				return ErrVIPRedeemRejected
			}
			return err
		}
		remaining = uint16(max(0, min(left, 0xffff)))
		for index, item := range redeemed {
			var sold int
			if err := tx.QueryRow(ctx, `
				INSERT INTO vip_item_sales (user_id, lounge, period, tier, group_id, item_id, period_key, sold)
				VALUES ($1, $2, $3, $4, $5, $6, $7, 1)
				ON CONFLICT (user_id, lounge, period, tier, group_id, item_id, period_key)
				DO UPDATE SET sold = vip_item_sales.sold + 1
				RETURNING sold`, userID, lounge, period, tier, int(item.Group), item.ItemID, key).Scan(&sold); err != nil {
				return err
			}
			if item.StockLimit != 0 && sold > int(item.StockLimit) {
				return ErrVIPRedeemRejected
			}
			redeemed[index].Sold = uint16(min(sold, 0xffff))
			days := 0
			if item.PeriodType == 0 {
				days = item.PeriodValue
			}
			itemType := 0
			if item.ItemID >= 30000 {
				itemType = 1
			}
			if _, err := grantInventoryItem(ctx, tx, inventoryGrant{
				UserID: userID, ItemID: item.ItemID, ItemType: itemType, Days: days, AcquiredAt: now,
			}); err != nil {
				return fmt.Errorf("grant VIP item %d: %w", item.ItemID, err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	return remaining, redeemed, nil
}

// VIPRankList returns the best buyer and the player's own amount for the
// Daily..Year periods (bulletin rows 0..5) of the lounge.
func (s *PostgresStore) VIPRankList(ctx context.Context, userID int, lounge byte) ([]VIPRankEntry, error) {
	now := s.now()
	entries := make([]VIPRankEntry, 0, 6)
	for period := byte(1); period <= 6; period++ {
		key := vipPeriodKey(period, now)
		var entry VIPRankEntry
		var best int64
		err := s.pool.QueryRow(ctx, `
			SELECT u.callsign, s.amount FROM vip_user_spend s JOIN users u ON u.id = s.user_id
			WHERE s.lounge = $1 AND s.period = $2 AND s.period_key = $3
			ORDER BY s.amount DESC, s.user_id LIMIT 1`, lounge, period, key).Scan(&entry.Name, &best)
		if err != nil && !noRows(err) {
			return nil, fmt.Errorf("load VIP best buyer: %w", err)
		}
		entry.Best = uint32(min(best, int64(^uint32(0))))
		var mine int64
		if err := s.pool.QueryRow(ctx, `
			SELECT COALESCE((SELECT amount FROM vip_user_spend
			WHERE user_id = $1 AND lounge = $2 AND period = $3 AND period_key = $4), 0)`,
			userID, lounge, period, key).Scan(&mine); err != nil {
			return nil, fmt.Errorf("load VIP own amount: %w", err)
		}
		entry.Mine = uint32(min(mine, int64(^uint32(0))))
		entries = append(entries, entry)
	}
	return entries, nil
}

// RecordVIPSpend adds a completed purchase to every enabled period of the
// currency's lounge and grants one ticket for each tier threshold the new
// total crosses. It returns the lounge when at least one ticket was granted.
func (s *PostgresStore) RecordVIPSpend(ctx context.Context, userID, currency, amount int) ([]byte, error) {
	lounge, ok := VIPLoungeForCurrency(currency)
	if !ok || userID < 1 || amount <= 0 {
		return nil, nil
	}
	granted := false
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		granted = false
		now := s.now()
		today := now.UTC().Format("20060102")
		var visible bool
		var start, end string
		if err := tx.QueryRow(ctx, `SELECT visible, start_date, end_date FROM vip_lounges WHERE lounge = $1`, lounge).
			Scan(&visible, &start, &end); err != nil {
			if noRows(err) {
				return nil
			}
			return err
		}
		if !visible || today < start || today > end {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT period FROM vip_lounge_periods WHERE lounge = $1 AND enabled AND period > 0`, lounge)
		if err != nil {
			return err
		}
		var periods []byte
		for rows.Next() {
			var period int16
			if err := rows.Scan(&period); err != nil {
				rows.Close()
				return err
			}
			periods = append(periods, byte(period))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, period := range periods {
			key := vipPeriodKey(period, now)
			var total int64
			if err := tx.QueryRow(ctx, `
				INSERT INTO vip_user_spend (user_id, lounge, period, period_key, amount)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (user_id, lounge, period, period_key) DO UPDATE SET amount = vip_user_spend.amount + $5
				RETURNING amount`, userID, lounge, period, key, amount).Scan(&total); err != nil {
				return err
			}
			previous := total - int64(amount)
			tag, err := tx.Exec(ctx, `
				INSERT INTO vip_user_tickets (user_id, lounge, period, tier, period_key, granted)
				SELECT $1, lounge, period, tier, $4, 1 FROM vip_lounge_tiers
				WHERE lounge = $2 AND period = $3 AND threshold > 0 AND threshold <= $5 AND threshold > $6
				ON CONFLICT (user_id, lounge, period, tier, period_key)
				DO UPDATE SET granted = vip_user_tickets.granted + 1`, userID, lounge, period, key, total, previous)
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				granted = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("record VIP spend: %w", err)
	}
	if granted {
		return []byte{lounge}, nil
	}
	return nil, nil
}
