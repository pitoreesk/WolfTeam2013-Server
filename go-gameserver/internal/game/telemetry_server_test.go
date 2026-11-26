package game

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"

	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

type telemetryTestUsers struct {
	*fakeUsers
	store.ClientTelemetryStore
	records []store.ClientTelemetry
}

func (t *telemetryTestUsers) RecordClientTelemetry(_ context.Context, record store.ClientTelemetry) error {
	record.Payload = append([]byte(nil), record.Payload...)
	t.records = append(t.records, record)
	return nil
}

func TestClientTelemetryDispatchPersistsAndAcknowledges(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	defer serverNet.Close()
	defer clientNet.Close()
	_ = clientNet.SetDeadline(time.Now().Add(3 * time.Second))
	loopback := netip.MustParseAddr("127.0.0.1")
	users := &telemetryTestUsers{fakeUsers: &fakeUsers{}}
	server := NewServer("unused", 40707, 40708, loopback, loopback, users, slog.New(slog.NewTextHandler(io.Discard, nil)))
	session, err := newSession(transport.NewEncryptedConn(serverNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	session.setAuthenticated("user", 42, "Player", 7)
	client := transport.NewEncryptedConn(clientNet, nil)

	invoke := func(requestID, replyID uint16, payload []byte) transport.Frame {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			done <- server.handleMessage(context.Background(), session, transport.Frame{MessageID: requestID, Payload: payload})
		}()
		frame := assertFrameID(t, client, replyID)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		return frame
	}

	gameGuard := invoke(RequestGameGuard, ReplyGameGuard, []byte{0x10, 0x20})
	xtrap := invoke(RequestXTrap, ReplyXTrap, []byte{0x30})
	if err := server.handleMessage(context.Background(), session, transport.Frame{
		MessageID: RequestApexClientInfo,
		Payload:   []byte{1, 2, 0, 0, 0, 0xaa, 0xbb},
	}); err != nil {
		t.Fatal(err)
	}
	if !channelAllZero(gameGuard.Payload) || !channelAllZero(xtrap.Payload) {
		t.Fatalf("legacy ACKs = %x / %x", gameGuard.Payload, xtrap.Payload)
	}
	if len(users.records) != 3 || users.records[0].Provider != store.TelemetryGameGuard || users.records[1].Provider != store.TelemetryXTrap || users.records[2].Provider != store.TelemetryApex {
		t.Fatalf("telemetry records = %+v", users.records)
	}
	if users.records[0].UserID != 42 || users.records[0].SessionID != session.SessionID() || len(users.records[2].Payload) != 2 || users.records[2].Payload[1] != 0xbb {
		t.Fatalf("telemetry record contents = %+v", users.records)
	}
}
