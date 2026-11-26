package store

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type ChannelAccountState struct {
	SchoolID   uint32
	WolfPoints uint32
}

type RecommendationUser struct {
	UserID   uint32
	Username string
	Callsign string
	Level    byte
	Status   byte
}

type ChannelAccountStore interface {
	ChangeCallsign(context.Context, int, string) (string, bool, error)
	ChangeSchool(context.Context, int, uint32) (ChannelAccountState, bool, error)
	ChannelAccountState(context.Context, int) (ChannelAccountState, error)
	PendingRecommendations(context.Context, int) ([]RecommendationUser, error)
	AskRecommendation(context.Context, int, string) (int, RecommendationUser, RecommendationUser, bool, error)
	AnswerRecommendation(context.Context, int, string, bool) (int, RecommendationUser, RecommendationUser, bool, error)
}

func validCallsign(value string) bool {
	if value != strings.TrimSpace(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 32 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func (s *PostgresStore) ChangeCallsign(ctx context.Context, userID int, callsign string) (string, bool, error) {
	if userID < 1 || !validCallsign(callsign) {
		return "", false, fmt.Errorf("invalid callsign")
	}
	var effective string
	changed := false
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT callsign FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&effective); err != nil {
			return err
		}
		var conflict int
		err := tx.QueryRow(ctx, `SELECT id FROM users WHERE lower(callsign) = lower($1) AND id <> $2`, callsign, userID).Scan(&conflict)
		if err == nil {
			return nil
		}
		if !noRows(err) {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET callsign = $1 WHERE id = $2`, callsign, userID); err != nil {
			return err
		}
		effective, changed = callsign, true
		return nil
	})
	if err != nil {
		return "", false, fmt.Errorf("change callsign: %w", err)
	}
	return effective, changed, nil
}

func (s *PostgresStore) ChangeSchool(ctx context.Context, userID int, schoolID uint32) (ChannelAccountState, bool, error) {
	if userID < 1 || schoolID == ^uint32(0) {
		return ChannelAccountState{}, false, fmt.Errorf("invalid school change")
	}
	result, err := s.pool.Exec(ctx, `UPDATE users SET school_id = $1 WHERE id = $2`, int64(schoolID), userID)
	if err != nil {
		return ChannelAccountState{}, false, fmt.Errorf("change school: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ChannelAccountState{}, false, fmt.Errorf("change school: user %d not found", userID)
	}
	state, err := s.ChannelAccountState(ctx, userID)
	return state, err == nil, err
}

func (s *PostgresStore) ChannelAccountState(ctx context.Context, userID int) (ChannelAccountState, error) {
	var schoolID, wolfPoints int64
	err := s.pool.QueryRow(ctx, `SELECT school_id, wolf_points FROM users WHERE id = $1`, userID).Scan(&schoolID, &wolfPoints)
	if err != nil {
		return ChannelAccountState{}, fmt.Errorf("load channel account state: %w", err)
	}
	return ChannelAccountState{SchoolID: uint32(schoolID), WolfPoints: uint32(wolfPoints)}, nil
}

func (s *PostgresStore) PendingRecommendations(ctx context.Context, userID int) ([]RecommendationUser, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.username, u.callsign, u.level
		FROM recommendations r
		JOIN users u ON u.id = r.requester_user_id
		WHERE r.target_user_id = $1 AND r.status = 0
		ORDER BY r.created_at, r.requester_user_id
		LIMIT 255`, userID)
	if err != nil {
		return nil, fmt.Errorf("load pending recommendations: %w", err)
	}
	defer rows.Close()
	result := make([]RecommendationUser, 0, 16)
	for rows.Next() {
		user, err := scanRecommendationUser(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, user)
	}
	return result, rows.Err()
}

func (s *PostgresStore) AskRecommendation(ctx context.Context, requesterID int, targetCallsign string) (int, RecommendationUser, RecommendationUser, bool, error) {
	if requesterID < 1 || !validCallsign(targetCallsign) {
		return 0, RecommendationUser{}, RecommendationUser{}, false, fmt.Errorf("invalid recommendation request")
	}
	var targetID int
	var requester, target RecommendationUser
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		var err error
		requester, err = recommendationUserByID(ctx, tx, requesterID)
		if err != nil {
			return err
		}
		target, err = recommendationUserByCallsign(ctx, tx, targetCallsign)
		if err != nil {
			return err
		}
		targetID = int(target.UserID)
		if targetID == requesterID {
			return fmt.Errorf("cannot recommend self")
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO recommendations (requester_user_id, target_user_id, status, created_at, answered_at)
			VALUES ($1, $2, 0, now(), NULL)
			ON CONFLICT (requester_user_id, target_user_id) DO UPDATE
			SET status = 0, created_at = now(), answered_at = NULL`, requesterID, targetID)
		return err
	})
	if err != nil {
		return 0, RecommendationUser{}, RecommendationUser{}, false, fmt.Errorf("ask recommendation: %w", err)
	}
	return targetID, requester, target, true, nil
}

func (s *PostgresStore) AnswerRecommendation(ctx context.Context, responderID int, requesterCallsign string, accept bool) (int, RecommendationUser, RecommendationUser, bool, error) {
	if responderID < 1 || !validCallsign(requesterCallsign) {
		return 0, RecommendationUser{}, RecommendationUser{}, false, fmt.Errorf("invalid recommendation answer")
	}
	var requesterID int
	var requester, responder RecommendationUser
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		var err error
		responder, err = recommendationUserByID(ctx, tx, responderID)
		if err != nil {
			return err
		}
		requester, err = recommendationUserByCallsign(ctx, tx, requesterCallsign)
		if err != nil {
			return err
		}
		requesterID = int(requester.UserID)
		status := 2
		if accept {
			status = 1
		}
		result, err := tx.Exec(ctx, `
			UPDATE recommendations SET status = $1, answered_at = now()
			WHERE requester_user_id = $2 AND target_user_id = $3 AND status = 0`, status, requesterID, responderID)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return pgx.ErrNoRows
		}
		return nil
	})
	if err != nil {
		if noRows(err) {
			return requesterID, requester, responder, false, nil
		}
		return 0, RecommendationUser{}, RecommendationUser{}, false, fmt.Errorf("answer recommendation: %w", err)
	}
	return requesterID, requester, responder, true, nil
}

type recommendationScanner interface {
	Scan(...any) error
}

func scanRecommendationUser(scanner recommendationScanner) (RecommendationUser, error) {
	var id int64
	var level int
	var user RecommendationUser
	if err := scanner.Scan(&id, &user.Username, &user.Callsign, &level); err != nil {
		return RecommendationUser{}, err
	}
	user.UserID, user.Level, user.Status = uint32(id), byte(level), 3
	return user, nil
}

func recommendationUserByID(ctx context.Context, tx pgx.Tx, userID int) (RecommendationUser, error) {
	return scanRecommendationUser(tx.QueryRow(ctx, `SELECT id, username, callsign, level FROM users WHERE id = $1`, userID))
}

func recommendationUserByCallsign(ctx context.Context, tx pgx.Tx, callsign string) (RecommendationUser, error) {
	return scanRecommendationUser(tx.QueryRow(ctx, `SELECT id, username, callsign, level FROM users WHERE lower(callsign) = lower($1)`, callsign))
}
