package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// GiftRefundResult is the outcome of converting an unclaimed gift into
// currency (2013 gift box "convert" button, CS_GI_REFUND_REQ 0x1A10).
type GiftRefundResult struct {
	Success bool
	WC      int // credited Wolf Coin (KP)
	Gold    int // credited Gold
	Balance AccountSnapshot
}

// GiftRefundStore converts received gifts. value prices the locked gift; a
// gift valued 0/0 is not convertible.
type GiftRefundStore interface {
	RefundGift(ctx context.Context, userID int, giftID uint32, value func(Gift) (wc, gold int)) (GiftRefundResult, error)
}

func (s *PostgresStore) RefundGift(ctx context.Context, userID int, giftID uint32, value func(Gift) (wc, gold int)) (GiftRefundResult, error) {
	result := GiftRefundResult{}
	if userID < 1 || giftID == 0 {
		return result, nil
	}
	now := s.now().UTC()
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		var gift Gift
		var status int
		err := tx.QueryRow(ctx, `
			SELECT id, status, item_id, item_type, days, quantity FROM gifts
			WHERE id = $1 AND recipient_user_id = $2 AND expires_at > $3
			FOR UPDATE`, giftID, userID, now).Scan(
			&gift.ID, &status, &gift.ItemID, &gift.ItemType, &gift.Days, &gift.Quantity)
		if noRows(err) || status == 2 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock refunded gift: %w", err)
		}
		gift.Status = byte(status)
		wc, gold := value(gift)
		if wc < 0 || gold < 0 || wc == 0 && gold == 0 {
			return nil
		}
		// The converted gift leaves the mailbox (gift_components cascade).
		if _, err := tx.Exec(ctx, `DELETE FROM gifts WHERE id = $1`, giftID); err != nil {
			return fmt.Errorf("delete refunded gift: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			UPDATE users SET wc = LEAST(2147483647, wc + $1), gold = LEAST(2147483647, gold + $2)
			WHERE id = $3 RETURNING gold, wc, cash`, wc, gold, userID).Scan(
			&result.Balance.Gold, &result.Balance.WC, &result.Balance.Cash); err != nil {
			return fmt.Errorf("credit refunded gift: %w", err)
		}
		result.Success, result.WC, result.Gold = true, wc, gold
		return nil
	})
	if err != nil {
		return GiftRefundResult{}, err
	}
	return result, nil
}
