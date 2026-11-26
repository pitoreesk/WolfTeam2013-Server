package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Ranking board kinds: the RankingSection KIND value (combo strings
// 9530-9533 personal "Öldürme/Kayıt/GP/Ölüm", 9535-9538 Pride "Klan Öld/
// Klan Rekoru/Klan Puanı/Klan Ölm").
const (
	RankingKindKill   = 0
	RankingKindRecord = 1
	RankingKindPoints = 2 // GP, or Pride points on Pride boards
	RankingKindDeath  = 3
)

// ranking_events.event values.
const (
	RankingEventWin  = 1
	RankingEventLoss = 2
	RankingEventDraw = 3
	RankingEventGP   = 4
)

func rankingEventForOutcome(outcome MatchOutcome) int {
	switch outcome {
	case MatchWin:
		return RankingEventWin
	case MatchLoss:
		return RankingEventLoss
	default:
		return RankingEventDraw
	}
}

// RankingQuery selects one board. A zero From means all time; otherwise the
// board counts what happened in [From, To).
type RankingQuery struct {
	Pride    bool
	Kind     int
	From, To time.Time
}

// RankingEntry is one row of a board, best first.
type RankingEntry struct {
	Account string // username, or the Pride id in decimal
	Name    string // callsign, or the Pride name
	Level   int    // class emblem (0 on Pride boards)
	Value   int64  // kills, wins, GP/Pride points or deaths
	Losses  int64  // record boards only
}

// RankingCommentKey names a board as the client sends it.
type RankingCommentKey struct {
	Type, Kind, PeriodType int
	Period                 int64
	MainType, SubType      int64
}

// RankingStore feeds the ranking web module.
type RankingStore interface {
	RankingBoard(ctx context.Context, query RankingQuery, limit int) ([]RankingEntry, error)
	RankingComments(ctx context.Context, key RankingCommentKey) (map[string]string, error)
	SetRankingComment(ctx context.Context, key RankingCommentKey, owner, comment string) error
	PrideMasterAccount(ctx context.Context, prideID uint32) (string, error)
}

// rankingBoardSQL returns the query of a board. $1/$2 are the period bounds
// of dated boards and $3 the row limit (the all-time boards use $1 only).
func rankingBoardSQL(query RankingQuery) (string, bool, error) {
	dated := !query.From.IsZero()
	if query.Pride {
		battles := `pride_battles b JOIN prides p ON p.id = b.pride_id`
		period := ``
		if dated {
			period = ` WHERE b.played_at >= $1 AND b.played_at < $2`
		}
		limit := `$1`
		if dated {
			limit = `$3`
		}
		switch query.Kind {
		case RankingKindKill, RankingKindDeath:
			column := "m.kills"
			if query.Kind == RankingKindDeath {
				column = "m.deaths"
			}
			return `SELECT p.id::text, p.name, 0, SUM(` + column + `)::bigint AS value, 0::bigint
				FROM ` + battles + ` JOIN pride_battle_members m ON m.battle_id = b.id` + period + `
				GROUP BY p.id, p.name HAVING SUM(` + column + `) > 0
				ORDER BY value DESC, p.id LIMIT ` + limit, dated, nil
		case RankingKindRecord:
			if !dated {
				return `SELECT id::text, name, 0, wins::bigint, losses::bigint FROM prides
					WHERE wins + losses > 0 ORDER BY wins DESC, losses, id LIMIT $1`, false, nil
			}
			return `SELECT p.id::text, p.name, 0,
					COUNT(*) FILTER (WHERE b.won)::bigint AS wins, COUNT(*) FILTER (WHERE NOT b.won)::bigint AS losses
				FROM ` + battles + period + `
				GROUP BY p.id, p.name ORDER BY wins DESC, losses, p.id LIMIT ` + limit, true, nil
		case RankingKindPoints:
			if !dated {
				return `SELECT id::text, name, 0, rank_points::bigint, 0::bigint FROM prides
					WHERE rank_points > 0 ORDER BY ` + prideRankOrder + ` LIMIT $1`, false, nil
			}
			return `SELECT p.id::text, p.name, 0, SUM(b.rank_delta)::bigint AS value, 0::bigint
				FROM ` + battles + period + `
				GROUP BY p.id, p.name HAVING SUM(b.rank_delta) > 0
				ORDER BY value DESC, p.id LIMIT ` + limit, true, nil
		}
		return "", false, fmt.Errorf("unknown ranking kind %d", query.Kind)
	}
	if !dated {
		switch query.Kind {
		case RankingKindKill:
			return `SELECT username, callsign, level, kills::bigint, 0::bigint FROM users
				WHERE kills > 0 ORDER BY kills DESC, id LIMIT $1`, false, nil
		case RankingKindRecord:
			return `SELECT username, callsign, level, wins::bigint, losses::bigint FROM users
				WHERE wins + losses > 0 ORDER BY wins DESC, losses, id LIMIT $1`, false, nil
		case RankingKindPoints:
			return `SELECT username, callsign, level, gp::bigint, 0::bigint FROM users
				WHERE gp > 0 ORDER BY gp DESC, id LIMIT $1`, false, nil
		case RankingKindDeath:
			return `SELECT username, callsign, level, deaths::bigint, 0::bigint FROM users
				WHERE deaths > 0 ORDER BY deaths DESC, id LIMIT $1`, false, nil
		}
		return "", false, fmt.Errorf("unknown ranking kind %d", query.Kind)
	}
	switch query.Kind {
	case RankingKindKill, RankingKindDeath:
		column := "killer_id"
		if query.Kind == RankingKindDeath {
			column = "victim_id"
		}
		return `SELECT u.username, u.callsign, u.level, COUNT(*)::bigint AS value, 0::bigint
			FROM kill_log k JOIN users u ON u.id = k.` + column + `
			WHERE k.occurred_at >= $1 AND k.occurred_at < $2
			GROUP BY u.id ORDER BY value DESC, u.id LIMIT $3`, true, nil
	case RankingKindRecord:
		return `SELECT u.username, u.callsign, u.level,
				COUNT(*) FILTER (WHERE e.event = 1)::bigint AS wins, COUNT(*) FILTER (WHERE e.event = 2)::bigint AS losses
			FROM ranking_events e JOIN users u ON u.id = e.user_id
			WHERE e.event IN (1, 2) AND e.occurred_at >= $1 AND e.occurred_at < $2
			GROUP BY u.id ORDER BY wins DESC, losses, u.id LIMIT $3`, true, nil
	case RankingKindPoints:
		return `SELECT u.username, u.callsign, u.level, SUM(e.amount)::bigint AS value, 0::bigint
			FROM ranking_events e JOIN users u ON u.id = e.user_id
			WHERE e.event = 4 AND e.occurred_at >= $1 AND e.occurred_at < $2
			GROUP BY u.id HAVING SUM(e.amount) > 0 ORDER BY value DESC, u.id LIMIT $3`, true, nil
	}
	return "", false, fmt.Errorf("unknown ranking kind %d", query.Kind)
}

func (s *PostgresStore) RankingBoard(ctx context.Context, query RankingQuery, limit int) ([]RankingEntry, error) {
	sql, dated, err := rankingBoardSQL(query)
	if err != nil {
		return nil, err
	}
	args := []any{limit}
	if dated {
		args = []any{query.From, query.To, limit}
	}
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("load ranking board: %w", err)
	}
	defer rows.Close()
	var entries []RankingEntry
	for rows.Next() {
		var entry RankingEntry
		if err := rows.Scan(&entry.Account, &entry.Name, &entry.Level, &entry.Value, &entry.Losses); err != nil {
			return nil, fmt.Errorf("scan ranking board: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *PostgresStore) RankingComments(ctx context.Context, key RankingCommentKey) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT owner, comment FROM ranking_comments
		WHERE rank_type = $1 AND kind = $2 AND period_type = $3 AND period = $4 AND main_type = $5 AND sub_type = $6`,
		key.Type, key.Kind, key.PeriodType, key.Period, key.MainType, key.SubType)
	if err != nil {
		return nil, fmt.Errorf("load ranking comments: %w", err)
	}
	defer rows.Close()
	comments := make(map[string]string)
	for rows.Next() {
		var owner, comment string
		if err := rows.Scan(&owner, &comment); err != nil {
			return nil, fmt.Errorf("scan ranking comment: %w", err)
		}
		comments[strings.ToLower(owner)] = comment
	}
	return comments, rows.Err()
}

func (s *PostgresStore) SetRankingComment(ctx context.Context, key RankingCommentKey, owner, comment string) error {
	owner = strings.ToLower(owner)
	if owner == "" || len(owner) > 20 {
		return fmt.Errorf("invalid ranking comment owner")
	}
	if comment == "" {
		_, err := s.pool.Exec(ctx, `
			DELETE FROM ranking_comments
			WHERE rank_type = $1 AND kind = $2 AND period_type = $3 AND period = $4 AND main_type = $5 AND sub_type = $6 AND owner = $7`,
			key.Type, key.Kind, key.PeriodType, key.Period, key.MainType, key.SubType, owner)
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ranking_comments (rank_type, kind, period_type, period, main_type, sub_type, owner, comment)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (rank_type, kind, period_type, period, main_type, sub_type, owner)
		DO UPDATE SET comment = EXCLUDED.comment, updated_at = now()`,
		key.Type, key.Kind, key.PeriodType, key.Period, key.MainType, key.SubType, owner, comment)
	if err != nil {
		return fmt.Errorf("save ranking comment: %w", err)
	}
	return nil
}

// PrideMasterAccount returns the username of a Pride's master ("" when the
// Pride does not exist).
func (s *PostgresStore) PrideMasterAccount(ctx context.Context, prideID uint32) (string, error) {
	var account string
	err := s.pool.QueryRow(ctx, `
		SELECT u.username FROM prides p JOIN users u ON u.id = p.master_user_id WHERE p.id = $1`,
		int64(prideID)).Scan(&account)
	if noRows(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load pride master: %w", err)
	}
	return account, nil
}
