package game

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"

	"wtserver-go/internal/transport"
)

func newChannelSheetTestServer() *Server {
	loopback := netip.MustParseAddr("127.0.0.1")
	return NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

type channelSheetTestClient struct {
	session    *Session
	wire       *transport.EncryptedConn
	connection net.Conn
}

func addChannelSheetTestClient(t *testing.T, server *Server, loginName, callsign string, userID int) channelSheetTestClient {
	t.Helper()
	serverNet, clientNet := net.Pipe()
	t.Cleanup(func() {
		_ = serverNet.Close()
		_ = clientNet.Close()
	})
	_ = clientNet.SetDeadline(time.Now().Add(3 * time.Second))

	loopback := netip.MustParseAddr("127.0.0.1")
	session, err := newSession(transport.NewEncryptedConn(serverNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	session.setAuthenticated(loginName, userID, callsign, 7)
	server.addSession(session)
	return channelSheetTestClient{
		session:    session,
		wire:       transport.NewEncryptedConn(clientNet, nil),
		connection: clientNet,
	}
}

func TestSessionAuthorityPropagatesToChannelAndRoomRecords(t *testing.T) {
	server := newChannelSheetTestServer()
	session := &Session{RoomNumber: 0, RoomSlot: 1, RoomTeam: redTeam}
	session.setAuthenticatedWithAuthority("user", 1, "User", 7, 13)

	channel := channelUserRecord(session)
	room := server.rosterMember(session, rosterState{})
	if channel.Authority != 13 || room.Authority != 13 {
		t.Fatalf("authority propagation = channel %d room %d, want 13", channel.Authority, room.Authority)
	}
}

func TestPrideDisplayPropagatesToChannelAndRoomRecords(t *testing.T) {
	server := newChannelSheetTestServer()
	session := &Session{RoomNumber: 0, RoomSlot: 1, RoomTeam: redTeam}
	session.setAuthenticated("user", 1, "User", 7)
	session.setPrideDisplay("Wolves", 0)

	channel := channelUserRecord(session)
	room := server.rosterMember(session, rosterState{})
	if channel.PrideName != "Wolves" || room.PrideName != "Wolves" {
		t.Fatalf("Pride display propagation = channel %q room %q, want Wolves", channel.PrideName, room.PrideName)
	}
	if channel.PrideMarkID != 0 || room.PrideMarkID != 0 {
		t.Fatalf("unsupported Pride mark was emitted: channel %d room %d", channel.PrideMarkID, room.PrideMarkID)
	}
}

func TestChannelSheetsPartitionRosterAtOneByteCapacity(t *testing.T) {
	server := newChannelSheetTestServer()
	sessions := make([]*Session, channelSheetCapacity+1)
	for index := range sessions {
		session := &Session{
			LoginName:  fmt.Sprintf("user%d", index),
			Callsign:   fmt.Sprintf("User%d", index),
			UserID:     index + 1,
			UserLevel:  byte(index),
			RoomNumber: -1,
		}
		session.sessionID = uint16(index + 1)
		server.addSession(session)
		sessions[index] = session
	}

	if sessions[0].channelSheet != 0 || sessions[0].channelMemberID != 0 {
		t.Fatalf("first channel member = sheet %d id %d", sessions[0].channelSheet, sessions[0].channelMemberID)
	}
	if sessions[channelSheetCapacity-1].channelSheet != 0 || sessions[channelSheetCapacity-1].channelMemberID != channelSheetCapacity-1 {
		t.Fatalf("last first-sheet member = sheet %d id %d", sessions[channelSheetCapacity-1].channelSheet, sessions[channelSheetCapacity-1].channelMemberID)
	}
	if sessions[channelSheetCapacity].channelSheet != 1 || sessions[channelSheetCapacity].channelMemberID != 0 {
		t.Fatalf("overflow member = sheet %d id %d", sessions[channelSheetCapacity].channelSheet, sessions[channelSheetCapacity].channelMemberID)
	}

	messages, err := server.channelRosterMessages([]*Session{sessions[0], sessions[channelSheetCapacity]})
	if err != nil {
		t.Fatal(err)
	}
	firstSheet := messages[sessions[0]]
	if len(firstSheet) < 2 || firstSheet[0].MessageID != ReplyChannelChars {
		t.Fatalf("sheet-zero multipart roster = count %d, first %#x/%x", len(firstSheet), firstSheet[0].MessageID, firstSheet[0].Payload)
	}
	totalUsers := 0
	for index, message := range firstSheet {
		wantID := ReplyChannelCharsMore
		if index == 0 {
			wantID = ReplyChannelChars
		}
		if message.MessageID != wantID || len(message.Payload) > transport.MaxEncryptedPayload ||
			message.Payload[0] != 0 || message.Payload[1] != 0 || message.Payload[2] == 0 {
			t.Fatalf("sheet-zero chunk %d = %#x/%d bytes/%x", index, message.MessageID, len(message.Payload), message.Payload[:3])
		}
		totalUsers += int(message.Payload[2])
	}
	if totalUsers != channelSheetCapacity {
		t.Fatalf("sheet-zero chunk users = %d, want %d", totalUsers, channelSheetCapacity)
	}
	secondSheet := messages[sessions[channelSheetCapacity]]
	if len(secondSheet) != 1 || secondSheet[0].MessageID != ReplyChannelChars ||
		secondSheet[0].Payload[1] != 1 || secondSheet[0].Payload[2] != 1 {
		t.Fatalf("sheet-one roster = %+v", secondSheet)
	}
}

func TestChannelMemberIDIsStableUntilSheetMove(t *testing.T) {
	server := newChannelSheetTestServer()
	session := &Session{LoginName: "user", Callsign: "User", UserID: 1, RoomNumber: -1}
	server.addSession(session)
	originalID := session.channelMemberID

	server.setSessionCallsign(session, "Renamed")
	if !session.channelMemberAssigned || session.channelSheet != 0 || session.channelMemberID != originalID {
		t.Fatalf("member changed during identity refresh: assigned=%v sheet=%d id=%d",
			session.channelMemberAssigned, session.channelSheet, session.channelMemberID)
	}

	selected, changed, err := server.moveChannelMember(session, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || selected != 2 || session.channelSheet != 2 || session.channelMemberID != 0 {
		t.Fatalf("moved member = selected %d changed %v sheet %d id %d",
			selected, changed, session.channelSheet, session.channelMemberID)
	}
	if server.channelMembers[0][originalID] != nil || server.channelMembers[2][0] != session {
		t.Fatal("channel member allocation table was not moved atomically")
	}
}

func TestChannelMemberIDIsReleasedWhileSessionIsInRoom(t *testing.T) {
	server := newChannelSheetTestServer()
	first := &Session{LoginName: "first", Callsign: "First", UserID: 1, RoomNumber: -1}
	server.addSession(first)
	if first.channelMemberID != 0 {
		t.Fatalf("initial member ID = %d", first.channelMemberID)
	}

	first.mu.Lock()
	first.RoomNumber = 7
	first.mu.Unlock()
	server.refreshSessionLobby(first)
	if first.channelMemberAssigned || server.channelMembers[0][0] != nil {
		t.Fatal("room member retained a channel member ID")
	}

	second := &Session{LoginName: "second", Callsign: "Second", UserID: 2, RoomNumber: -1}
	server.addSession(second)
	if second.channelSheet != 0 || second.channelMemberID != 0 {
		t.Fatalf("released member ID was not reused: sheet=%d id=%d", second.channelSheet, second.channelMemberID)
	}

	first.mu.Lock()
	first.RoomNumber = -1
	first.mu.Unlock()
	server.refreshSessionLobby(first)
	if !first.channelMemberAssigned || first.channelSheet != 0 || first.channelMemberID != 1 {
		t.Fatalf("returning member = assigned %v sheet %d id %d", first.channelMemberAssigned, first.channelSheet, first.channelMemberID)
	}
}

func TestConnectionSessionIDsUseFullUint16Space(t *testing.T) {
	server := newChannelSheetTestServer()
	server.nextSessionID = 0x08df
	first := &Session{RoomNumber: -1}
	second := &Session{RoomNumber: -1}
	if err := server.addConnectionSession(first); err != nil {
		t.Fatal(err)
	}
	if err := server.addConnectionSession(second); err != nil {
		t.Fatal(err)
	}
	if first.SessionID() != 0x08df || second.SessionID() != 0x0900 {
		t.Fatalf("allocated session IDs = %#04x, %#04x", first.SessionID(), second.SessionID())
	}
	if server.online.bySessionID[first.SessionID()].oldest() != first || server.online.bySessionID[second.SessionID()].oldest() != second {
		t.Fatal("allocated session IDs were not indexed independently")
	}
}

func TestChangeSheetEchoesUISelectionWithoutChannelMove(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	defer serverNet.Close()
	defer clientNet.Close()
	_ = clientNet.SetDeadline(time.Now().Add(3 * time.Second))

	server := newChannelSheetTestServer()
	loopback := netip.MustParseAddr("127.0.0.1")
	session, err := newSession(transport.NewEncryptedConn(serverNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	session.setAuthenticated("user", 1, "User", 7)
	server.addSession(session)
	client := transport.NewEncryptedConn(clientNet, nil)
	originalSheet := session.channelSheet
	originalMemberID := session.channelMemberID

	done := make(chan error, 1)
	go func() {
		done <- server.handleMessage(context.Background(), session, transport.Frame{
			MessageID: RequestChangeSheet,
			Payload:   []byte{5},
		})
	}()
	change := assertFrameID(t, client, ReplyChangeSheet)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if change.Payload[1] != 5 {
		t.Fatalf("change-sheet acknowledgement = %x", change.Payload)
	}
	if !session.channelMemberAssigned || session.channelSheet != originalSheet || session.channelMemberID != originalMemberID {
		t.Fatalf("UI sheet selection changed channel membership: assigned=%t sheet=%d member=%d",
			session.channelMemberAssigned, session.channelSheet, session.channelMemberID)
	}

	_ = clientNet.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if frame, err := client.ReadFrame(); err == nil {
		t.Fatalf("UI sheet selection sent unexpected roster frame %#x/%x", frame.MessageID, frame.Payload)
	}
}

func TestChangeSheetInRoomEchoesSelectionWithoutLobbyMove(t *testing.T) {
	server := newChannelSheetTestServer()
	client := addChannelSheetTestClient(t, server, "user", "User", 1)
	client.session.mu.Lock()
	client.session.RoomNumber = 7
	client.session.mu.Unlock()
	server.refreshSessionLobby(client.session)

	done := make(chan error, 1)
	go func() {
		done <- server.handleMessage(context.Background(), client.session, transport.Frame{
			MessageID: RequestChangeSheet,
			Payload:   []byte{3},
		})
	}()
	change := assertFrameID(t, client.wire, ReplyChangeSheet)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if change.Payload[1] != 3 {
		t.Fatalf("in-room change-sheet acknowledgement = %x", change.Payload)
	}
	if client.session.channelMemberAssigned {
		t.Fatal("in-room sheet selection reassigned lobby membership")
	}

	_ = client.connection.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if frame, err := client.wire.ReadFrame(); err == nil {
		t.Fatalf("in-room sheet selection sent unexpected roster frame %#x/%x", frame.MessageID, frame.Payload)
	}
}

func TestChangeSheetReplaysDeferredRoomExitBeforeLobbyTransition(t *testing.T) {
	server := newChannelSheetTestServer()
	client := addChannelSheetTestClient(t, server, "user", "User", 1)
	client.session.deferClientRoomExit(7)

	done := make(chan error, 1)
	go func() {
		done <- server.handleMessage(context.Background(), client.session, transport.Frame{
			MessageID: RequestChangeSheet,
			Payload:   []byte{0},
		})
	}()
	change := assertFrameID(t, client.wire, ReplyChangeSheet)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if change.Payload[0] != 0 || change.Payload[1] != 0 {
		t.Fatalf("pride-sheet acknowledgement = %x", change.Payload)
	}
	if slot, pending := client.session.pendingClientRoomDeparture(); !pending || slot != 7 {
		t.Fatalf("opening Pride consumed deferred room exit: slot=%d pending=%t", slot, pending)
	}

	go func() {
		done <- server.handleMessage(context.Background(), client.session, transport.Frame{
			MessageID: RequestChangeSheet,
			Payload:   []byte{1},
		})
	}()
	exit := assertFrameID(t, client.wire, ReplyExitRoom)
	change = assertFrameID(t, client.wire, ReplyChangeSheet)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if exit.Payload[0] != 0 || exit.Payload[1] != 7 {
		t.Fatalf("deferred exit acknowledgement = %x", exit.Payload)
	}
	if change.Payload[0] != 0 || change.Payload[1] != 1 {
		t.Fatalf("lobby-sheet acknowledgement = %x", change.Payload)
	}
	if slot, pending := client.session.pendingClientRoomDeparture(); pending {
		t.Fatalf("deferred room exit survived successful replay: slot=%d", slot)
	}

	go func() {
		done <- server.handleMessage(context.Background(), client.session, transport.Frame{
			MessageID: RequestChangeSheet,
			Payload:   []byte{1},
		})
	}()
	change = assertFrameID(t, client.wire, ReplyChangeSheet)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if change.Payload[0] != 0 || change.Payload[1] != 1 {
		t.Fatalf("repeated lobby-sheet acknowledgement = %x", change.Payload)
	}
}

func TestChannelEnterSendsSnapshotToEntrantAndDeltaToPeers(t *testing.T) {
	server := newChannelSheetTestServer()
	entrant := addChannelSheetTestClient(t, server, "entrant", "Entrant", 1)
	peer := addChannelSheetTestClient(t, server, "peer", "Peer", 2)

	done := make(chan error, 1)
	go func() {
		done <- server.handleMessage(context.Background(), entrant.session, transport.Frame{
			MessageID: RequestChannelEnter,
			Payload:   []byte{0},
		})
	}()
	roster := assertFrameID(t, entrant.wire, ReplyChannelChars)
	enter := assertFrameID(t, peer.wire, ReplyChannelEnter)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if roster.Payload[1] != 0 || roster.Payload[2] != 2 {
		t.Fatalf("entrant roster = %x", roster.Payload)
	}
	if enter.Payload[0] != 0 || enter.Payload[1] != entrant.session.channelMemberID ||
		binary.LittleEndian.Uint16(enter.Payload[2:4]) != entrant.session.SessionID() {
		t.Fatalf("peer enter delta = %x", enter.Payload)
	}

	// The entrant must not receive the 0x1507 delta intended for existing peers.
	_ = entrant.connection.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if frame, err := entrant.wire.ReadFrame(); err == nil {
		t.Fatalf("entrant received unexpected frame %#x/%x", frame.MessageID, frame.Payload)
	}
	_ = peer.connection.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if frame, err := peer.wire.ReadFrame(); err == nil {
		t.Fatalf("peer received unexpected full roster frame %#x/%x", frame.MessageID, frame.Payload)
	}
}

func TestChannelEnterMoveSendsExitAndEnterDeltas(t *testing.T) {
	server := newChannelSheetTestServer()
	oldPeer := addChannelSheetTestClient(t, server, "old", "Old", 2)
	entrant := addChannelSheetTestClient(t, server, "entrant", "Entrant", 1)
	destinationPeer := addChannelSheetTestClient(t, server, "destination", "Destination", 3)
	if _, _, err := server.moveChannelMember(destinationPeer.session, 2); err != nil {
		t.Fatal(err)
	}
	previousMemberID := entrant.session.channelMemberID

	done := make(chan error, 1)
	go func() {
		done <- server.handleMessage(context.Background(), entrant.session, transport.Frame{
			MessageID: RequestChannelEnter,
			Payload:   []byte{2},
		})
	}()
	roster := assertFrameID(t, entrant.wire, ReplyChannelChars)
	exit := assertFrameID(t, oldPeer.wire, ReplyChannelExit)
	enter := assertFrameID(t, destinationPeer.wire, ReplyChannelEnter)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if roster.Payload[1] != 2 || roster.Payload[2] != 2 {
		t.Fatalf("moved entrant roster = %x", roster.Payload)
	}
	if exit.Payload[0] != 0 || exit.Payload[1] != previousMemberID {
		t.Fatalf("old-sheet exit delta = %x", exit.Payload)
	}
	if enter.Payload[0] != 0 || enter.Payload[1] != entrant.session.channelMemberID ||
		binary.LittleEndian.Uint16(enter.Payload[2:4]) != entrant.session.SessionID() {
		t.Fatalf("destination-sheet enter delta = %x", enter.Payload)
	}
}
