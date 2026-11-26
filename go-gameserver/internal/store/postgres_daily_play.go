package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// DailyPlayResult is the day's play time after a match and the P.T. bonus it
// paid.
type DailyPlayResult struct {
	DailySeconds uint32
	RewardGold   int
	Gold         int // balance after the reward
}

// DailyPlayStore keeps the day's play time and the P.T. bonus tiers already
// paid (migration 044). reward returns the Gold for the tiers reached beyond
// paid and the new number of tiers reached.
type DailyPlayStore interface {
	AddDailyPlayTime(ctx context.Context, userID int, seconds uint32, reward func(paid int, daily uint32) (gold, reached int)) (DailyPlayResult, error)
}

func (s *PostgresStore) AddDailyPlayTime(ctx context.Context, userID int, seconds uint32, reward func(int, uint32) (int, int)) (DailyPlayResult, error) {
	var result DailyPlayResult
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		var daily int64
		var paid int
		var today bool
		if err := tx.QueryRow(ctx, `
			SELECT daily_play_seconds, pt_bonus_paid, daily_play_date IS NOT DISTINCT FROM current_date
			FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&daily, &paid, &today); err != nil {
			return fmt.Errorf("lock daily play time: %w", err)
		}
		if !today {
			daily, paid = 0, 0
		}
		daily = min(daily+int64(seconds), 172800)
		gold, reached := reward(paid, uint32(daily))
		if err := tx.QueryRow(ctx, `
			UPDATE users SET daily_play_seconds = $1, daily_play_date = current_date, pt_bonus_paid = $2,
			       gold = LEAST(2147483647, gold + $3)
			WHERE id = $4 RETURNING gold`, daily, max(reached, paid), gold, userID).Scan(&result.Gold); err != nil {
			return fmt.Errorf("save daily play time: %w", err)
		}
		result.DailySeconds, result.RewardGold = uint32(daily), gold
		return nil
	})
	return result, err
}
