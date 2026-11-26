package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// EventBonus is one 1+1 event row (migration 046). Units are 0 = days and
// 1 = count, as the client formats them (sub_3407D0C0).
type EventBonus struct {
	ItemID        int
	RequiredUnit  byte
	RequiredValue int
	BonusItemID   int
	BonusUnit     byte
	BonusAmount   int
}

// EventBonusStore lists the active 1+1 rows and sends a bonus to the Gift Box.
type EventBonusStore interface {
	EventBonusItems(ctx context.Context) ([]EventBonus, error)
	GrantEventBonus(ctx context.Context, userID int, bonus EventBonus) (bool, error)
}

func (s *PostgresStore) EventBonusItems(ctx context.Context) ([]EventBonus, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT item_id, required_unit, required_value, bonus_item_id, bonus_unit, bonus_amount
		FROM event_bonus_items WHERE enabled ORDER BY item_id`)
	if err != nil {
		return nil, fmt.Errorf("load event bonus items: %w", err)
	}
	defer rows.Close()
	var bonuses []EventBonus
	for rows.Next() {
		var bonus EventBonus
		var requiredUnit, bonusUnit int16
		if err := rows.Scan(&bonus.ItemID, &requiredUnit, &bonus.RequiredValue, &bonus.BonusItemID, &bonusUnit, &bonus.BonusAmount); err != nil {
			return nil, fmt.Errorf("scan event bonus item: %w", err)
		}
		bonus.RequiredUnit, bonus.BonusUnit = byte(requiredUnit), byte(bonusUnit)
		bonuses = append(bonuses, bonus)
	}
	return bonuses, rows.Err()
}

// GrantEventBonus queues the bonus as a gift from the event sender. It
// reports false when the Gift Box is full.
func (s *PostgresStore) GrantEventBonus(ctx context.Context, userID int, bonus EventBonus) (bool, error) {
	if userID < 1 || bonus.BonusItemID < 1 || bonus.BonusAmount < 1 {
		return false, nil
	}
	return withSerializableResult(ctx, s, "event bonus gift", func(tx pgx.Tx) (bool, error) {
		var senderID int
		if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE username = 'event_system'`).Scan(&senderID); err != nil {
			return false, fmt.Errorf("load event gift sender: %w", err)
		}
		now := s.now().UTC()
		if err := purgeExpiredGiftsTx(ctx, tx, now); err != nil {
			return false, err
		}
		var giftCount int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM gifts WHERE recipient_user_id = $1 AND expires_at > $2`, userID, now).Scan(&giftCount); err != nil {
			return false, fmt.Errorf("count event gift mailbox: %w", err)
		}
		if giftCount >= giftMailboxCapacity {
			return false, nil
		}
		// A gift carries days, or a quantity with days 0 (GiftDetailPayload
		// shows the days, else the quantity).
		days, quantity := min(bonus.BonusAmount, 3650), 1
		if bonus.BonusUnit != 0 {
			days, quantity = 0, bonus.BonusAmount
		}
		itemType := 0
		if bonus.BonusItemID >= 30000 {
			itemType = 1
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO gifts (sender_user_id, recipient_user_id, item_id, item_type, days, quantity, message, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, '1+1', $7, $8)`,
			senderID, userID, bonus.BonusItemID, itemType, days, quantity, now, giftExpiresAt(now)); err != nil {
			return false, fmt.Errorf("queue event bonus gift: %w", err)
		}
		return true, nil
	})
}
