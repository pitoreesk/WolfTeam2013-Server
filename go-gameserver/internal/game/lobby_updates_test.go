package game

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"

	"wtserver-go/internal/transport"
)

func TestLobbyUpdatesCoalesceBurstToLatestState(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	counted := &lobbyWriteCountingConn{Conn: serverNet}
	defer serverNet.Close()
	defer clientNet.Close()
	_ = clientNet.SetDeadline(time.Now().Add(3 * time.Second))

	loopback := netip.MustParseAddr("127.0.0.1")
	server := NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.lobbyUpdates.mu.Lock()
	server.lobbyUpdates.delay = time.Hour
	server.lobbyUpdates.mu.Unlock()

	target, err := newSession(transport.NewEncryptedConn(counted, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	target.setAuthenticated("target", 1, "Target", 7)
	server.addSession(target)
	client := transport.NewEncryptedConn(clientNet, nil)

	room := newRoom(3, RoomConfig{Title: "Burst", Capacity: 16})
	if _, ok := room.Add(&Session{RoomNumber: -1, RoomSlot: -1}); !ok {
		t.Fatal("add first room member")
	}
	server.mu.Lock()
	server.rooms[room.Number] = room
	server.mu.Unlock()
	server.broadcastFieldCreate(room)

	for index := 0; index < 2; index++ {
		if _, ok := room.Add(&Session{RoomNumber: -1, RoomSlot: -1}); !ok {
			t.Fatalf("add room member %d", index+2)
		}
		server.broadcastFieldUsers(room)
	}
	for range 25 {
		server.broadcastChannelRoster()
		server.broadcastJoinAllow()
	}

	framesResult := readLobbyFrames(client, 3)
	server.flushLobbyUpdates()
	frames := <-framesResult
	if frames.err != nil {
		t.Fatal(frames.err)
	}
	byID := make(map[uint16]transport.Frame, len(frames.frames))
	for _, frame := range frames.frames {
		if _, duplicate := byID[frame.MessageID]; duplicate {
			t.Fatalf("duplicate coalesced message %#x", frame.MessageID)
		}
		byID[frame.MessageID] = frame
	}
	create, ok := byID[ReplyFieldCreate]
	if !ok {
		t.Fatalf("coalesced messages = %v, missing field create", lobbyFrameIDs(frames.frames))
	}
	usersOffset := 21 + int(create.Payload[3])
	if usersOffset >= len(create.Payload) || create.Payload[usersOffset] != 3 {
		t.Fatalf("coalesced field-create user count = %x", create.Payload)
	}
	roster, ok := byID[ReplyChannelChars]
	if !ok || roster.Payload[2] != 1 {
		t.Fatalf("coalesced roster = %x", roster.Payload)
	}
	joinAllow, ok := byID[ReplyJoinAllow]
	if !ok || joinAllow.Payload[1] != 1 {
		t.Fatalf("coalesced join-allow = %x", joinAllow.Payload)
	}
	if counted.writes != 1 {
		t.Fatalf("first lobby flush used %d connection writes, want 1", counted.writes)
	}

	for index := 0; index < 2; index++ {
		if _, ok := room.Add(&Session{RoomNumber: -1, RoomSlot: -1}); !ok {
			t.Fatalf("add later room member %d", index+4)
		}
		for range 10 {
			server.broadcastFieldUsers(room)
		}
	}
	latestResult := readLobbyFrames(client, 1)
	server.flushLobbyUpdates()
	latest := <-latestResult
	if latest.err != nil {
		t.Fatal(latest.err)
	}
	if frame := latest.frames[0]; frame.MessageID != ReplyFieldUsers || frame.Payload[3] != 5 {
		t.Fatalf("latest room-user update = id %#x payload %x", frame.MessageID, frame.Payload)
	}
	if counted.writes != 2 {
		t.Fatalf("two lobby flushes used %d connection writes, want 2", counted.writes)
	}
}

func TestLobbyJoinAllowIsFilteredByRecipientClass(t *testing.T) {
	lowServer, lowNet := net.Pipe()
	highServer, highNet := net.Pipe()
	defer lowServer.Close()
	defer lowNet.Close()
	defer highServer.Close()
	defer highNet.Close()
	_ = lowNet.SetDeadline(time.Now().Add(3 * time.Second))
	_ = highNet.SetDeadline(time.Now().Add(3 * time.Second))

	loopback := netip.MustParseAddr("127.0.0.1")
	server := NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	low, err := newSession(transport.NewEncryptedConn(lowServer, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	low.setAuthenticated("low", 1, "Low", 5)
	high, err := newSession(transport.NewEncryptedConn(highServer, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	high.setAuthenticated("high", 2, "High", 15)
	server.addSession(low)
	server.addSession(high)

	room := newRoom(3, RoomConfig{
		Title: "Limited", Capacity: 8, ClassLimitMin: 10, ClassLimitMax: 20,
	})
	server.mu.Lock()
	server.rooms[room.Number] = room
	server.mu.Unlock()

	lowResult := readLobbyFrames(transport.NewEncryptedConn(lowNet, nil), 1)
	highResult := readLobbyFrames(transport.NewEncryptedConn(highNet, nil), 1)
	server.flushLobbyUpdateEvents([]lobbyUpdateEvent{{kind: lobbyUpdateJoinAllow}})
	lowFrames, highFrames := <-lowResult, <-highResult
	if lowFrames.err != nil {
		t.Fatal(lowFrames.err)
	}
	if highFrames.err != nil {
		t.Fatal(highFrames.err)
	}
	lowReply, highReply := lowFrames.frames[0], highFrames.frames[0]
	if lowReply.MessageID != ReplyJoinAllow || lowReply.Payload[1] != 0 {
		t.Fatalf("out-of-range join-allow = %x", lowReply.Payload)
	}
	if highReply.MessageID != ReplyJoinAllow || highReply.Payload[1] != 1 ||
		highReply.Payload[2] != byte(room.Number) || highReply.Payload[3] != 0 {
		t.Fatalf("in-range join-allow = %x", highReply.Payload)
	}
}

func TestLobbyStateUpdatesBatchLatestFieldAndOrderedGameTransitions(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	counted := &lobbyWriteCountingConn{Conn: serverNet}
	defer serverNet.Close()
	defer clientNet.Close()
	_ = clientNet.SetDeadline(time.Now().Add(3 * time.Second))

	loopback := netip.MustParseAddr("127.0.0.1")
	server := NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.lobbyUpdates.mu.Lock()
	server.lobbyUpdates.delay = time.Hour
	server.lobbyUpdates.mu.Unlock()
	target, err := newSession(transport.NewEncryptedConn(counted, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	target.setAuthenticated("target", 1, "Target", 7)
	server.addSession(target)
	client := transport.NewEncryptedConn(clientNet, nil)

	room := newRoom(7, RoomConfig{Title: "First", Capacity: 8})
	server.broadcastFieldState(room)
	room.UpdateConfig(RoomConfig{Title: "Latest", Capacity: 8})
	server.broadcastFieldState(room)
	server.broadcastGameState(room, true)
	server.broadcastGameState(room, false)

	framesResult := readLobbyFrames(client, 3)
	server.flushLobbyUpdates()
	frames := <-framesResult
	if frames.err != nil {
		t.Fatal(frames.err)
	}
	wantIDs := []uint16{ReplyFieldState, ReplyGameState, ReplyGameState}
	for index, frame := range frames.frames {
		if frame.MessageID != wantIDs[index] || frame.Sequence != uint16(index) {
			t.Fatalf("frame %d = id %#x sequence %d, want id %#x sequence %d", index, frame.MessageID, frame.Sequence, wantIDs[index], index)
		}
	}
	wantField, err := FieldStatePayload(room.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frames.frames[0].Payload[:len(wantField)], wantField) {
		t.Fatalf("field state = %x, want latest %x", frames.frames[0].Payload, wantField)
	}
	if frames.frames[1].Payload[3] != 1 || frames.frames[2].Payload[3] != 0 {
		t.Fatalf("game transitions = %x / %x", frames.frames[1].Payload, frames.frames[2].Payload)
	}
	if counted.writes != 1 {
		t.Fatalf("state flush used %d connection writes, want 1", counted.writes)
	}
}

func TestLobbyRoomExitDiscardsPendingRoomState(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	defer serverNet.Close()
	defer clientNet.Close()
	_ = clientNet.SetDeadline(time.Now().Add(3 * time.Second))

	loopback := netip.MustParseAddr("127.0.0.1")
	server := NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.lobbyUpdates.mu.Lock()
	server.lobbyUpdates.delay = time.Hour
	server.lobbyUpdates.mu.Unlock()
	target, err := newSession(transport.NewEncryptedConn(serverNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	target.setAuthenticated("target", 1, "Target", 7)
	server.addSession(target)
	client := transport.NewEncryptedConn(clientNet, nil)

	room := newRoom(9, RoomConfig{Title: "Closing", Capacity: 8})
	server.broadcastFieldUsers(room)
	server.broadcastFieldState(room)
	server.broadcastGameState(room, true)
	server.broadcastGameState(room, false)
	server.broadcastFieldExit(room)

	framesResult := readLobbyFrames(client, 1)
	server.flushLobbyUpdates()
	frames := <-framesResult
	if frames.err != nil {
		t.Fatal(frames.err)
	}
	if frame := frames.frames[0]; frame.MessageID != ReplyFieldExit || frame.Sequence != 0 {
		t.Fatalf("exit flush = id %#x sequence %d", frame.MessageID, frame.Sequence)
	}
	_ = clientNet.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := client.ReadFrame(); err == nil {
		t.Fatal("received an obsolete room-state frame after field exit")
	}
}

func TestLobbyUpdateStatsAreOptIn(t *testing.T) {
	loopback := netip.MustParseAddr("127.0.0.1")
	server := NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.flushLobbyUpdateEvents([]lobbyUpdateEvent{{kind: lobbyUpdateJoinAllow}})
	if stats := server.LobbyUpdateStats(); stats != (LobbyUpdateStats{}) {
		t.Fatalf("production-default lobby stats = %+v, want zero/disabled", stats)
	}

	server.EnableLobbyUpdateStats()
	server.flushLobbyUpdateEvents([]lobbyUpdateEvent{{kind: lobbyUpdateJoinAllow}})
	if stats := server.LobbyUpdateStats(); stats.Flushes != 1 || stats.Events != 1 {
		t.Fatalf("enabled lobby stats = %+v", stats)
	}
}

type lobbyFramesResult struct {
	frames []transport.Frame
	err    error
}

func readLobbyFrames(client *transport.EncryptedConn, count int) <-chan lobbyFramesResult {
	result := make(chan lobbyFramesResult, 1)
	go func() {
		frames := make([]transport.Frame, 0, count)
		for range count {
			frame, err := client.ReadFrame()
			if err != nil {
				result <- lobbyFramesResult{err: err}
				return
			}
			frames = append(frames, frame)
		}
		result <- lobbyFramesResult{frames: frames}
	}()
	return result
}

func lobbyFrameIDs(frames []transport.Frame) []uint16 {
	ids := make([]uint16, len(frames))
	for index, frame := range frames {
		ids[index] = frame.MessageID
	}
	return ids
}

type lobbyWriteCountingConn struct {
	net.Conn
	writes int
}

func (connection *lobbyWriteCountingConn) Write(data []byte) (int, error) {
	connection.writes++
	return connection.Conn.Write(data)
}
