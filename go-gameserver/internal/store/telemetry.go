package store

import (
	"context"
	"fmt"
)

const (
	TelemetryGameGuard = "gameguard"
	TelemetryXTrap     = "xtrap"
	TelemetryApex      = "apex"
)

type ClientTelemetry struct {
	UserID    int
	SessionID uint16
	Provider  string
	Payload   []byte
}

type ClientTelemetryStore interface {
	RecordClientTelemetry(context.Context, ClientTelemetry) error
}

func (s *PostgresStore) RecordClientTelemetry(ctx context.Context, record ClientTelemetry) error {
	if record.Provider != TelemetryGameGuard && record.Provider != TelemetryXTrap && record.Provider != TelemetryApex {
		return fmt.Errorf("invalid telemetry provider %q", record.Provider)
	}
	if len(record.Payload) > 65535 {
		return fmt.Errorf("telemetry payload exceeds 65535 bytes")
	}
	var userID any
	if record.UserID > 0 {
		userID = record.UserID
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO client_telemetry (user_id, session_id, provider, payload)
		VALUES ($1, $2, $3, $4)`, userID, int(record.SessionID), record.Provider, record.Payload)
	if err != nil {
		return fmt.Errorf("record client telemetry: %w", err)
	}
	return nil
}
