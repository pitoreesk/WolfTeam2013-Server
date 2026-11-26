package store

import (
	"context"
	"fmt"
)

// powerUserItemID is the Power User EX item (Güçlü Kullanıcı).
const powerUserItemID = 10027

// PowerUserStore persists the Power User chat color and name prefix.
type PowerUserStore interface {
	PowerUserSettings(ctx context.Context, userID int) (color uint32, label string, err error)
	SavePowerUserSettings(ctx context.Context, userID int, color uint32, label string) error
}

func (s *PostgresStore) PowerUserSettings(ctx context.Context, userID int) (uint32, string, error) {
	var color int64
	var label string
	if err := s.pool.QueryRow(ctx, `SELECT power_user_color, power_user_label FROM users WHERE id = $1`, userID).Scan(&color, &label); err != nil {
		return 0, "", fmt.Errorf("load power-user settings: %w", err)
	}
	return uint32(color), label, nil
}

func (s *PostgresStore) SavePowerUserSettings(ctx context.Context, userID int, color uint32, label string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE users SET power_user_color = $1, power_user_label = $2 WHERE id = $3`,
		int64(color), label, userID); err != nil {
		return fmt.Errorf("save power-user settings: %w", err)
	}
	return nil
}
