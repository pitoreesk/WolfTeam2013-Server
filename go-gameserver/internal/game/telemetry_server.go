package game

import (
	"context"
	"fmt"

	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

func (s *Server) handleClientTelemetry(ctx context.Context, session *Session, frame transport.Frame) error {
	telemetry, ok := s.users.(store.ClientTelemetryStore)
	if !ok {
		return fmt.Errorf("configured user store does not support client telemetry")
	}
	_, _, userID, _ := session.identity()
	record := store.ClientTelemetry{UserID: userID, SessionID: session.SessionID()}
	switch frame.MessageID {
	case RequestGameGuard:
		record.Provider = store.TelemetryGameGuard
		record.Payload = append([]byte(nil), frame.Payload...)
		if err := telemetry.RecordClientTelemetry(ctx, record); err != nil {
			s.logBackendFallback("record GameGuard telemetry", userID, err)
		}
		return session.Write(ReplyGameGuard, GameGuardPayload())
	case RequestXTrap:
		record.Provider = store.TelemetryXTrap
		record.Payload = append([]byte(nil), frame.Payload...)
		if err := telemetry.RecordClientTelemetry(ctx, record); err != nil {
			s.logBackendFallback("record XTrap telemetry", userID, err)
		}
		return session.Write(ReplyXTrap, XTrapPayload())
	case RequestApexClientInfo:
		clientInfo, err := ParseApexClientInfo(frame.Payload)
		if err != nil {
			return err
		}
		record.Provider = store.TelemetryApex
		record.Payload = clientInfo.Data
		if err := telemetry.RecordClientTelemetry(ctx, record); err != nil {
			s.logBackendFallback("record Apex telemetry", userID, err)
		}
		// Record telemetry without claiming a verifier response when the
		// provider SDK is unavailable.
		return nil
	default:
		return fmt.Errorf("unsupported telemetry packet 0x%04x", frame.MessageID)
	}
}
