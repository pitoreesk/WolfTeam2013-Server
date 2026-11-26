package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrInvalidUsername = errors.New("username must be 3-12 ASCII letters, numbers, or underscores")
	ErrInvalidPassword = errors.New("password must be 4-64 bytes")
	ErrUsernameTaken   = errors.New("username is already registered")
	ErrUserNotFound    = errors.New("user is not registered")
)

// The callsign equals the username; 2012 client buffers for it (chat sender,
// GETUSERID, invite list) hold 12 UTF-16 units plus a terminator.
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,12}$`)

func validateAccount(username, password string) error {
	if !usernamePattern.MatchString(username) {
		return ErrInvalidUsername
	}
	if len(password) < 4 || len(password) > 64 {
		return ErrInvalidPassword
	}
	return nil
}

// Login authenticates credentials carried by launcher or game login messages.
// Invalid credentials are a normal rejection (-1, nil);
// database failures are returned as errors so callers can fail closed.
func (s *PostgresStore) Login(ctx context.Context, username, password string) (int, error) {
	if username == "" || password == "" {
		return -1, nil
	}
	var userID int
	err := s.pool.QueryRow(ctx, `
		UPDATE users
		SET last_login = now(),
		    banned = CASE
		        WHEN banned AND ban_expires_at IS NOT NULL AND ban_expires_at <= now() THEN false
		        ELSE banned
		    END,
		    ban_started_at = CASE
		        WHEN banned AND ban_expires_at IS NOT NULL AND ban_expires_at <= now() THEN NULL
		        ELSE ban_started_at
		    END,
		    ban_expires_at = CASE
		        WHEN banned AND ban_expires_at IS NOT NULL AND ban_expires_at <= now() THEN NULL
		        ELSE ban_expires_at
		    END,
		    ban_reason = CASE
		        WHEN banned AND ban_expires_at IS NOT NULL AND ban_expires_at <= now() THEN ''
		        ELSE ban_reason
		    END
		WHERE username = $1
		  AND password_hash = crypt($2, password_hash)
		  AND NOT (banned AND (ban_expires_at IS NULL OR ban_expires_at > now()))
		RETURNING id`, username, password).Scan(&userID)
	if noRows(err) {
		return -1, nil
	}
	if err != nil {
		return -1, fmt.Errorf("authenticate account login: %w", err)
	}
	return userID, nil
}

// Register creates an account and its complete starter loadout atomically. It
// is shared by trusted administrative tooling and the built-in web page.
func (s *PostgresStore) Register(ctx context.Context, username, password string) error {
	if err := validateAccount(username, password); err != nil {
		return err
	}
	err := s.withSerializable(ctx, func(tx pgx.Tx) error {
		var userID int
		if err := tx.QueryRow(ctx, `
			INSERT INTO users (username, password_hash, callsign, level, gp, gold, wc, cash)
			VALUES ($1, crypt($2, gen_salt('bf', 12)), $1, 61, 0, 100000, 100000, 100000)
			RETURNING id`, username, password).Scan(&userID); err != nil {
			return err
		}
		starterItems := [][2]int{
			{1001, 0}, {2001, 0}, {3001, 0}, {9001, 0},
			{30005, 1}, {30006, 1},
			{12078, 0}, {13006, 0}, {16011, 0}, {18004, 0}, {19001, 0},
			{19501, 0}, {19601, 0}, {20004, 0}, {21002, 0}, {14133, 0}, {17008, 0},
		}
		for _, item := range starterItems {
			if _, err := tx.Exec(ctx, `
				INSERT INTO inventory (user_id, item_id, type, upgrade, expiry)
				VALUES ($1, $2, $3, 0, 'permanent')`, userID, item[0], item[1]); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO character_loadout (user_id, red_char_id, blue_char_id)
			VALUES ($1, 30006, 30005)`, userID); err != nil {
			return err
		}
		for slot, characterID := range map[int]int{1: 30005, 2: 30006} {
			if _, err := tx.Exec(ctx, `INSERT INTO char_slots (user_id, slot, char_id) VALUES ($1, $2, $3)`, userID, slot, characterID); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		return nil
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return ErrUsernameTaken
	}
	return fmt.Errorf("register account: %w", err)
}

func (s *PostgresStore) ChangePassword(ctx context.Context, username, password string) error {
	if err := validateAccount(username, password); err != nil {
		return err
	}
	result, err := s.pool.Exec(ctx, `
		UPDATE users SET password_hash = crypt($2, gen_salt('bf', 12))
		WHERE username = $1`, username, password)
	if err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}
