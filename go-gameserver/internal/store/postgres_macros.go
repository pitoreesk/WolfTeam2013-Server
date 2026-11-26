package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Macro is one chat macro slot (0..4).
type Macro struct {
	Slot byte
	Flag byte
	Text string
}

// MacroStore persists chat macros (migration 043).
type MacroStore interface {
	Macros(ctx context.Context, userID int) ([]Macro, error)
	SaveMacros(ctx context.Context, userID int, macros []Macro) error
}

func (s *PostgresStore) Macros(ctx context.Context, userID int) ([]Macro, error) {
	rows, err := s.pool.Query(ctx, `SELECT slot, flag, text FROM user_macros WHERE user_id = $1 ORDER BY slot`, userID)
	if err != nil {
		return nil, fmt.Errorf("load macros: %w", err)
	}
	defer rows.Close()
	var macros []Macro
	for rows.Next() {
		var slot, flag int
		var text string
		if err := rows.Scan(&slot, &flag, &text); err != nil {
			return nil, fmt.Errorf("scan macro: %w", err)
		}
		macros = append(macros, Macro{Slot: byte(slot), Flag: byte(flag), Text: text})
	}
	return macros, rows.Err()
}

func (s *PostgresStore) SaveMacros(ctx context.Context, userID int, macros []Macro) error {
	return s.withSerializable(ctx, func(tx pgx.Tx) error {
		for _, macro := range macros {
			if macro.Slot > 4 {
				continue
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO user_macros (user_id, slot, flag, text) VALUES ($1, $2, $3, $4)
				ON CONFLICT (user_id, slot) DO UPDATE SET flag = excluded.flag, text = excluded.text, updated_at = now()`,
				userID, int(macro.Slot), int(macro.Flag), macro.Text); err != nil {
				return fmt.Errorf("save macro: %w", err)
			}
		}
		return nil
	})
}
