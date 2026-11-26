package broker

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

type fakeChannels struct{ channels []store.Channel }

func (f fakeChannels) Channels(context.Context) ([]store.Channel, error) { return f.channels, nil }

type localAddressConn struct {
	net.Conn
	local net.Addr
}

func (c localAddressConn) LocalAddr() net.Addr { return c.local }

func TestEncryptedChainListExchange(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer clientSide.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(
		"unused",
		netip.MustParseAddr("127.0.0.1"),
		40708,
		fakeChannels{},
		logger,
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.handleConnection(ctx, serverSide) }()

	_ = clientSide.SetDeadline(time.Now().Add(2 * time.Second))
	client := transport.NewEncryptedConn(clientSide, nil)
	if _, err := client.WriteMessage(MessageChainListRequest, nil); err != nil {
		t.Fatal(err)
	}
	frame, err := client.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if frame.MessageID != MessageChainListReply {
		t.Fatalf("response ID = %#x", frame.MessageID)
	}
	if got := frame.Payload; got[0] != 0 || got[1] != 0x0e {
		t.Fatalf("response payload = %x", got)
	}

	_ = clientSide.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("connection handler: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("connection handler did not stop")
	}
}

func TestWorldListUsesConfiguredAddressInsideDocker(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer clientSide.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(
		"unused",
		netip.MustParseAddr("127.0.0.1"),
		40708,
		fakeChannels{channels: []store.Channel{{Name: "New Players", Port: 40707, Open: true}}},
		logger,
	)
	wrappedServerSide := localAddressConn{
		Conn:  serverSide,
		local: &net.TCPAddr{IP: net.ParseIP("172.21.0.3"), Port: 40706},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.handleConnection(ctx, wrappedServerSide) }()

	_ = clientSide.SetDeadline(time.Now().Add(2 * time.Second))
	client := transport.NewEncryptedConn(clientSide, nil)
	if _, err := client.WriteMessage(MessageWorldListRequest, nil); err != nil {
		t.Fatal(err)
	}
	frame, err := client.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if frame.MessageID != MessageWorldListReply {
		t.Fatalf("response ID = %#x", frame.MessageID)
	}
	if got := frame.Payload[2:6]; string(got) != string([]byte{127, 0, 0, 1}) {
		t.Fatalf("advertised IP = %v, want 127.0.0.1", got)
	}

	_ = clientSide.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("connection handler: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("connection handler did not stop")
	}
}
