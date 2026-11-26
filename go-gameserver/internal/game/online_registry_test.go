package game

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"

	"wtserver-go/internal/transport"
)

func trackTestConnections(server *Server, sessions ...*Session) {
	for _, session := range sessions {
		server.online.trackConnection(session)
	}
}

func TestOnlineRegistryLobbyReentryKeepsConnectionOrder(t *testing.T) {
	loopback := netip.MustParseAddr("127.0.0.1")
	server := NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	first := &Session{LoginName: "first", Callsign: "First", UserID: 1, RoomNumber: -1, RoomSlot: -1}
	second := &Session{LoginName: "second", Callsign: "Second", UserID: 2, RoomNumber: -1, RoomSlot: -1}
	third := &Session{LoginName: "third", Callsign: "Third", UserID: 3, RoomNumber: -1, RoomSlot: -1}
	server.addSession(first)
	server.addSession(second)
	server.addSession(third)

	first.mu.Lock()
	first.RoomNumber = 7
	first.mu.Unlock()
	server.refreshSessionLobby(first)
	if got := server.channelSessions(); len(got) != 2 || got[0] != second || got[1] != third {
		t.Fatalf("lobby after room entry = %p, want second then third", got)
	}

	first.mu.Lock()
	first.RoomNumber = -1
	first.mu.Unlock()
	server.refreshSessionLobby(first)
	if got := server.channelSessions(); len(got) != 3 || got[0] != first || got[1] != second || got[2] != third {
		t.Fatalf("lobby after room exit = %p, want original connection order", got)
	}

	server.removeSession(second)
	if got := server.online.connections.sessions(); len(got) != 2 || got[0] != first || got[1] != third {
		t.Fatalf("connections after removal = %p, want first then third", got)
	}
}

func TestOnlineRegistryPreservesLookupPrecedenceAndTransitions(t *testing.T) {
	loopback := netip.MustParseAddr("127.0.0.1")
	server := NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	oldest := &Session{LoginName: "old", Callsign: "Player", UserID: 42, RoomNumber: -1, RoomSlot: -1, UDPIP: loopback, UDPPort: 5000}
	newest := &Session{LoginName: "new", Callsign: "Player", UserID: 42, RoomNumber: -1, RoomSlot: -1, UDPIP: loopback, UDPPort: 5000}
	oldest.sessionID = 0x0801
	newest.sessionID = 0x0801
	server.addSession(oldest)
	server.addSession(newest)

	if got := server.sessionByUserID(42); got != newest {
		t.Fatalf("newest user lookup = %p, want %p", got, newest)
	}
	if got := server.sessionForUserID(42); got != oldest {
		t.Fatalf("oldest user lookup = %p, want %p", got, oldest)
	}
	if got := server.sessionBySessionID(0x0801); got != newest {
		t.Fatalf("colliding session-ID lookup = %p, want newest %p", got, newest)
	}
	if got := server.sessionByUDPEndpoint(loopback, 5000, true); got != oldest {
		t.Fatalf("UDP endpoint lookup = %p, want oldest %p", got, oldest)
	}

	server.setSessionCallsign(oldest, "Renamed")
	if got := server.sessionForCallsign("renamed"); got != oldest {
		t.Fatalf("renamed callsign lookup = %p, want %p", got, oldest)
	}
	if got := server.channelSessionByCallsign("PLAYER"); got != newest {
		t.Fatalf("remaining callsign lookup = %p, want %p", got, newest)
	}
	server.setSessionCallsign(oldest, "Player")
	if got := server.channelSessionByCallsign("player"); got != oldest {
		t.Fatalf("callsign lookup after reindex = %p, want oldest %p", got, oldest)
	}

	server.removeSession(newest)
	if got := server.sessionByUserID(42); got != oldest {
		t.Fatalf("user lookup after disconnect = %p, want %p", got, oldest)
	}
	if got := server.sessionBySessionID(0x0801); got != oldest {
		t.Fatalf("session-ID lookup after disconnect = %p, want %p", got, oldest)
	}
}

func TestOnlineRegistryUsesEqualFoldCompatibleCallsignKeys(t *testing.T) {
	loopback := netip.MustParseAddr("127.0.0.1")
	server := NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	session := &Session{LoginName: "user", Callsign: "KK", UserID: 42, RoomNumber: -1, RoomSlot: -1}
	server.addSession(session)
	if got := server.channelSessionByCallsign("kk"); got != session {
		t.Fatalf("Unicode-folded callsign lookup = %p, want %p", got, session)
	}
}

func TestSuccessfulLoginClosesAndDeindexesPreviousAccountSession(t *testing.T) {
	loopback := netip.MustParseAddr("127.0.0.1")
	server := NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	oldServer, oldClient := net.Pipe()
	defer oldServer.Close()
	defer oldClient.Close()
	newServer, newClient := net.Pipe()
	defer newServer.Close()
	defer newClient.Close()

	oldSession, err := newSession(transport.NewEncryptedConn(oldServer, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := newSession(transport.NewEncryptedConn(newServer, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	oldSession.setAuthenticated("user", 42, "Player", 7)
	server.addSession(oldSession)
	server.addSession(replacement)

	done := make(chan error, 1)
	go func() {
		done <- server.handleLogin(context.Background(), replacement,
			[]byte{0, 0, 0, 4, 'u', 's', 'e', 'r', 4, 'p', 'a', 's', 's'})
	}()
	client := transport.NewEncryptedConn(newClient, nil)
	frame := assertFrameID(t, client, ReplyChannelLogin)
	if frame.Payload[0] != 0 {
		t.Fatalf("replacement login status = %d", frame.Payload[0])
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if got := server.sessionByUserID(42); got != replacement {
		t.Fatalf("active account session = %p, want replacement %p", got, replacement)
	}
	_ = oldClient.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := oldClient.Read(make([]byte, 1)); err == nil {
		t.Fatal("superseded account connection remained open")
	}
}

func TestOnlineRegistryReplacesUDPEndpoint(t *testing.T) {
	loopback := netip.MustParseAddr("127.0.0.1")
	server := NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	session := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: loopback, UDPPort: 5000}
	server.addSession(session)

	session.mu.Lock()
	oldAddress, oldPort := session.UDPIP, session.UDPPort
	session.UDPPort = 6000
	session.mu.Unlock()
	server.mu.Lock()
	server.online.replaceUDPEndpoint(session, oldAddress, oldPort)
	server.mu.Unlock()

	if got := server.sessionByUDPEndpoint(loopback, 5000, true); got != nil {
		t.Fatalf("old UDP endpoint still resolves to %p", got)
	}
	if got := server.sessionByUDPEndpoint(loopback, 6000, true); got != session {
		t.Fatalf("new UDP endpoint resolves to %p, want %p", got, session)
	}
}
