package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// PrideBattleRecord is one finished Pride Battle as the game server saw it.
type PrideBattleRecord struct {
	WinnerUserIDs []int
	LoserUserIDs  []int
	Mode          int
	Map           int
	WinnerScore   int // rounds or points of the winning side
	LoserScore    int
	Members       []PrideBattleMemberEntry
}

// PrideBattleRecorder saves a Pride Battle with its details (mode, map,
// scores, members and aces) and moves the rank points by the rank gap.
type PrideBattleRecorder interface {
	RecordPrideBattle(ctx context.Context, record PrideBattleRecord) error
}

// PrideBattlePoints is the rank-point change of a Pride Battle (user choice,
// 2026-09-25): 10 points, adjusted by half the rank gap and clamped to 5..15.
// An upset (the lower-ranked Pride wins) earns more, beating a weaker Pride
// earns less. The loser loses the same amount. Rank 0 means unranked and is
// treated as one below the last ranked Pride.
func PrideBattlePoints(winnerRank, loserRank int) int {
	gap := (winnerRank - loserRank) / 2
	gap = max(min(gap, 5), -5)
	return 10 + gap
}

// PrideAce picks the ace (hero) of one side: the highest score, then most
// kills, then fewest deaths.
func PrideAce(members []PrideBattleMemberEntry) int {
	best := -1
	for index, member := range members {
		if best < 0 {
			best = index
			continue
		}
		current := members[best]
		switch {
		case member.Score != current.Score:
			if member.Score > current.Score {
				best = index
			}
		case member.Kills != current.Kills:
			if member.Kills > current.Kills {
				best = index
			}
		case member.Deaths < current.Deaths:
			best = index
		}
	}
	return best
}

func (s *PostgresStore) RecordPrideBattle(ctx context.Context, record PrideBattleRecord) error {
	winnerPride, ok, err := s.singlePrideForUsers(ctx, record.WinnerUserIDs)
	if err != nil || !ok {
		return err
	}
	loserPride, ok, err := s.singlePrideForUsers(ctx, record.LoserUserIDs)
	if err != nil || !ok || winnerPride == loserPride {
		return err
	}
	winners, losers := splitPrideBattleMembers(record)
	return s.withSerializable(ctx, func(tx pgx.Tx) error {
		winnerRank, err := prideRankTx(ctx, tx, winnerPride)
		if err != nil {
			return err
		}
		loserRank, err := prideRankTx(ctx, tx, loserPride)
		if err != nil {
			return err
		}
		points := PrideBattlePoints(winnerRank, loserRank)
		var winnerBattle, loserBattle uint64
		if err := tx.QueryRow(ctx, `
			INSERT INTO pride_battles (pride_id, opponent_pride_id, won, rank_delta, mode, map, score_a, score_b, earn_points)
			VALUES ($1, $2, true, $3, $4, $5, $6, $7, $8) RETURNING id`,
			winnerPride, loserPride, points, record.Mode, record.Map, record.WinnerScore, record.LoserScore, points).Scan(&winnerBattle); err != nil {
			return fmt.Errorf("insert pride battle (winner): %w", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO pride_battles (pride_id, opponent_pride_id, won, rank_delta, mode, map, score_a, score_b, earn_points)
			VALUES ($1, $2, false, $3, $4, $5, $6, $7, $8) RETURNING id`,
			loserPride, winnerPride, -points, record.Mode, record.Map, record.LoserScore, record.WinnerScore, -points).Scan(&loserBattle); err != nil {
			return fmt.Errorf("insert pride battle (loser): %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE prides SET wins = wins + 1, rank_points = rank_points + $2 WHERE id = $1`, winnerPride, points); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE prides SET losses = losses + 1, rank_points = GREATEST(0, rank_points - $2) WHERE id = $1`, loserPride, points); err != nil {
			return err
		}
		for _, side := range []struct {
			battle  uint64
			pride   uint32
			members []PrideBattleMemberEntry
			won     bool
		}{{winnerBattle, winnerPride, winners, true}, {loserBattle, loserPride, losers, false}} {
			ace := PrideAce(side.members)
			for index, member := range side.members {
				if _, err := tx.Exec(ctx, `
					INSERT INTO pride_battle_members (battle_id, user_id, kills, deaths, score, is_ace)
					VALUES ($1, $2, $3, $4, $5, $6)`,
					side.battle, member.UserID, member.Kills, member.Deaths, member.Score, index == ace); err != nil {
					return fmt.Errorf("insert pride battle member: %w", err)
				}
				// Contribution: 1 point for playing, the kills on top, +2 for a win.
				contribution := 1 + member.Kills
				if side.won {
					contribution += 2
				}
				if _, err := tx.Exec(ctx, `UPDATE pride_members SET contribution = contribution + $3 WHERE pride_id = $1 AND user_id = $2`,
					side.pride, member.UserID, contribution); err != nil {
					return err
				}
			}
		}
		return refreshPrideRanksTx(ctx, tx, s.now())
	})
}

// splitPrideBattleMembers keeps each side's members (only players of that
// side's Pride roster are credited).
func splitPrideBattleMembers(record PrideBattleRecord) (winners, losers []PrideBattleMemberEntry) {
	winnerSet := make(map[int]bool, len(record.WinnerUserIDs))
	for _, id := range record.WinnerUserIDs {
		winnerSet[id] = true
	}
	loserSet := make(map[int]bool, len(record.LoserUserIDs))
	for _, id := range record.LoserUserIDs {
		loserSet[id] = true
	}
	for _, member := range record.Members {
		switch {
		case member.UserID < 1:
		case winnerSet[member.UserID]:
			winners = append(winners, member)
		case loserSet[member.UserID]:
			losers = append(losers, member)
		}
	}
	return winners, losers
}

// prideRankOrder is the Pride ranking: rank points, then wins, then age.
const prideRankOrder = `rank_points DESC, wins DESC, id`

// prideRankTx returns a Pride's current rank (1 = best). A Pride without rank
// points ranks one below the last ranked Pride.
func prideRankTx(ctx context.Context, tx pgx.Tx, prideID uint32) (int, error) {
	var rank int
	err := tx.QueryRow(ctx, `
		SELECT rank FROM (
			SELECT id, row_number() OVER (ORDER BY `+prideRankOrder+`) AS rank
			FROM prides
		) ranked WHERE id = $1`, prideID).Scan(&rank)
	if noRows(err) {
		return 0, nil
	}
	return rank, err
}

// refreshPrideRanksTx records today's rank of every ranked Pride and lowers
// best_rank where a Pride reached a better place.
func refreshPrideRanksTx(ctx context.Context, tx pgx.Tx, now time.Time) error {
	if _, err := tx.Exec(ctx, `
		WITH ranked AS (
			SELECT id, rank_points, row_number() OVER (ORDER BY `+prideRankOrder+`) AS rank
			FROM prides WHERE rank_points > 0
		)
		INSERT INTO pride_rank_snapshots (pride_id, day, rank, points)
		SELECT id, $1::date, rank, rank_points FROM ranked
		ON CONFLICT (pride_id, day) DO UPDATE SET rank = EXCLUDED.rank, points = EXCLUDED.points`,
		now.Format("2006-01-02")); err != nil {
		return fmt.Errorf("record pride rank snapshots: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE prides p SET best_rank = r.rank
		FROM (
			SELECT id, row_number() OVER (ORDER BY `+prideRankOrder+`) AS rank
			FROM prides WHERE rank_points > 0
		) r
		WHERE p.id = r.id AND (p.best_rank = 0 OR r.rank < p.best_rank)`); err != nil {
		return fmt.Errorf("update pride best ranks: %w", err)
	}
	return nil
}

// PrideRecentRanks returns a Pride's rank on each of the last `days` days,
// oldest first (the RECENTLYRANK graph); a day without a snapshot is 0.
func (s *PostgresStore) PrideRecentRanks(ctx context.Context, prideID uint32, days int) ([]int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT (current_date - day)::integer, rank FROM pride_rank_snapshots
		WHERE pride_id = $1 AND day > current_date - $2::integer`, prideID, days)
	if err != nil {
		return nil, fmt.Errorf("load pride rank snapshots: %w", err)
	}
	defer rows.Close()
	ranks := make([]int, days)
	for rows.Next() {
		var age, rank int
		if err := rows.Scan(&age, &rank); err != nil {
			return nil, err
		}
		if age >= 0 && age < days {
			ranks[days-1-age] = rank
		}
	}
	return ranks, rows.Err()
}
