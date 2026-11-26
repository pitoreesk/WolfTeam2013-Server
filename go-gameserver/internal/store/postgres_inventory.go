package store

import (
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const permanentInventoryExpiry = "20991231235959"

const maxInventoryQuantity = 0xffff

type inventoryGrant struct {
	UserID     int
	ItemID     int
	ItemType   int
	Days       int
	Quantity   int
	Currency   int
	Price      uint32
	AcquiredAt time.Time
}

type couponCredit struct {
	Gold int
	WC   int
}

// couponCreditForItem maps coupon identities to their account credit.
func couponCreditForItem(itemID int) (couponCredit, bool) {
	switch itemID {
	case 15003:
		return couponCredit{Gold: 30_000}, true
	case 15005:
		return couponCredit{Gold: 100_000}, true
	case 15030:
		return couponCredit{WC: 100_000}, true
	case 15037:
		return couponCredit{WC: 5_000}, true
	default:
		return couponCredit{}, false
	}
}

func parseTimedInventoryExpiry(value string) (time.Time, bool) {
	cleaned := strings.NewReplacer(" ", "", "-", "", ":", "").Replace(value)
	if cleaned == permanentInventoryExpiry || strings.EqualFold(strings.TrimSpace(value), "permanent") {
		return time.Time{}, false
	}
	expiry, err := time.ParseInLocation("20060102150405", cleaned, time.UTC)
	return expiry, err == nil
}

func inventoryItemExpired(value string, now time.Time) bool {
	expiry, timed := parseTimedInventoryExpiry(value)
	return timed && !expiry.After(now.UTC())
}

func (s *PostgresStore) purgeExpiredInventory(ctx context.Context, userID int) error {
	if userID < 1 {
		return fmt.Errorf("invalid inventory owner")
	}
	now := s.now().UTC()
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, item_id, expiry FROM inventory
			WHERE user_id = $1 ORDER BY id FOR UPDATE`, userID)
		if err != nil {
			return fmt.Errorf("lock inventory expiry rows: %w", err)
		}
		type expiredRow struct {
			id     int64
			itemID int
		}
		var expired []expiredRow
		for rows.Next() {
			var row expiredRow
			var expiry string
			if err := rows.Scan(&row.id, &row.itemID, &expiry); err != nil {
				rows.Close()
				return fmt.Errorf("scan inventory expiry row: %w", err)
			}
			if inventoryItemExpired(expiry, now) {
				expired = append(expired, row)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate inventory expiry rows: %w", err)
		}
		rows.Close()

		removedItems := make(map[int]struct{}, len(expired))
		for _, row := range expired {
			if _, err := tx.Exec(ctx, `DELETE FROM inventory WHERE id = $1 AND user_id = $2`, row.id, userID); err != nil {
				return fmt.Errorf("delete expired inventory row: %w", err)
			}
			removedItems[row.itemID] = struct{}{}
		}
		for itemID := range removedItems {
			if itemID <= 0xffff {
				if _, err := tx.Exec(ctx, `
					INSERT INTO expired_inventory_notifications (user_id, item_id, expired_at)
					VALUES ($1, $2, $3)
					ON CONFLICT (user_id, item_id) DO NOTHING`, userID, itemID, now); err != nil {
					return fmt.Errorf("queue expired inventory notification: %w", err)
				}
			}
			if err := clearUnownedInventoryReferences(ctx, tx, userID, itemID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("purge expired inventory: %w", err)
	}
	return nil
}

func (s *PostgresStore) TakeExpiredItems(ctx context.Context, userID, limit int) ([]int, error) {
	if userID < 1 || limit < 1 || limit > 8 {
		return nil, fmt.Errorf("invalid expired inventory notification request")
	}
	if err := s.purgeExpiredInventory(ctx, userID); err != nil {
		return nil, err
	}
	return withSerializableResult(ctx, s, "take expired inventory notifications", func(tx pgx.Tx) ([]int, error) {
		rows, err := tx.Query(ctx, `
			SELECT item_id FROM expired_inventory_notifications
			WHERE user_id = $1
			ORDER BY expired_at, item_id
			LIMIT $2 FOR UPDATE`, userID, limit)
		if err != nil {
			return nil, fmt.Errorf("lock expired inventory notifications: %w", err)
		}
		var itemIDs []int
		for rows.Next() {
			var itemID int
			if err := rows.Scan(&itemID); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan expired inventory notification: %w", err)
			}
			itemIDs = append(itemIDs, itemID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate expired inventory notifications: %w", err)
		}
		rows.Close()
		for _, itemID := range itemIDs {
			if _, err := tx.Exec(ctx, `
				DELETE FROM expired_inventory_notifications
				WHERE user_id = $1 AND item_id = $2`, userID, itemID); err != nil {
				return nil, fmt.Errorf("delete expired inventory notification: %w", err)
			}
		}
		return itemIDs, nil
	})
}

func extendedInventoryExpiry(now time.Time, days int, existing ...string) string {
	base := now.UTC()
	for _, value := range existing {
		expiry, timed := parseTimedInventoryExpiry(value)
		if timed && expiry.After(base) {
			base = expiry
		}
	}
	return base.AddDate(0, 0, days).Format("20060102150405")
}

// grantInventoryItem keeps timed ownership as one cumulative duration and
// permanent ownership as a compact quantity stack.
func grantInventoryItem(ctx context.Context, tx pgx.Tx, grant inventoryGrant) (string, error) {
	if grant.ItemID == powerUserItemID {
		// "100 Ölüm'den Kurtulma": the next 100 deaths after a Power User
		// grant are not recorded (migration 045).
		if _, err := tx.Exec(ctx, `UPDATE users SET death_shield = death_shield + 100 WHERE id = $1`, grant.UserID); err != nil {
			return "", fmt.Errorf("grant power-user death shield: %w", err)
		}
	}
	quantity := grant.Quantity
	if quantity == 0 {
		quantity = 1
	}
	if quantity < 1 || quantity > maxInventoryQuantity || grant.Days > 0 && quantity != 1 {
		return "", fmt.Errorf("invalid inventory grant duration/quantity %d/%d", grant.Days, quantity)
	}
	acquiredAt := grant.AcquiredAt.UTC()
	if acquiredAt.IsZero() {
		acquiredAt = time.Now().UTC()
	}
	expiry := permanentInventoryExpiry
	if grant.Days > 0 {
		rows, err := tx.Query(ctx, `
			SELECT id, expiry FROM inventory
			WHERE user_id = $1 AND item_id = $2
			ORDER BY id FOR UPDATE`, grant.UserID, grant.ItemID)
		if err != nil {
			return "", fmt.Errorf("lock inventory expiry: %w", err)
		}
		var timedRowID int64
		var timedExpiries []string
		for rows.Next() {
			var rowID int64
			var current string
			if err := rows.Scan(&rowID, &current); err != nil {
				rows.Close()
				return "", fmt.Errorf("scan inventory expiry: %w", err)
			}
			if _, timed := parseTimedInventoryExpiry(current); !timed {
				continue
			}
			if timedRowID == 0 {
				timedRowID = rowID
			}
			timedExpiries = append(timedExpiries, current)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return "", fmt.Errorf("iterate inventory expiry: %w", err)
		}
		rows.Close()

		expiry = extendedInventoryExpiry(acquiredAt, grant.Days, timedExpiries...)
		if timedRowID != 0 {
			if _, err := tx.Exec(ctx, `UPDATE inventory SET expiry = $1 WHERE id = $2`, expiry, timedRowID); err != nil {
				return "", fmt.Errorf("extend inventory expiry: %w", err)
			}
			return expiry, nil
		}
	}
	var stackID int64
	err := tx.QueryRow(ctx, `
		SELECT id FROM inventory
		WHERE user_id = $1 AND item_id = $2 AND type = $3 AND upgrade = 0
		  AND expiry IN ('permanent', '20991231235959') AND paid_currency = $4
		ORDER BY id LIMIT 1 FOR UPDATE`,
		grant.UserID, grant.ItemID, grant.ItemType, grant.Currency).Scan(&stackID)
	if err == nil {
		if _, err := tx.Exec(ctx, `
			UPDATE inventory
			SET quantity = quantity + $1, paid_price = paid_price + $2, expiry = $3
			WHERE id = $4`, quantity, grant.Price, permanentInventoryExpiry, stackID); err != nil {
			return "", fmt.Errorf("extend inventory quantity stack: %w", err)
		}
		return permanentInventoryExpiry, nil
	}
	if !noRows(err) {
		return "", fmt.Errorf("lock inventory quantity stack: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO inventory (user_id, item_id, type, upgrade, expiry, quantity, paid_currency, paid_price, acquired_at)
		VALUES ($1, $2, $3, 0, $4, $5, $6, $7, $8)`,
		grant.UserID, grant.ItemID, grant.ItemType, expiry, quantity, grant.Currency, grant.Price, acquiredAt); err != nil {
		return "", fmt.Errorf("insert inventory quantity stack: %w", err)
	}
	return expiry, nil
}

func purchaseQuantity(request PurchaseOrder) (int, bool) {
	quantity := request.Quantity
	if quantity == 0 {
		quantity = 1
	}
	valid := quantity > 0 && quantity <= maxInventoryQuantity &&
		(request.Days == 0 || quantity == 1) &&
		(len(request.Components) == 0 || quantity == 1)
	return quantity, valid
}

// decrementInventoryStack consumes exactly one unit from a locked inventory
// row. It returns true when the final unit removed the physical row.
func decrementInventoryStack(ctx context.Context, tx pgx.Tx, rowID int64, userID int) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE inventory SET quantity = quantity - 1
		WHERE id = $1 AND user_id = $2 AND quantity > 1`, rowID, userID)
	if err != nil {
		return false, fmt.Errorf("decrement inventory quantity: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return false, nil
	}
	tag, err = tx.Exec(ctx, `DELETE FROM inventory WHERE id = $1 AND user_id = $2 AND quantity = 1`, rowID, userID)
	if err != nil {
		return false, fmt.Errorf("delete final inventory unit: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return false, fmt.Errorf("locked inventory stack %d was not consumable", rowID)
	}
	return true, nil
}

func (s *PostgresStore) SetItemUpgrade(ctx context.Context, userID, itemID int, slot byte, upgradeID uint16) (bool, error) {
	if userID < 1 || itemID < 1 || slot >= 4 {
		return false, nil
	}
	return withSerializableResult(ctx, s, "item upgrade", func(tx pgx.Tx) (bool, error) {
		var rowID int64
		var packed int64
		err := tx.QueryRow(ctx, `
			SELECT id, upgrade FROM inventory
			WHERE user_id = $1 AND item_id = $2
			ORDER BY id LIMIT 1 FOR UPDATE`, userID, itemID).Scan(&rowID, &packed)
		if noRows(err) {
			err = tx.QueryRow(ctx, `
				SELECT id, upgrade FROM inventory
				WHERE user_id = $1 AND id = $2
				ORDER BY id LIMIT 1 FOR UPDATE`, userID, itemID).Scan(&rowID, &packed)
		}
		if noRows(err) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("lock upgraded item: %w", err)
		}
		shift := uint(slot) * 16
		updated := (uint64(packed) & ^(uint64(0xffff) << shift)) | uint64(upgradeID)<<shift
		if _, err := tx.Exec(ctx, `UPDATE inventory SET upgrade = $1 WHERE id = $2`, int64(updated), rowID); err != nil {
			return false, fmt.Errorf("update item upgrade: %w", err)
		}
		return true, nil
	})
}

func (s *PostgresStore) SellItem(ctx context.Context, userID, itemID int) (SellResult, error) {
	result := SellResult{}
	if userID < 1 || itemID < 1 {
		return result, nil
	}
	return withSerializableResult(ctx, s, "item sale", func(tx pgx.Tx) (SellResult, error) {
		var rowID int64
		var paidCurrency, quantity int
		var paidPrice int64
		err := tx.QueryRow(ctx, `
			SELECT id, paid_currency, paid_price, quantity FROM inventory
			WHERE user_id = $1 AND item_id = $2 AND paid_price > 0
			ORDER BY id LIMIT 1 FOR UPDATE`, userID, itemID).Scan(&rowID, &paidCurrency, &paidPrice, &quantity)
		if noRows(err) {
			return result, nil
		}
		if err != nil {
			return result, fmt.Errorf("lock sold item: %w", err)
		}
		unitPrice := (paidPrice + int64(quantity) - 1) / int64(quantity)
		reward := unitPrice / 2
		if reward < 1 {
			reward = 1
		}
		if quantity > 1 {
			if _, err := tx.Exec(ctx, `
				UPDATE inventory SET quantity = quantity - 1, paid_price = paid_price - $1
				WHERE id = $2`, unitPrice, rowID); err != nil {
				return result, fmt.Errorf("decrement sold item stack: %w", err)
			}
		} else if _, err := tx.Exec(ctx, `DELETE FROM inventory WHERE id = $1`, rowID); err != nil {
			return result, fmt.Errorf("delete final sold item: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			UPDATE users SET gold = LEAST(2147483647, gold + $1)
			WHERE id = $2 RETURNING gold, cash`, reward, userID).Scan(&result.Gold, &result.Cash); err != nil {
			return result, fmt.Errorf("credit item sale: %w", err)
		}
		if err := clearUnownedInventoryReferences(ctx, tx, userID, itemID); err != nil {
			return result, err
		}
		result.Success = true
		_ = paidCurrency // sales award gold; acquisition currency is only used by refunds.
		return result, nil
	})
}

func (s *PostgresStore) RefundItem(ctx context.Context, userID, itemID int) (RefundResult, error) {
	result := RefundResult{}
	if userID < 1 || itemID < 1 {
		return result, nil
	}
	return withSerializableResult(ctx, s, "item refund", func(tx pgx.Tx) (RefundResult, error) {
		var rowID int64
		var currency, quantity int
		var price int64
		err := tx.QueryRow(ctx, `
			SELECT id, paid_currency, paid_price, quantity FROM inventory
			WHERE user_id = $1 AND item_id = $2 AND paid_price > 0
			ORDER BY id LIMIT 1 FOR UPDATE`, userID, itemID).Scan(&rowID, &currency, &price, &quantity)
		if noRows(err) {
			return result, nil
		}
		if err != nil {
			return result, fmt.Errorf("lock refunded item: %w", err)
		}
		if currency != 1 && currency != 2 && currency != 4 {
			return result, nil
		}
		unitPrice := (price + int64(quantity) - 1) / int64(quantity)
		if quantity > 1 {
			if _, err := tx.Exec(ctx, `
				UPDATE inventory SET quantity = quantity - 1, paid_price = paid_price - $1
				WHERE id = $2`, unitPrice, rowID); err != nil {
				return result, fmt.Errorf("decrement refunded item stack: %w", err)
			}
		} else if _, err := tx.Exec(ctx, `DELETE FROM inventory WHERE id = $1`, rowID); err != nil {
			return result, fmt.Errorf("delete final refunded item: %w", err)
		}
		query := `UPDATE users SET cash = LEAST(2147483647, cash + $1) WHERE id = $2 RETURNING gold, wc, cash`
		if currency == 2 {
			query = `UPDATE users SET wc = LEAST(2147483647, wc + $1) WHERE id = $2 RETURNING gold, wc, cash`
		} else if currency == 4 {
			query = `UPDATE users SET gold = LEAST(2147483647, gold + $1) WHERE id = $2 RETURNING gold, wc, cash`
		}
		if err := tx.QueryRow(ctx, query, unitPrice, userID).Scan(&result.Gold, &result.WC, &result.Cash); err != nil {
			return result, fmt.Errorf("credit item refund: %w", err)
		}
		if err := clearUnownedInventoryReferences(ctx, tx, userID, itemID); err != nil {
			return result, err
		}
		result.Success = true
		if currency == 1 {
			result.RefundedCash = int(unitPrice)
		} else if currency == 2 {
			result.RefundedWC = int(unitPrice)
		} else {
			result.RefundedGold = int(unitPrice)
		}
		return result, nil
	})
}

func clearUnownedInventoryReferences(ctx context.Context, tx pgx.Tx, userID, itemID int) error {
	var remaining int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM inventory WHERE user_id = $1 AND item_id = $2`, userID, itemID).Scan(&remaining); err != nil {
		return fmt.Errorf("count remaining inventory after removal: %w", err)
	}
	if remaining != 0 {
		return nil
	}
	uniqueID := 2_000_000_000 + itemID
	if _, err := tx.Exec(ctx, `UPDATE equipment SET item_id = 0 WHERE user_id = $1 AND item_id IN ($2, $3)`, userID, itemID, uniqueID); err != nil {
		return fmt.Errorf("clear removed equipment: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM character_equipment
		WHERE user_id = $1 AND (char_id = $2 OR item_unique = $3)`, userID, itemID, uniqueID); err != nil {
		return fmt.Errorf("clear removed character equipment: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM char_slots WHERE user_id = $1 AND char_id = $2`, userID, itemID); err != nil {
		return fmt.Errorf("clear removed character slot: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM character_loadout
		WHERE user_id = $1 AND (red_char_id = $2 OR blue_char_id = $2)`, userID, itemID); err != nil {
		return fmt.Errorf("clear removed character loadout: %w", err)
	}
	return nil
}

func validPurchaseComponents(components []PurchaseComponent) bool {
	if len(components) > 17 {
		return false
	}
	seen := make(map[int]struct{}, len(components))
	for _, component := range components {
		if component.ItemID < 1 || component.ItemID > 65535 || component.Days < 0 || component.Days > 3650 {
			return false
		}
		if _, duplicate := seen[component.ItemID]; duplicate {
			return false
		}
		seen[component.ItemID] = struct{}{}
	}
	return true
}

func grantPurchaseComponents(ctx context.Context, tx pgx.Tx, userID int, components []PurchaseComponent, acquiredAt time.Time) error {
	for _, component := range components {
		itemType := 0
		if component.ItemID >= 30000 {
			itemType = 1
		}
		if _, err := grantInventoryItem(ctx, tx, inventoryGrant{
			UserID: userID, ItemID: component.ItemID, ItemType: itemType,
			Days: component.Days, AcquiredAt: acquiredAt,
		}); err != nil {
			return fmt.Errorf("grant set component %d: %w", component.ItemID, err)
		}
	}
	return nil
}

func (s *PostgresStore) PermanentItemTicketInfo(ctx context.Context, userID, ticketItemID int) (PermanentTicketInfo, error) {
	info := PermanentTicketInfo{}
	if userID < 1 || ticketItemID < 1 {
		return info, nil
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT target_type FROM permanent_item_tickets
		WHERE item_id = $1 AND enabled`, ticketItemID).Scan(&info.TargetType); err != nil {
		if noRows(err) {
			return info, nil
		}
		return info, fmt.Errorf("load permanent ticket configuration: %w", err)
	}
	var owned bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM inventory WHERE user_id = $1 AND item_id = $2)`, userID, ticketItemID).Scan(&owned); err != nil {
		return info, fmt.Errorf("check permanent ticket ownership: %w", err)
	}
	if !owned {
		return info, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT item_id, type, upgrade, expiry FROM inventory
		WHERE user_id = $1 AND item_id <> $2
		  AND expiry NOT IN ('permanent', '20991231235959')
		  AND ($3 = -1 OR type = $3)
		ORDER BY id`, userID, ticketItemID, info.TargetType)
	if err != nil {
		return info, fmt.Errorf("load permanent ticket candidates: %w", err)
	}
	defer rows.Close()
	seen := make(map[int]struct{})
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.Type, &item.Upgrade, &item.Expiry); err != nil {
			return info, fmt.Errorf("scan permanent ticket candidate: %w", err)
		}
		if _, exists := seen[item.ID]; exists {
			continue
		}
		seen[item.ID] = struct{}{}
		info.Eligible = append(info.Eligible, item)
	}
	if err := rows.Err(); err != nil {
		return info, fmt.Errorf("iterate permanent ticket candidates: %w", err)
	}
	info.Valid = true
	return info, nil
}

func (s *PostgresStore) UsePermanentItemTicket(ctx context.Context, userID, ticketItemID, targetItemID int) (bool, error) {
	if userID < 1 || ticketItemID < 1 || targetItemID < 1 || ticketItemID == targetItemID {
		return false, nil
	}
	return withSerializableResult(ctx, s, "permanent ticket use", func(tx pgx.Tx) (bool, error) {
		var targetType int
		if err := tx.QueryRow(ctx, `SELECT target_type FROM permanent_item_tickets WHERE item_id = $1 AND enabled`, ticketItemID).Scan(&targetType); err != nil {
			if noRows(err) {
				return false, nil
			}
			return false, fmt.Errorf("lock permanent ticket configuration: %w", err)
		}
		ticketRow, found, err := lockFirstInventoryRow(ctx, tx, userID, ticketItemID)
		if err != nil || !found {
			return false, err
		}
		var targetRow int64
		var itemType int
		var expiry string
		err = tx.QueryRow(ctx, `
			SELECT id, type, expiry FROM inventory
			WHERE user_id = $1 AND item_id = $2
			ORDER BY id LIMIT 1 FOR UPDATE`, userID, targetItemID).Scan(&targetRow, &itemType, &expiry)
		if noRows(err) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("lock permanent ticket target: %w", err)
		}
		if targetType != -1 && targetType != itemType || expiry == "permanent" || expiry == "20991231235959" {
			return false, nil
		}
		if _, err := decrementInventoryStack(ctx, tx, ticketRow, userID); err != nil {
			return false, fmt.Errorf("consume permanent ticket: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE inventory SET expiry = 'permanent' WHERE id = $1`, targetRow); err != nil {
			return false, fmt.Errorf("make inventory item permanent: %w", err)
		}
		return true, nil
	})
}

func (s *PostgresStore) CommitPurchase(ctx context.Context, request PurchaseOrder) (PurchaseReceipt, error) {
	quantity, validQuantity := purchaseQuantity(request)
	if request.UserID < 1 || request.ItemID < 1 || request.Days < 0 || request.Days > 3650 ||
		!validQuantity || !validPurchaseComponents(request.Components) {
		return PurchaseReceipt{Error: "invalid-request"}, nil
	}
	if request.Currency != 1 && request.Currency != 2 && request.Currency != 4 {
		return PurchaseReceipt{Error: "invalid-request"}, nil
	}
	return withSerializableResult(ctx, s, "purchase", func(tx pgx.Tx) (PurchaseReceipt, error) {
		var gold, wc, cash int
		if err := tx.QueryRow(ctx, `SELECT gold, wc, cash FROM users WHERE id = $1 FOR UPDATE`, request.UserID).Scan(&gold, &wc, &cash); err != nil {
			if noRows(err) {
				return PurchaseReceipt{Error: "user-not-found"}, nil
			}
			return PurchaseReceipt{}, fmt.Errorf("lock purchase balance: %w", err)
		}
		var err error
		isPowerSlot := request.ItemID >= 10050 && request.ItemID <= 10059
		isWeaponSlot := request.ItemID >= 10061 && request.ItemID <= 10064
		if isPowerSlot || isWeaponSlot {
			var owned bool
			if isPowerSlot {
				err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM power_slots WHERE user_id = $1 AND slot = $2)`, request.UserID, request.ItemID-10049).Scan(&owned)
			} else {
				err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM weapon_slot_extensions WHERE user_id = $1 AND set_index = $2)`, request.UserID, request.ItemID-10061).Scan(&owned)
			}
			if err != nil {
				return PurchaseReceipt{}, fmt.Errorf("check entitlement ownership: %w", err)
			}
			if owned {
				return PurchaseReceipt{Error: "already-owned"}, nil
			}
		}
		price := int64(request.Price)
		balance := int64(cash)
		if request.Currency == 2 {
			balance = int64(wc)
		} else if request.Currency == 4 {
			balance = int64(gold)
		}
		if balance < price {
			return PurchaseReceipt{Error: "insufficient-funds"}, nil
		}
		switch request.Currency {
		case 1:
			cash -= int(request.Price)
			_, err = tx.Exec(ctx, `UPDATE users SET cash = $1 WHERE id = $2`, cash, request.UserID)
		case 2:
			wc -= int(request.Price)
			_, err = tx.Exec(ctx, `UPDATE users SET wc = $1 WHERE id = $2`, wc, request.UserID)
		case 4:
			gold -= int(request.Price)
			_, err = tx.Exec(ctx, `UPDATE users SET gold = $1 WHERE id = $2`, gold, request.UserID)
		}
		if err != nil {
			return PurchaseReceipt{}, fmt.Errorf("debit purchase balance: %w", err)
		}
		acquiredAt := s.now().UTC()
		expiry := permanentInventoryExpiry
		if request.Days > 0 {
			expiry = acquiredAt.AddDate(0, 0, request.Days).Format("20060102150405")
		}
		switch {
		case isPowerSlot:
			_, err = tx.Exec(ctx, `INSERT INTO power_slots (user_id, slot) VALUES ($1, $2)`, request.UserID, request.ItemID-10049)
		case isWeaponSlot:
			_, err = tx.Exec(ctx, `INSERT INTO weapon_slot_extensions (user_id, set_index) VALUES ($1, $2)`, request.UserID, request.ItemID-10061)
		case len(request.Components) > 0:
			err = grantPurchaseComponents(ctx, tx, request.UserID, request.Components, acquiredAt)
		default:
			expiry, err = grantInventoryItem(ctx, tx, inventoryGrant{
				UserID: request.UserID, ItemID: request.ItemID, ItemType: request.ItemType,
				Days: request.Days, Quantity: quantity, Currency: request.Currency, Price: request.Price, AcquiredAt: acquiredAt,
			})
		}
		if err != nil {
			return PurchaseReceipt{}, fmt.Errorf("grant purchase: %w", err)
		}
		return PurchaseReceipt{Success: true, Expiry: expiry, Gold: gold, WC: wc, Cash: cash}, nil
	})
}

func (s *PostgresStore) DailyShopItems(ctx context.Context, day time.Time) ([2]uint16, error) {
	items := [2]uint16{10038, 10087}
	err := s.pool.QueryRow(ctx, `
		SELECT item_a, item_b FROM daily_shop
		WHERE active_day = $1`, day.UTC().Format("2006-01-02")).Scan(&items[0], &items[1])
	if noRows(err) {
		return items, nil
	}
	if err != nil {
		return [2]uint16{}, fmt.Errorf("load daily shop: %w", err)
	}
	return items, nil
}

func (s *PostgresStore) PurchaseBasket(ctx context.Context, requests []PurchaseOrder) (BasketPurchaseResult, error) {
	result := BasketPurchaseResult{Items: make([]PurchaseReceipt, len(requests))}
	if len(requests) == 0 || len(requests) > 255 {
		return result, nil
	}
	userID := requests[0].UserID
	for _, request := range requests {
		_, validQuantity := purchaseQuantity(request)
		if request.UserID != userID || request.ItemID < 1 || request.Days < 0 || request.Days > 3650 ||
			request.Currency != 1 && request.Currency != 2 && request.Currency != 4 ||
			request.ItemID >= 10050 && request.ItemID <= 10064 || !validQuantity || !validPurchaseComponents(request.Components) {
			return result, nil
		}
	}
	return withSerializableResult(ctx, s, "basket purchase", func(tx pgx.Tx) (BasketPurchaseResult, error) {
		if err := tx.QueryRow(ctx, `SELECT gold, wc, cash FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&result.Gold, &result.WC, &result.Cash); err != nil {
			return result, fmt.Errorf("lock basket balances: %w", err)
		}
		totals := map[int]int64{}
		for _, request := range requests {
			totals[request.Currency] += int64(request.Price)
		}
		if int64(result.Cash) < totals[1] || int64(result.WC) < totals[2] || int64(result.Gold) < totals[4] {
			return result, nil
		}
		result.Cash -= int(totals[1])
		result.WC -= int(totals[2])
		result.Gold -= int(totals[4])
		if _, err := tx.Exec(ctx, `UPDATE users SET gold = $1, wc = $2, cash = $3 WHERE id = $4`, result.Gold, result.WC, result.Cash, userID); err != nil {
			return result, fmt.Errorf("debit basket balances: %w", err)
		}
		for index, request := range requests {
			quantity, _ := purchaseQuantity(request)
			acquiredAt := s.now().UTC()
			expiry := permanentInventoryExpiry
			var err error
			if len(request.Components) > 0 {
				if request.Days > 0 {
					expiry = acquiredAt.AddDate(0, 0, request.Days).Format("20060102150405")
				}
				err = grantPurchaseComponents(ctx, tx, userID, request.Components, acquiredAt)
			} else {
				expiry, err = grantInventoryItem(ctx, tx, inventoryGrant{
					UserID: userID, ItemID: request.ItemID, ItemType: request.ItemType,
					Days: request.Days, Quantity: quantity, Currency: request.Currency, Price: request.Price, AcquiredAt: acquiredAt,
				})
			}
			if err != nil {
				return result, fmt.Errorf("grant basket item %d: %w", request.ItemID, err)
			}
			result.Items[index] = PurchaseReceipt{Success: true, Expiry: expiry, Gold: result.Gold, WC: result.WC, Cash: result.Cash}
		}
		result.Success = true
		return result, nil
	})
}

func (s *PostgresStore) ForgeRecipes(ctx context.Context, category byte) ([]ForgeRecipe, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT category, challenge, choice_index, cost_cash, reward_item_id, reward_days
		FROM forge_recipes WHERE category = $1 AND enabled
		ORDER BY challenge, choice_index`, category)
	if err != nil {
		return nil, fmt.Errorf("load forge recipes: %w", err)
	}
	defer rows.Close()
	var recipes []ForgeRecipe
	for rows.Next() {
		var recipe ForgeRecipe
		if err := rows.Scan(&recipe.Category, &recipe.Challenge, &recipe.Choice, &recipe.CostCash, &recipe.RewardItem, &recipe.RewardDays); err != nil {
			return nil, fmt.Errorf("scan forge recipe: %w", err)
		}
		recipes = append(recipes, recipe)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate forge recipes: %w", err)
	}
	return recipes, nil
}

func createForgeGift(ctx context.Context, tx pgx.Tx, attempt ForgeAttempt, itemID, days int, now time.Time) (bool, error) {
	var senderID int
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE username = 'forge_system'`).Scan(&senderID); err != nil {
		return false, fmt.Errorf("load forge gift sender: %w", err)
	}
	if err := purgeExpiredGiftsTx(ctx, tx, now); err != nil {
		return false, err
	}
	var giftCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM gifts WHERE recipient_user_id = $1 AND expires_at > $2`, attempt.UserID, now).Scan(&giftCount); err != nil {
		return false, fmt.Errorf("count forge gift mailbox: %w", err)
	}
	if giftCount >= giftMailboxCapacity {
		return false, nil
	}
	itemType := 0
	if itemID >= 30000 {
		itemType = 1
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO gifts (sender_user_id, recipient_user_id, item_id, item_type, days, message, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, 'Item Forge reward', $6, $7)`,
		senderID, attempt.UserID, itemID, itemType, days, now, giftExpiresAt(now)); err != nil {
		return false, fmt.Errorf("create forge gift: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forge_wins (user_id, category, reward_item_id, reward_days)
		VALUES ($1, $2, $3, $4)`, attempt.UserID, attempt.Category, itemID, days); err != nil {
		return false, fmt.Errorf("record forge reward: %w", err)
	}
	return true, nil
}

// 2012 forge failure result codes (WShopSection sub_341C45D0).
const (
	forgeStatusFailed     = 97
	forgeStatusNoCurrency = 98
)

func (s *PostgresStore) RunForgeChallenge(ctx context.Context, attempt ForgeAttempt) (ForgeResult, error) {
	result := ForgeResult{}
	if attempt.UserID < 1 || attempt.Category > 2 || attempt.Round > 4 || attempt.Action > 1 {
		return ForgeResult{Status: forgeStatusFailed}, nil
	}
	return withSerializableResult(ctx, s, "forge challenge", func(tx pgx.Tx) (ForgeResult, error) {
		if attempt.Action == 0 {
			result.RewardItem, result.RewardDays = attempt.RewardItem, attempt.RewardDays
			if result.RewardItem < 1 || result.RewardDays < 0 {
				return ForgeResult{Status: forgeStatusFailed}, nil
			}
			var valid bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM forge_recipes
					WHERE category = $1 AND challenge = $2 AND reward_item_id = $3
					  AND reward_days = $4 AND enabled
				)`, attempt.Category, attempt.Round, result.RewardItem, result.RewardDays).Scan(&valid); err != nil {
				return ForgeResult{}, fmt.Errorf("validate forge reward: %w", err)
			}
			if !valid {
				return ForgeResult{Status: forgeStatusFailed}, nil
			}
			created, err := createForgeGift(ctx, tx, attempt, result.RewardItem, result.RewardDays, s.now().UTC())
			if err != nil {
				return ForgeResult{}, err
			}
			if !created {
				return ForgeResult{Status: forgeStatusFailed}, nil
			}
			result.Outcome = 2
		} else {
			var cost int
			var passed bool
			if err := tx.QueryRow(ctx, `
				SELECT cost_cash, reward_item_id, reward_days,
				       random() * 100 < success_percent
				FROM forge_recipes
				WHERE category = $1 AND challenge = $2 AND enabled
				ORDER BY random() LIMIT 1`, attempt.Category, attempt.Round).
				Scan(&cost, &result.RewardItem, &result.RewardDays, &passed); err != nil {
				if noRows(err) {
					return ForgeResult{Status: forgeStatusFailed}, nil
				}
				return result, fmt.Errorf("load forge challenge: %w", err)
			}
			if attempt.Round == 0 {
				debitQuery := `UPDATE users SET cash = cash - $1 WHERE id = $2 AND cash >= $1 RETURNING cash`
				switch attempt.Category {
				case 1:
					debitQuery = `UPDATE users SET gold = gold - $1 WHERE id = $2 AND gold >= $1 RETURNING cash`
				case 2:
					debitQuery = `UPDATE users SET wc = wc - $1 WHERE id = $2 AND wc >= $1 RETURNING cash`
				}
				if err := tx.QueryRow(ctx, debitQuery, cost, attempt.UserID).Scan(&result.Cash); err != nil {
					if noRows(err) {
						return ForgeResult{Status: forgeStatusNoCurrency}, nil
					}
					return ForgeResult{}, fmt.Errorf("debit forge challenge: %w", err)
				}
			} else if err := tx.QueryRow(ctx, `SELECT cash FROM users WHERE id = $1`, attempt.UserID).Scan(&result.Cash); err != nil {
				return ForgeResult{}, fmt.Errorf("load forge balance: %w", err)
			}
			if passed {
				result.Outcome = 1
			} else {
				created, err := createForgeGift(ctx, tx, attempt, result.RewardItem, result.RewardDays, s.now().UTC())
				if err != nil {
					return ForgeResult{}, err
				}
				if !created {
					return ForgeResult{Status: forgeStatusFailed}, nil
				}
			}
		}
		if result.Cash == 0 {
			if err := tx.QueryRow(ctx, `SELECT cash FROM users WHERE id = $1`, attempt.UserID).Scan(&result.Cash); err != nil {
				return ForgeResult{}, fmt.Errorf("load final forge balance: %w", err)
			}
		}
		return result, nil
	})
}

func (s *PostgresStore) ForgeWinners(ctx context.Context, category byte) ([]ForgeWinner, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.callsign, w.reward_item_id, w.category, w.reward_days, w.won_at
		FROM forge_wins w JOIN users u ON u.id = w.user_id
		WHERE w.category = $1 ORDER BY w.won_at DESC LIMIT 20`, category)
	if err != nil {
		return nil, fmt.Errorf("load forge winners: %w", err)
	}
	defer rows.Close()
	var winners []ForgeWinner
	for rows.Next() {
		var winner ForgeWinner
		if err := rows.Scan(&winner.Callsign, &winner.RewardItem, &winner.Category, &winner.Days, &winner.WonAt); err != nil {
			return nil, fmt.Errorf("scan forge winner: %w", err)
		}
		winners = append(winners, winner)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate forge winners: %w", err)
	}
	return winners, nil
}

func (s *PostgresStore) VIPShopItems(ctx context.Context, section, page byte) ([]VIPShopItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT section, page, slot, group_id, mode, item_id, currency, days, price
		FROM vip_shop_items WHERE section = $1 AND page = $2 AND enabled
		ORDER BY slot, item_id`, section, page)
	if err != nil {
		return nil, fmt.Errorf("load VIP shop items: %w", err)
	}
	defer rows.Close()
	var items []VIPShopItem
	for rows.Next() {
		var item VIPShopItem
		if err := rows.Scan(&item.Section, &item.Page, &item.Slot, &item.GroupID, &item.Mode, &item.ItemID, &item.Currency, &item.Days, &item.Price); err != nil {
			return nil, fmt.Errorf("scan VIP shop item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate VIP shop items: %w", err)
	}
	return items, nil
}

func (s *PostgresStore) ConsumeCountedItems(ctx context.Context, userID int, uniqueIDs []int) ([]CountedItemUse, error) {
	if userID < 1 || len(uniqueIDs) < 1 || len(uniqueIDs) > 32 {
		return nil, nil
	}
	itemIDs := make([]int, len(uniqueIDs))
	seen := make(map[int]struct{}, len(uniqueIDs))
	for index, uniqueID := range uniqueIDs {
		itemID := uniqueID - 2_000_000_000
		if itemID < 1 || itemID > 0xffff {
			return nil, nil
		}
		if _, duplicate := seen[itemID]; duplicate {
			return nil, nil
		}
		seen[itemID] = struct{}{}
		itemIDs[index] = itemID
	}
	return withSerializableResult(ctx, s, "counted item use", func(tx pgx.Tx) ([]CountedItemUse, error) {
		results := make([]CountedItemUse, 0, len(uniqueIDs))
		for index, uniqueID := range uniqueIDs {
			itemID := itemIDs[index]
			var rowID int64
			err := tx.QueryRow(ctx, `
				SELECT i.id
				FROM inventory i
				WHERE i.user_id = $1 AND i.item_id = $2 AND i.type = 0
				  AND i.expiry IN ('permanent', '20991231235959')
				  AND EXISTS (
				      SELECT 1 FROM equipment e
				      WHERE e.user_id = i.user_id AND e.slot BETWEEN 24 AND 33
				        AND e.item_id IN ($2, $3)
				  )
				ORDER BY i.id LIMIT 1
				FOR UPDATE OF i`, userID, itemID, uniqueID).Scan(&rowID)
			if noRows(err) {
				return nil, fmt.Errorf("counted inventory item %d is not equipped or unavailable", itemID)
			}
			if err != nil {
				return nil, fmt.Errorf("lock counted inventory item %d: %w", itemID, err)
			}
			if _, err := decrementInventoryStack(ctx, tx, rowID, userID); err != nil {
				return nil, fmt.Errorf("consume counted inventory item %d: %w", itemID, err)
			}
			var remaining int64
			if err := tx.QueryRow(ctx, `
				SELECT COALESCE(sum(quantity), 0)
				FROM inventory
				WHERE user_id = $1 AND item_id = $2 AND type = 0
				  AND expiry IN ('permanent', '20991231235959')`,
				userID, itemID).Scan(&remaining); err != nil {
				return nil, fmt.Errorf("count remaining inventory item %d: %w", itemID, err)
			}
			if remaining == 0 {
				if err := clearUnownedInventoryReferences(ctx, tx, userID, itemID); err != nil {
					return nil, err
				}
			}
			results = append(results, CountedItemUse{
				UniqueID:  uniqueID,
				Remaining: int(min(remaining, int64(0xffff))),
			})
		}
		return results, nil
	})
}

func (s *PostgresStore) ConsumeInventoryItem(ctx context.Context, userID, itemID int) (bool, error) {
	credit, supported := couponCreditForItem(itemID)
	if userID < 1 || itemID < 1 || !supported {
		return false, nil
	}
	return withSerializableResult(ctx, s, "item use", func(tx pgx.Tx) (bool, error) {
		rowID, found, err := lockFirstInventoryRow(ctx, tx, userID, itemID)
		if err != nil {
			return false, err
		}
		if !found {
			return false, nil
		}
		if _, err := decrementInventoryStack(ctx, tx, rowID, userID); err != nil {
			return false, fmt.Errorf("consume inventory item: %w", err)
		}
		switch {
		case credit.Gold > 0:
			_, err = tx.Exec(ctx, `UPDATE users SET gold = LEAST(2147483647, gold + $1) WHERE id = $2`, credit.Gold, userID)
		case credit.WC > 0:
			_, err = tx.Exec(ctx, `UPDATE users SET wc = LEAST(2147483647, wc + $1) WHERE id = $2`, credit.WC, userID)
		}
		if err != nil {
			return false, fmt.Errorf("apply consumed item: %w", err)
		}
		return true, nil
	})
}

func (s *PostgresStore) ClaimPackageContents(ctx context.Context, userID, itemID int) (bool, error) {
	packages := map[int][][2]int{
		14132: {{12149, 30}, {20025, 30}, {21041, 30}},
		14133: {{12150, 30}, {20026, 30}, {21042, 30}},
		14134: {{12151, 30}, {20027, 30}, {21043, 30}},
		14135: {{12152, 30}, {20028, 30}, {21044, 30}},
		14136: {{12153, 30}, {20029, 30}, {21045, 30}},
		14137: {{12154, 30}, {20030, 30}, {21046, 30}},
		14032: {{10027, 30}, {12031, 7}},
	}
	components, supported := packages[itemID]
	if userID < 1 || !supported {
		return false, nil
	}
	return withSerializableResult(ctx, s, "package open", func(tx pgx.Tx) (bool, error) {
		rowID, found, err := lockFirstInventoryRow(ctx, tx, userID, itemID)
		if err != nil {
			return false, err
		}
		if !found {
			return false, nil
		}
		if _, err := decrementInventoryStack(ctx, tx, rowID, userID); err != nil {
			return false, fmt.Errorf("consume package: %w", err)
		}
		acquiredAt := s.now().UTC()
		for _, component := range components {
			itemType := 0
			if component[0] >= 30000 {
				itemType = 1
			}
			if _, err := grantInventoryItem(ctx, tx, inventoryGrant{
				UserID: userID, ItemID: component[0], ItemType: itemType,
				Days: component[1], AcquiredAt: acquiredAt,
			}); err != nil {
				return false, fmt.Errorf("grant package component: %w", err)
			}
		}
		return true, nil
	})
}

func (s *PostgresStore) DrawLuckyBox(ctx context.Context, userID, itemID, boxUnique int) (LuckyBoxReceipt, error) {
	result := LuckyBoxReceipt{BoxUnique: boxUnique}
	if userID < 1 || s.luckyBoxes == nil || !s.luckyBoxes.HasBox(itemID) {
		result.Error = "unsupported-box"
		return result, nil
	}
	roll, err := cryptorand.Int(cryptorand.Reader, big.NewInt(10_000))
	if err != nil {
		return result, fmt.Errorf("roll lucky-box reward: %w", err)
	}
	reward, err := s.luckyBoxes.Resolve(itemID, int(roll.Int64()))
	if err != nil {
		return result, fmt.Errorf("resolve lucky-box reward: %w", err)
	}
	days := reward.MinimumDays
	if reward.MaximumDays > reward.MinimumDays {
		durationRange := int64(reward.MaximumDays - reward.MinimumDays + 1)
		duration, randomErr := cryptorand.Int(cryptorand.Reader, big.NewInt(durationRange))
		if randomErr != nil {
			return result, fmt.Errorf("choose lucky-box reward duration: %w", randomErr)
		}
		days += int(duration.Int64())
	}
	giftDays := days
	if reward.Stackable {
		giftDays = 0 // Quantity/currency rewards are permanent stackable items when claimed.
	}
	return withSerializableResult(ctx, s, "lucky-box open", func(tx pgx.Tx) (LuckyBoxReceipt, error) {
		rowID, found, err := lockFirstInventoryRow(ctx, tx, userID, itemID)
		if err != nil {
			return result, err
		}
		if !found {
			result.Error = "box-not-found"
			return result, nil
		}
		var senderID int
		if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE username = 'lucky_box_system'`).Scan(&senderID); err != nil {
			return result, fmt.Errorf("load lucky-box gift sender: %w", err)
		}
		now := s.now().UTC()
		if err := purgeExpiredGiftsTx(ctx, tx, now); err != nil {
			return result, err
		}
		var giftCount int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM gifts WHERE recipient_user_id = $1 AND expires_at > $2`, userID, now).Scan(&giftCount); err != nil {
			return result, fmt.Errorf("count lucky-box gift mailbox: %w", err)
		}
		if giftCount >= giftMailboxCapacity {
			result.Error = "gift-mailbox-full"
			return result, nil
		}
		if _, err := decrementInventoryStack(ctx, tx, rowID, userID); err != nil {
			return result, fmt.Errorf("consume lucky box: %w", err)
		}
		var remaining int64
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(sum(quantity), 0)
			FROM inventory
			WHERE user_id = $1 AND item_id = $2`, userID, itemID).Scan(&remaining); err != nil {
			return result, fmt.Errorf("count remaining lucky boxes: %w", err)
		}
		itemType := 0
		if reward.ItemID >= 30000 {
			itemType = 1
		}
		giftQuantity := 1
		if reward.Stackable {
			giftQuantity = max(1, days)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO gifts (sender_user_id, recipient_user_id, item_id, item_type, days, quantity, message, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'Lucky Box reward', $7, $8)`,
			senderID, userID, reward.ItemID, itemType, giftDays, giftQuantity, now, giftExpiresAt(now)); err != nil {
			return result, fmt.Errorf("queue lucky-box reward: %w", err)
		}
		result.Success = true
		result.RewardItem = reward.ItemID
		result.Days = max(1, days)
		// Keep the wire value in the positive int16 range even if a malformed
		// account owns more boxes.
		result.Remaining = int(min(remaining, int64(0x7fff)))
		return result, nil
	})
}

func (s *PostgresStore) RemoveInventoryItem(ctx context.Context, userID, itemID int) (bool, error) {
	protected := map[int]bool{1001: true, 2001: true, 3001: true, 9001: true, 30005: true, 30006: true}
	if userID < 1 || itemID < 1 || protected[itemID] || itemID >= 10050 && itemID <= 10064 {
		return false, nil
	}
	return withSerializableResult(ctx, s, "item delete", func(tx pgx.Tx) (bool, error) {
		rowID, found, itemType, err := lockInventoryRowWithType(ctx, tx, userID, itemID)
		if err != nil {
			return false, err
		}
		if !found {
			return false, nil
		}
		if _, err := decrementInventoryStack(ctx, tx, rowID, userID); err != nil {
			return false, fmt.Errorf("delete inventory item: %w", err)
		}
		var remaining int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM inventory WHERE user_id = $1 AND item_id = $2`, userID, itemID).Scan(&remaining); err != nil {
			return false, fmt.Errorf("count remaining inventory: %w", err)
		}
		if remaining == 0 {
			uniqueID := 2_000_000_000 + itemID
			if _, err := tx.Exec(ctx, `
				UPDATE equipment SET item_id = 0
				WHERE user_id = $1 AND item_id IN ($2, $3)`, userID, itemID, uniqueID); err != nil {
				return false, fmt.Errorf("clear deleted equipment: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				DELETE FROM character_equipment
				WHERE user_id = $1 AND (char_id = $2 OR item_unique = $3)`, userID, itemID, uniqueID); err != nil {
				return false, fmt.Errorf("clear deleted character equipment: %w", err)
			}
			if itemType == 1 {
				if _, err := tx.Exec(ctx, `DELETE FROM char_slots WHERE user_id = $1 AND char_id = $2`, userID, itemID); err != nil {
					return false, fmt.Errorf("clear deleted character slot: %w", err)
				}
				if _, err := tx.Exec(ctx, `
					UPDATE character_loadout SET
						red_char_id = CASE WHEN red_char_id = $2 THEN 30006 ELSE red_char_id END,
						blue_char_id = CASE WHEN blue_char_id = $2 THEN 30005 ELSE blue_char_id END,
						updated_at = now()
					WHERE user_id = $1`, userID, itemID); err != nil {
					return false, fmt.Errorf("repair deleted character loadout: %w", err)
				}
			}
		}
		return true, nil
	})
}

func lockFirstInventoryRow(ctx context.Context, tx pgx.Tx, userID, itemID int) (int64, bool, error) {
	var rowID int64
	err := tx.QueryRow(ctx, `
		SELECT id FROM inventory
		WHERE user_id = $1 AND item_id = $2
		ORDER BY id LIMIT 1 FOR UPDATE`, userID, itemID).Scan(&rowID)
	if noRows(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("lock inventory item: %w", err)
	}
	return rowID, true, nil
}

func lockInventoryRowWithType(ctx context.Context, tx pgx.Tx, userID, itemID int) (int64, bool, int, error) {
	var rowID int64
	var itemType int
	err := tx.QueryRow(ctx, `
		SELECT id, type FROM inventory
		WHERE user_id = $1 AND item_id = $2
		ORDER BY id LIMIT 1 FOR UPDATE`, userID, itemID).Scan(&rowID, &itemType)
	if noRows(err) {
		return 0, false, 0, nil
	}
	if err != nil {
		return 0, false, 0, fmt.Errorf("lock inventory item: %w", err)
	}
	return rowID, true, itemType, nil
}
