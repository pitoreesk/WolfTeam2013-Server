package store

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// ExItemEffect is what one use of an EX item does (2013 use types, item +110;
// docs/protocol/exitem_2013.md).
type ExItemEffect struct {
	Type           int
	Gold, WC, GP   int
	ResetKills     bool   // type 2: kill/death record
	ResetWins      bool   // type 3: win/lose record
	NewCallsign    string // type 1
	NewPrideName   string // type 4 (the user must be the Pride master)
}

// ExItemStore consumes one EX item and applies its effect in one transaction.
// A refused effect (name taken, not a Pride master, ...) consumes nothing.
type ExItemStore interface {
	UseExItem(ctx context.Context, userID, itemID int, effect ExItemEffect) (bool, error)
	FullUpgrade(ctx context.Context, userID, weaponID, kitID int, values [4]uint16) (bool, error)
}

func (s *PostgresStore) UseExItem(ctx context.Context, userID, itemID int, effect ExItemEffect) (bool, error) {
	if userID < 1 || itemID < 1 {
		return false, nil
	}
	return withSerializableResult(ctx, s, "EX item use", func(tx pgx.Tx) (bool, error) {
		rowID, found, err := lockFirstInventoryRow(ctx, tx, userID, itemID)
		if err != nil || !found {
			return false, err
		}
		switch {
		case effect.NewCallsign != "":
			if !validCallsign(effect.NewCallsign) {
				return false, nil
			}
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE lower(callsign) = lower($1) AND id <> $2)`,
				effect.NewCallsign, userID).Scan(&taken); err != nil || taken {
				return false, err
			}
			if _, err := tx.Exec(ctx, `UPDATE users SET callsign = $1 WHERE id = $2`, effect.NewCallsign, userID); err != nil {
				return false, fmt.Errorf("rename user: %w", err)
			}
		case effect.NewPrideName != "":
			name := strings.TrimSpace(effect.NewPrideName)
			if name != effect.NewPrideName || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 16 {
				return false, nil
			}
			var prideID int64
			err := tx.QueryRow(ctx, `SELECT id FROM prides WHERE master_user_id = $1 FOR UPDATE`, userID).Scan(&prideID)
			if noRows(err) {
				return false, nil
			}
			if err != nil {
				return false, fmt.Errorf("lock renamed pride: %w", err)
			}
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM prides WHERE lower(name) = lower($1) AND id <> $2)`,
				name, prideID).Scan(&taken); err != nil || taken {
				return false, err
			}
			if _, err := tx.Exec(ctx, `UPDATE prides SET name = $1 WHERE id = $2`, name, prideID); err != nil {
				return false, fmt.Errorf("rename pride: %w", err)
			}
		case effect.ResetKills:
			if _, err := tx.Exec(ctx, `
				UPDATE users SET kills = 0, deaths = 0, headshot_kills = 0, special_weapon_kills = 0, wolf_kills = 0
				WHERE id = $1`, userID); err != nil {
				return false, fmt.Errorf("reset kill record: %w", err)
			}
		case effect.ResetWins:
			if _, err := tx.Exec(ctx, `UPDATE users SET wins = 0, losses = 0, draws = 0 WHERE id = $1`, userID); err != nil {
				return false, fmt.Errorf("reset win record: %w", err)
			}
		case effect.Gold > 0 || effect.WC > 0:
			if _, err := tx.Exec(ctx, `
				UPDATE users SET gold = LEAST(2147483647, gold + $1), wc = LEAST(2147483647, wc + $2) WHERE id = $3`,
				effect.Gold, effect.WC, userID); err != nil {
				return false, fmt.Errorf("credit coupon: %w", err)
			}
		case effect.GP > 0:
			var level, gp int
			if err := tx.QueryRow(ctx, `SELECT level, gp FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&level, &gp); err != nil {
				return false, fmt.Errorf("lock GP coupon balance: %w", err)
			}
			level, gp = applyRankProgress(level, min(gp+effect.GP, 2147483647))
			if _, err := tx.Exec(ctx, `UPDATE users SET level = $1, gp = $2 WHERE id = $3`, level, gp, userID); err != nil {
				return false, fmt.Errorf("credit GP coupon: %w", err)
			}
		default:
			return false, nil
		}
		if _, err := decrementInventoryStack(ctx, tx, rowID, userID); err != nil {
			return false, fmt.Errorf("consume EX item: %w", err)
		}
		return true, clearUnownedInventoryReferences(ctx, tx, userID, itemID)
	})
}

// FullUpgrade applies a Full-up kit (use type 12): the weapon's four upgrade
// slots become the given values and one kit unit is consumed.
func (s *PostgresStore) FullUpgrade(ctx context.Context, userID, weaponID, kitID int, values [4]uint16) (bool, error) {
	if userID < 1 || weaponID < 1 || kitID < 1 {
		return false, nil
	}
	return withSerializableResult(ctx, s, "full upgrade", func(tx pgx.Tx) (bool, error) {
		weaponRow, found, err := lockFirstInventoryRow(ctx, tx, userID, weaponID)
		if err != nil || !found {
			return false, err
		}
		kitRow, found, err := lockFirstInventoryRow(ctx, tx, userID, kitID)
		if err != nil || !found {
			return false, err
		}
		packed := uint64(values[0]) | uint64(values[1])<<16 | uint64(values[2])<<32 | uint64(values[3])<<48
		if _, err := tx.Exec(ctx, `UPDATE inventory SET upgrade = $1 WHERE id = $2`, int64(packed), weaponRow); err != nil {
			return false, fmt.Errorf("full upgrade weapon: %w", err)
		}
		if _, err := decrementInventoryStack(ctx, tx, kitRow, userID); err != nil {
			return false, fmt.Errorf("consume full-up kit: %w", err)
		}
		return true, clearUnownedInventoryReferences(ctx, tx, userID, kitID)
	})
}
