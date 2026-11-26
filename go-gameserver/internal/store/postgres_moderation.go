package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (s *PostgresStore) SetMutedByCallsign(
	ctx context.Context,
	callsign string,
	state ModerationState,
) (bool, error) {
	callsign = strings.TrimSpace(callsign)
	if callsign == "" {
		return false, nil
	}
	state = normalizeModerationState(state, s.now().UTC())
	result, err := s.pool.Exec(ctx, `
		UPDATE users
		SET muted = $2,
		    mute_started_at = $3,
		    mute_expires_at = $4,
		    mute_reason = $5
		WHERE lower(callsign) = lower($1)`,
		callsign, state.Active, nullableModerationTime(state.StartedAt),
		nullableModerationTime(state.ExpiresAt), state.Reason)
	if err != nil {
		return false, fmt.Errorf("set muted state for %q: %w", callsign, err)
	}
	return result.RowsAffected() != 0, nil
}

func (s *PostgresStore) SetBannedByCallsign(
	ctx context.Context,
	callsign string,
	state ModerationState,
) (bool, error) {
	callsign = strings.TrimSpace(callsign)
	if callsign == "" {
		return false, nil
	}
	state = normalizeModerationState(state, s.now().UTC())
	result, err := s.pool.Exec(ctx, `
		UPDATE users
		SET banned = $2,
		    ban_started_at = $3,
		    ban_expires_at = $4,
		    ban_reason = $5
		WHERE lower(callsign) = lower($1)`,
		callsign, state.Active, nullableModerationTime(state.StartedAt),
		nullableModerationTime(state.ExpiresAt), state.Reason)
	if err != nil {
		return false, fmt.Errorf("set banned state for %q: %w", callsign, err)
	}
	return result.RowsAffected() != 0, nil
}

func normalizeModerationState(state ModerationState, now time.Time) ModerationState {
	if !state.Active {
		return ModerationState{}
	}
	state.StartedAt = state.StartedAt.UTC()
	state.ExpiresAt = state.ExpiresAt.UTC()
	state.Reason = strings.TrimSpace(state.Reason)
	if state.StartedAt.IsZero() {
		state.StartedAt = now
	}
	if !state.ExpiresAt.IsZero() && !state.StartedAt.Before(state.ExpiresAt) {
		return ModerationState{}
	}
	return state
}

func nullableModerationTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
