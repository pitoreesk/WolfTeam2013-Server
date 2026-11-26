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

type buddyLookupUsers struct {
	*fakeUsers
	identity store.BuddyIdentity
	found    bool
}

func (u *buddyLookupUsers) BuddyIdentityByCallsign(context.Context, string) (store.BuddyIdentity, bool, error) {
	return u.identity, u.found, nil
}

func TestHandleGetUserID(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	defer serverNet.Close()
	defer clientNet.Close()
	_ = clientNet.SetDeadline(time.Now().Add(3 * time.Second))
	loopback := netip.MustParseAddr("127.0.0.1")
	users := &buddyLookupUsers{
		fakeUsers: &fakeUsers{},
		identity:  store.BuddyIdentity{Username: "target_user", Callsign: "Target"},
		found:     true,
	}
	server := NewServer("unused", 40707, 40708, loopback, loopback, users, slog.New(slog.NewTextHandler(io.Discard, nil)))
	session, err := newSession(transport.NewEncryptedConn(serverNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	session.setAuthenticated("requester", 42, "Requester", 7)

	done := make(chan error, 1)
	go func() {
		done <- server.handleMessage(context.Background(), session, transport.Frame{
			MessageID: RequestGetUserID,
			Payload:   []byte{12, 'T', 0, 'a', 0, 'r', 0, 'g', 0, 'e', 0, 't', 0},
		})
	}()
	frame := assertFrameID(t, transport.NewEncryptedConn(clientNet, nil), ReplyGetUserID)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	want, err := GetUserIDPayload("Target", "target_user")
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Payload) < len(want) || string(frame.Payload[:len(want)]) != string(want) {
		t.Fatalf("get-user-id reply = %x, want prefix %x", frame.Payload, want)
	}
}
