package store

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	giftMailboxCapacity      = 100
	giftMailboxRetentionDays = 90
)

func giftExpiresAt(createdAt time.Time) time.Time {
	return createdAt.AddDate(0, 0, giftMailboxRetentionDays)
}

func giftInboxDaysRemaining(now, expiresAt time.Time) int {
	remaining := expiresAt.Sub(now)
	if remaining <= 0 {
		return 0
	}
	days := int((remaining + 24*time.Hour - 1) / (24 * time.Hour))
	return min(days, giftMailboxRetentionDays)
}

func (s *PostgresStore) purgeExpiredGifts(ctx context.Context, now time.Time) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM gifts WHERE expires_at <= $1`, now); err != nil {
		return fmt.Errorf("delete expired gifts: %w", err)
	}
	return nil
}

func purgeExpiredGiftsTx(ctx context.Context, tx pgx.Tx, now time.Time) error {
	if _, err := tx.Exec(ctx, `DELETE FROM gifts WHERE expires_at <= $1`, now); err != nil {
		return fmt.Errorf("delete expired gifts: %w", err)
	}
	return nil
}

func (s *PostgresStore) SendGift(ctx context.Context, request GiftSendRequest) (GiftSendResult, error) {
	request.ReceiverCallsign = strings.TrimSpace(request.ReceiverCallsign)
	if request.Quantity == 0 {
		request.Quantity = 1
	}
	if request.SenderID < 1 || request.ReceiverCallsign == "" || request.ItemID < 1 || request.ItemID > 65535 ||
		request.ItemType < 0 || request.ItemType > 1 || request.Days < 0 || request.Days > 3650 ||
		request.Quantity < 1 || request.Quantity > maxInventoryQuantity || request.Days > 0 && request.Quantity != 1 ||
		len(request.Components) > 0 && request.Quantity != 1 ||
		(request.Currency != 1 && request.Currency != 2 && request.Currency != 4) || utf8.RuneCountInString(request.Message) > 127 ||
		!validPurchaseComponents(request.Components) {
		return GiftSendResult{Error: "invalid-request"}, nil
	}
	return withSerializableResult(ctx, s, "send gift", func(tx pgx.Tx) (GiftSendResult, error) {
		result := GiftSendResult{}
		now := s.now().UTC()
		if err := purgeExpiredGiftsTx(ctx, tx, now); err != nil {
			return result, err
		}
		var receiverID int
		err := tx.QueryRow(ctx, `SELECT id FROM users WHERE lower(callsign) = lower($1)`, request.ReceiverCallsign).Scan(&receiverID)
		if noRows(err) {
			return GiftSendResult{Error: "receiver-not-found"}, nil
		}
		if err != nil {
			return result, fmt.Errorf("look up gift receiver: %w", err)
		}
		if receiverID == request.SenderID {
			return GiftSendResult{Error: "self-gift"}, nil
		}

		rows, err := tx.Query(ctx, `
			SELECT id FROM users WHERE id IN ($1, $2) ORDER BY id FOR UPDATE`,
			request.SenderID, receiverID)
		if err != nil {
			return result, fmt.Errorf("lock gift accounts: %w", err)
		}
		locked := 0
		for rows.Next() {
			locked++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return result, fmt.Errorf("iterate locked gift accounts: %w", err)
		}
		rows.Close()
		if locked != 2 {
			return GiftSendResult{Error: "user-not-found"}, nil
		}

		if err := tx.QueryRow(ctx, `SELECT gold, wc, cash FROM users WHERE id = $1`, request.SenderID).
			Scan(&result.Gold, &result.WC, &result.Cash); err != nil {
			return result, fmt.Errorf("load gift sender balances: %w", err)
		}
		var giftCount int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM gifts WHERE recipient_user_id = $1 AND expires_at > $2`, receiverID, now).Scan(&giftCount); err != nil {
			return result, fmt.Errorf("count recipient gifts: %w", err)
		}
		if giftCount >= giftMailboxCapacity {
			result.Error = "mailbox-full"
			return result, nil
		}

		price := int64(request.Price)
		balance := int64(result.Cash)
		if request.Currency == 2 {
			balance = int64(result.WC)
		} else if request.Currency == 4 {
			balance = int64(result.Gold)
		}
		if balance < price {
			result.Error = "insufficient-funds"
			return result, nil
		}
		switch request.Currency {
		case 1:
			result.Cash -= int(request.Price)
		case 2:
			result.WC -= int(request.Price)
		case 4:
			result.Gold -= int(request.Price)
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET gold = $1, wc = $2, cash = $3 WHERE id = $4`,
			result.Gold, result.WC, result.Cash, request.SenderID); err != nil {
			return result, fmt.Errorf("debit gift sender: %w", err)
		}
		var giftID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO gifts (sender_user_id, recipient_user_id, item_id, item_type, days, quantity, message, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			RETURNING id`, request.SenderID, receiverID, request.ItemID, request.ItemType,
			request.Days, request.Quantity, request.Message, now, giftExpiresAt(now)).Scan(&giftID); err != nil {
			return result, fmt.Errorf("insert sent gift: %w", err)
		}
		for position, component := range request.Components {
			if _, err := tx.Exec(ctx, `
				INSERT INTO gift_components (gift_id, position, item_id, days)
				VALUES ($1, $2, $3, $4)`, giftID, position, component.ItemID, component.Days); err != nil {
				return result, fmt.Errorf("insert sent gift component %d: %w", component.ItemID, err)
			}
		}
		result.Success = true
		return result, nil
	})
}

func (s *PostgresStore) GiftCount(ctx context.Context, userID int) (int, error) {
	if userID < 1 {
		return 0, nil
	}
	now := s.now().UTC()
	if err := s.purgeExpiredGifts(ctx, now); err != nil {
		return 0, err
	}
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM gifts WHERE recipient_user_id = $1 AND expires_at > $2`, userID, now).Scan(&count); err != nil {
		return 0, fmt.Errorf("count gifts: %w", err)
	}
	return count, nil
}

func (s *PostgresStore) Gifts(ctx context.Context, userID int, kind byte, page, pageSize int) (GiftPage, error) {
	result := GiftPage{}
	if userID < 1 || kind > 1 || page < 0 || pageSize < 1 || pageSize > giftMailboxCapacity {
		return result, nil
	}
	now := s.now().UTC()
	if err := s.purgeExpiredGifts(ctx, now); err != nil {
		return result, err
	}
	ownerColumn := "recipient_user_id"
	counterpartyColumn := "sender_user_id"
	if kind == 1 {
		ownerColumn = "sender_user_id"
		counterpartyColumn = "recipient_user_id"
	}
	countQuery := fmt.Sprintf(`SELECT count(*) FROM gifts WHERE %s = $1 AND expires_at > $2`, ownerColumn)
	if err := s.pool.QueryRow(ctx, countQuery, userID, now).Scan(&result.Total); err != nil {
		return result, fmt.Errorf("count gift page: %w", err)
	}
	if result.Total > 0 {
		result.TotalPages = (result.Total + pageSize - 1) / pageSize
	}
	query := fmt.Sprintf(`
		SELECT g.id, g.status, g.item_id, g.item_type, g.days, g.quantity,
		       counterparty.callsign, g.message, g.created_at, g.expires_at
		FROM gifts g
		JOIN users counterparty ON counterparty.id = g.%s
		WHERE g.%s = $1 AND g.expires_at > $2
		ORDER BY (g.status = 2), g.created_at DESC, g.id DESC
		LIMIT $3 OFFSET $4`, counterpartyColumn, ownerColumn)
	rows, err := s.pool.Query(ctx, query, userID, now, pageSize, page*pageSize)
	if err != nil {
		return GiftPage{}, fmt.Errorf("load gift page: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var gift Gift
		var status int
		if err := rows.Scan(&gift.ID, &status, &gift.ItemID, &gift.ItemType, &gift.Days, &gift.Quantity,
			&gift.Counterparty, &gift.Message, &gift.CreatedAt, &gift.ExpiresAt); err != nil {
			return GiftPage{}, fmt.Errorf("scan gift page: %w", err)
		}
		gift.Status = byte(status)
		gift.InboxDays = giftInboxDaysRemaining(now, gift.ExpiresAt)
		result.Gifts = append(result.Gifts, gift)
	}
	if err := rows.Err(); err != nil {
		return GiftPage{}, fmt.Errorf("iterate gift page: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) GiftDetail(ctx context.Context, userID int, giftID uint32, kind byte) (Gift, bool, error) {
	var gift Gift
	if userID < 1 || giftID == 0 || kind > 1 {
		return gift, false, nil
	}
	var status int
	found := false
	now := s.now().UTC()
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		if err := purgeExpiredGiftsTx(ctx, tx, now); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `
			SELECT g.id, g.status, g.item_id, g.item_type, g.days, g.quantity,
			       CASE WHEN $3 = 0 THEN sender.callsign ELSE recipient.callsign END,
			       g.message, g.created_at, g.expires_at
			FROM gifts g
			JOIN users sender ON sender.id = g.sender_user_id
			JOIN users recipient ON recipient.id = g.recipient_user_id
			WHERE g.id = $1
			  AND (($3 = 0 AND g.recipient_user_id = $2) OR ($3 = 1 AND g.sender_user_id = $2))
			  AND g.expires_at > $4
			FOR UPDATE OF g`, giftID, userID, kind, now).Scan(
			&gift.ID, &status, &gift.ItemID, &gift.ItemType, &gift.Days, &gift.Quantity,
			&gift.Counterparty, &gift.Message, &gift.CreatedAt, &gift.ExpiresAt)
		if noRows(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load gift detail: %w", err)
		}
		if kind == 0 && status == 0 {
			if _, err := tx.Exec(ctx, `UPDATE gifts SET status = 1 WHERE id = $1`, giftID); err != nil {
				return fmt.Errorf("mark gift read: %w", err)
			}
			status = 1
		}
		gift.Status = byte(status)
		gift.InboxDays = giftInboxDaysRemaining(now, gift.ExpiresAt)
		found = true
		return nil
	})
	if err != nil {
		return gift, false, fmt.Errorf("gift detail transaction: %w", err)
	}
	return gift, found, nil
}

func (s *PostgresStore) ClaimGift(ctx context.Context, userID int, giftID uint32) (GiftClaimResult, error) {
	result := GiftClaimResult{}
	if userID < 1 || giftID == 0 {
		return result, nil
	}
	var status int
	now := s.now().UTC()
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		if err := purgeExpiredGiftsTx(ctx, tx, now); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `
			SELECT g.id, g.status, g.item_id, g.item_type, g.days, g.quantity,
			       sender.callsign, g.message, g.created_at, g.expires_at
			FROM gifts g
			JOIN users sender ON sender.id = g.sender_user_id
			WHERE g.id = $1 AND g.recipient_user_id = $2 AND g.expires_at > $3
			FOR UPDATE OF g`, giftID, userID, now).Scan(
			&result.Gift.ID, &status, &result.Gift.ItemID, &result.Gift.ItemType,
			&result.Gift.Days, &result.Gift.Quantity, &result.Gift.Counterparty, &result.Gift.Message,
			&result.Gift.CreatedAt, &result.Gift.ExpiresAt)
		if noRows(err) || status == 2 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock gift claim: %w", err)
		}
		result.Gift.InboxDays = giftInboxDaysRemaining(now, result.Gift.ExpiresAt)
		componentRows, err := tx.Query(ctx, `
			SELECT item_id, days FROM gift_components
			WHERE gift_id = $1 ORDER BY position`, giftID)
		if err != nil {
			return fmt.Errorf("load gift components: %w", err)
		}
		var components []PurchaseComponent
		for componentRows.Next() {
			var component PurchaseComponent
			if err := componentRows.Scan(&component.ItemID, &component.Days); err != nil {
				componentRows.Close()
				return fmt.Errorf("scan gift component: %w", err)
			}
			components = append(components, component)
		}
		if err := componentRows.Err(); err != nil {
			componentRows.Close()
			return fmt.Errorf("iterate gift components: %w", err)
		}
		componentRows.Close()
		if err := tx.QueryRow(ctx, `SELECT gold, cash FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&result.Gold, &result.Cash); err != nil {
			return fmt.Errorf("lock gift claim balances: %w", err)
		}
		claimedAt := s.now().UTC()
		if len(components) > 0 {
			result.Expiry = permanentInventoryExpiry
			if result.Gift.Days > 0 {
				result.Expiry = claimedAt.AddDate(0, 0, result.Gift.Days).Format("20060102150405")
			}
			err = grantPurchaseComponents(ctx, tx, userID, components, claimedAt)
		} else {
			result.Expiry, err = grantInventoryItem(ctx, tx, inventoryGrant{
				UserID: userID, ItemID: result.Gift.ItemID, ItemType: result.Gift.ItemType,
				Days: result.Gift.Days, Quantity: result.Gift.Quantity, AcquiredAt: claimedAt,
			})
		}
		if err != nil {
			return fmt.Errorf("grant claimed gift: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE gifts SET status = 2, claimed_at = $2 WHERE id = $1`, giftID, claimedAt); err != nil {
			return fmt.Errorf("finish gift claim: %w", err)
		}
		result.Gift.Status = 2
		result.Success = true
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("claim gift transaction: %w", err)
	}
	return result, nil
}
