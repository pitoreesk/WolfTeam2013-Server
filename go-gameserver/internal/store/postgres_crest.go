package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInsufficientCrestGold  = errors.New("insufficient Gold for crest challenge")
	ErrCrestChallengeConflict = errors.New("crest challenge conflicts with the active challenges")
)

// CrestDelegateSlot is one worn emblem (client vector +2332, roster tail).
type CrestDelegateSlot struct {
	Slot    byte
	CrestID uint16
}

// CrestActiveChallenge is one paid challenge and its raw event counter.
type CrestActiveChallenge struct {
	CrestID  uint16
	Group    uint16
	Progress uint32
}

// CrestState is everything the crest screen shows (0x134D).
type CrestState struct {
	Delegates []CrestDelegateSlot
	Completed map[uint16]bool
	Active    []CrestActiveChallenge
}

// CrestChallengeStart names one challenge to buy.
type CrestChallengeStart struct {
	CrestID uint16
	Group   uint16
}

// CrestCredit is the outcome of one gameplay event for a challenge group.
type CrestCredit struct {
	Moved     bool // an active challenge of the group counted the event
	CrestID   uint16
	Progress  uint32
	Completed bool // the target was reached: the challenge was retired
	RewardWC  uint32
	WC        int // balance after the reward
}

// CrestStore persists the 2013 crest system (migration 042).
type CrestStore interface {
	CrestState(ctx context.Context, userID int) (CrestState, error)
	SetCrestDelegateSlot(ctx context.Context, userID int, slot byte, crestID uint16) ([]CrestDelegateSlot, error)
	StartCrestChallenges(ctx context.Context, userID int, starts []CrestChallengeStart, cost uint32) (gold uint32, err error)
	CancelCrestChallenge(ctx context.Context, userID int, crestID uint16) (bool, error)
	CreditCrestGroup(ctx context.Context, userID int, group uint16, delta uint32, goal func(crestID uint16) (target, rewardWC uint32, ok bool)) (CrestCredit, error)
}

func (s *PostgresStore) CrestState(ctx context.Context, userID int) (CrestState, error) {
	state := CrestState{Completed: map[uint16]bool{}}
	if userID < 1 {
		return state, nil
	}
	delegates, err := crestDelegates(ctx, s.pool, userID)
	if err != nil {
		return state, err
	}
	state.Delegates = delegates
	rows, err := s.pool.Query(ctx, `SELECT crest_id FROM crest_progress WHERE user_id = $1 AND progress >= 100`, userID)
	if err != nil {
		return state, fmt.Errorf("load completed crests: %w", err)
	}
	for rows.Next() {
		var crestID int
		if err := rows.Scan(&crestID); err != nil {
			rows.Close()
			return state, fmt.Errorf("scan completed crest: %w", err)
		}
		state.Completed[uint16(crestID)] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return state, fmt.Errorf("iterate completed crests: %w", err)
	}
	rows, err = s.pool.Query(ctx, `
		SELECT crest_id, group_id, progress FROM crest_active_challenges
		WHERE user_id = $1 ORDER BY started_at, crest_id`, userID)
	if err != nil {
		return state, fmt.Errorf("load active crest challenges: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var crestID, group int
		var progress int64
		if err := rows.Scan(&crestID, &group, &progress); err != nil {
			return state, fmt.Errorf("scan active crest challenge: %w", err)
		}
		state.Active = append(state.Active, CrestActiveChallenge{CrestID: uint16(crestID), Group: uint16(group), Progress: uint32(progress)})
	}
	return state, rows.Err()
}

type crestQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func crestDelegates(ctx context.Context, db crestQuerier, userID int) ([]CrestDelegateSlot, error) {
	rows, err := db.Query(ctx, `SELECT slot, crest_id FROM crest_delegate_slots WHERE user_id = $1 ORDER BY slot`, userID)
	if err != nil {
		return nil, fmt.Errorf("load crest delegate slots: %w", err)
	}
	defer rows.Close()
	var slots []CrestDelegateSlot
	for rows.Next() {
		var slot, crestID int
		if err := rows.Scan(&slot, &crestID); err != nil {
			return nil, fmt.Errorf("scan crest delegate slot: %w", err)
		}
		slots = append(slots, CrestDelegateSlot{Slot: byte(slot), CrestID: uint16(crestID)})
	}
	return slots, rows.Err()
}

// SetCrestDelegateSlot wears a completed crest in a slot (moving it out of any
// other slot); crest 0 empties the slot. It returns the whole slot list.
func (s *PostgresStore) SetCrestDelegateSlot(ctx context.Context, userID int, slot byte, crestID uint16) ([]CrestDelegateSlot, error) {
	var slots []CrestDelegateSlot
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM crest_delegate_slots WHERE user_id = $1 AND (slot = $2 OR crest_id = $3)`,
			userID, int(slot), int(crestID)); err != nil {
			return fmt.Errorf("clear crest delegate slot: %w", err)
		}
		if crestID != 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO crest_delegate_slots (user_id, slot, crest_id)
				SELECT $1, $2, $3 WHERE EXISTS (
					SELECT 1 FROM crest_progress WHERE user_id = $1 AND crest_id = $3 AND progress >= 100)`,
				userID, int(slot), int(crestID)); err != nil {
				return fmt.Errorf("save crest delegate slot: %w", err)
			}
		}
		loaded, err := crestDelegates(ctx, tx, userID)
		slots = loaded
		return err
	})
	return slots, err
}

// StartCrestChallenges pays for challenges and starts their counters at 0.
func (s *PostgresStore) StartCrestChallenges(ctx context.Context, userID int, starts []CrestChallengeStart, cost uint32) (uint32, error) {
	var gold int64
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT gold FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&gold); err != nil {
			return fmt.Errorf("lock crest challenge balance: %w", err)
		}
		if gold < int64(cost) {
			return ErrInsufficientCrestGold
		}
		var active int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM crest_active_challenges WHERE user_id = $1`, userID).Scan(&active); err != nil {
			return fmt.Errorf("count active crest challenges: %w", err)
		}
		if active+len(starts) > 3 {
			return ErrCrestChallengeConflict
		}
		for _, start := range starts {
			tag, err := tx.Exec(ctx, `
				INSERT INTO crest_active_challenges (user_id, crest_id, group_id) VALUES ($1, $2, $3)
				ON CONFLICT DO NOTHING`, userID, int(start.CrestID), int(start.Group))
			if err != nil {
				return fmt.Errorf("start crest challenge: %w", err)
			}
			if tag.RowsAffected() != 1 {
				return ErrCrestChallengeConflict
			}
		}
		if err := tx.QueryRow(ctx, `UPDATE users SET gold = gold - $1 WHERE id = $2 RETURNING gold`, int64(cost), userID).Scan(&gold); err != nil {
			return fmt.Errorf("debit crest challenge: %w", err)
		}
		return nil
	})
	if gold < 0 {
		gold = 0
	}
	return uint32(gold), err
}

// CancelCrestChallenge drops one active challenge and its counter (no refund).
func (s *PostgresStore) CancelCrestChallenge(ctx context.Context, userID int, crestID uint16) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM crest_active_challenges WHERE user_id = $1 AND crest_id = $2`, userID, int(crestID))
	if err != nil {
		return false, fmt.Errorf("cancel crest challenge: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// CreditCrestGroup counts one event for the user's active challenge of a group
// and, at the target, retires it: completion 100, reward WC, row removed.
func (s *PostgresStore) CreditCrestGroup(ctx context.Context, userID int, group uint16, delta uint32, goal func(uint16) (uint32, uint32, bool)) (CrestCredit, error) {
	result := CrestCredit{}
	if userID < 1 || delta == 0 {
		return result, nil
	}
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		var crestID int
		var progress int64
		err := tx.QueryRow(ctx, `
			UPDATE crest_active_challenges SET progress = LEAST(4294967295, progress + $3)
			WHERE user_id = $1 AND group_id = $2 RETURNING crest_id, progress`,
			userID, int(group), int64(delta)).Scan(&crestID, &progress)
		if noRows(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("count crest event: %w", err)
		}
		result = CrestCredit{Moved: true, CrestID: uint16(crestID), Progress: uint32(progress)}
		target, reward, ok := goal(result.CrestID)
		if !ok || uint32(progress) < target {
			return nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM crest_active_challenges WHERE user_id = $1 AND crest_id = $2`, userID, crestID); err != nil {
			return fmt.Errorf("retire crest challenge: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO crest_progress (user_id, crest_id, progress, updated_at) VALUES ($1, $2, 100, now())
			ON CONFLICT (user_id, crest_id) DO UPDATE SET progress = 100, updated_at = now()`, userID, crestID); err != nil {
			return fmt.Errorf("complete crest: %w", err)
		}
		if err := tx.QueryRow(ctx, `UPDATE users SET wc = LEAST(2147483647, wc + $1) WHERE id = $2 RETURNING wc`,
			int64(reward), userID).Scan(&result.WC); err != nil {
			return fmt.Errorf("pay crest reward: %w", err)
		}
		result.Completed, result.RewardWC = true, reward
		return nil
	})
	if err != nil {
		return CrestCredit{}, err
	}
	return result, nil
}

// ItemUnitStore consumes one unit of a quantity item outside a match, such as
// the Kick Vote ticket (10039) that the client decrements when it starts an
// in-game kick vote (sub_342F7770).
type ItemUnitStore interface {
	ConsumeItemUnit(ctx context.Context, userID, itemID int) (bool, error)
}

func (s *PostgresStore) ConsumeItemUnit(ctx context.Context, userID, itemID int) (bool, error) {
	if userID < 1 || itemID < 1 {
		return false, nil
	}
	return withSerializableResult(ctx, s, "item unit use", func(tx pgx.Tx) (bool, error) {
		rowID, found, err := lockFirstInventoryRow(ctx, tx, userID, itemID)
		if err != nil || !found {
			return false, err
		}
		if _, err := decrementInventoryStack(ctx, tx, rowID, userID); err != nil {
			return false, fmt.Errorf("consume item unit: %w", err)
		}
		return true, clearUnownedInventoryReferences(ctx, tx, userID, itemID)
	})
}
