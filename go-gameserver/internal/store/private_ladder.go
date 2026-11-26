package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type PrivateLadderInfo struct {
	Rank   uint32
	Points uint32
}

type PrivateLadderRank struct {
	Username    string
	Callsign    string
	Level       byte
	PrideMarkID uint32
	Rank        uint32
	Points      uint32
	Wins        uint32
	Status      byte
}

type PrivateLadderNotice struct {
	Title    string
	StartsOn *time.Time
	EndsOn   *time.Time
	Summary  string
	Details  string
}

type PrivateLadderStore interface {
	PrivateLadderInfo(context.Context, int) (PrivateLadderInfo, error)
	PrivateLadderRanks(context.Context, int, int) ([]PrivateLadderRank, error)
	PrivateLadderNotice(context.Context, time.Time) (PrivateLadderNotice, error)
	RegisterPrivateLadderMatch(context.Context, []int, []int) error
}

func (s *PostgresStore) PrivateLadderInfo(ctx context.Context, userID int) (PrivateLadderInfo, error) {
	if userID < 1 {
		return PrivateLadderInfo{}, fmt.Errorf("invalid private-ladder user")
	}
	var info PrivateLadderInfo
	err := s.pool.QueryRow(ctx, `
		SELECT ranked.position, u.ladder_points
		FROM (
			SELECT id,
			       row_number() OVER (ORDER BY ladder_points DESC, wins DESC, id) AS position
			FROM users
			WHERE username NOT LIKE '%_system'
		) AS ranked
		JOIN users u ON u.id = ranked.id
		WHERE ranked.id = $1`, userID).Scan(&info.Rank, &info.Points)
	if noRows(err) {
		return PrivateLadderInfo{}, nil
	}
	if err != nil {
		return PrivateLadderInfo{}, fmt.Errorf("load private-ladder info: %w", err)
	}
	return info, nil
}

func (s *PostgresStore) PrivateLadderRanks(ctx context.Context, offset, limit int) ([]PrivateLadderRank, error) {
	if offset < 0 || limit < 0 || limit > 255 {
		return nil, fmt.Errorf("invalid private-ladder rank window")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT u.username, u.callsign, u.level, ranked.position, u.ladder_points, u.wins,
		       COALESCE(p.id, 0) AS pride_mark_id
		FROM (
			SELECT id,
			       row_number() OVER (ORDER BY ladder_points DESC, wins DESC, id) AS position
			FROM users
			WHERE username NOT LIKE '%_system'
		) AS ranked
		JOIN users u ON u.id = ranked.id
		LEFT JOIN pride_members pm ON pm.user_id = u.id
		LEFT JOIN prides p ON p.id = pm.pride_id
		ORDER BY ranked.position
		OFFSET $1 LIMIT $2`, offset, limit)
	if err != nil {
		return nil, fmt.Errorf("load private-ladder ranks: %w", err)
	}
	defer rows.Close()
	result := make([]PrivateLadderRank, 0, limit)
	for rows.Next() {
		var rank PrivateLadderRank
		var level int
		if err := rows.Scan(&rank.Username, &rank.Callsign, &level, &rank.Rank, &rank.Points, &rank.Wins, &rank.PrideMarkID); err != nil {
			return nil, fmt.Errorf("scan private-ladder rank: %w", err)
		}
		if level < 0 {
			level = 0
		} else if level > 255 {
			level = 255
		}
		rank.Level = byte(level)
		result = append(result, rank)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate private-ladder ranks: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) PrivateLadderNotice(ctx context.Context, now time.Time) (PrivateLadderNotice, error) {
	var notice PrivateLadderNotice
	var startsOn, endsOn *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT title, starts_on, ends_on, summary, details
		FROM private_ladder_notices
		WHERE enabled
		  AND (starts_on IS NULL OR starts_on <= $1::date)
		  AND (ends_on IS NULL OR ends_on >= $1::date)
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, now.UTC()).Scan(&notice.Title, &startsOn, &endsOn, &notice.Summary, &notice.Details)
	if err == pgx.ErrNoRows {
		return PrivateLadderNotice{}, nil
	}
	if err != nil {
		return PrivateLadderNotice{}, fmt.Errorf("load private-ladder notice: %w", err)
	}
	notice.StartsOn, notice.EndsOn = startsOn, endsOn
	return notice, nil
}

func (s *PostgresStore) RegisterPrivateLadderMatch(ctx context.Context, winners, losers []int) error {
	if len(winners) == 0 || len(losers) == 0 {
		return fmt.Errorf("private-ladder match requires both teams")
	}
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		winnerResult, err := tx.Exec(ctx, `
			UPDATE users SET ladder_points = ladder_points + 10
			WHERE id = ANY($1)`, winners)
		if err != nil {
			return err
		}
		if winnerResult.RowsAffected() != int64(len(winners)) {
			return fmt.Errorf("one or more private-ladder winners do not exist")
		}
		loserResult, err := tx.Exec(ctx, `
			UPDATE users SET ladder_points = greatest(0, ladder_points - 5)
			WHERE id = ANY($1)`, losers)
		if err != nil {
			return err
		}
		if loserResult.RowsAffected() != int64(len(losers)) {
			return fmt.Errorf("one or more private-ladder losers do not exist")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("register private-ladder match: %w", err)
	}
	return nil
}

var _ PrivateLadderStore = (*PostgresStore)(nil)
