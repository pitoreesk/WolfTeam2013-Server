package game

import (
	"context"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"wtserver-go/internal/store"
)

type moderationFakeUsers struct {
	*fakeUsers
	mutedTarget  string
	mutedState   store.ModerationState
	bannedTarget string
	bannedState  store.ModerationState
}

func (users *moderationFakeUsers) SetMutedByCallsign(
	_ context.Context,
	callsign string,
	state store.ModerationState,
) (bool, error) {
	if state.Active {
		users.mutedTarget = callsign
		users.mutedState = state
	}
	return callsign != "", nil
}

func (users *moderationFakeUsers) SetBannedByCallsign(
	_ context.Context,
	callsign string,
	state store.ModerationState,
) (bool, error) {
	if state.Active {
		users.bannedTarget = callsign
		users.bannedState = state
	}
	return callsign != "", nil
}

func moderationTestServer(users *moderationFakeUsers) *Server {
	address := netip.MustParseAddr("127.0.0.1")
	return NewServer("", 0, 0, address, address, users, slog.Default())
}

func authenticatedTestSession(userID int, callsign string, authority byte) *Session {
	session := newBotSession(callsign, userID, 0)
	session.IsBot = false
	session.setAuthenticatedWithAuthority(callsign, userID, callsign, 7, authority)
	return session
}

func TestGMCommandRequiresStaffAuthority(t *testing.T) {
	users := &moderationFakeUsers{fakeUsers: &fakeUsers{}}
	server := moderationTestServer(users)
	ordinary := authenticatedTestSession(1, "Ordinary", 0)
	server.addSession(ordinary)

	if err := server.handleGMCommand(context.Background(), ordinary, GMCommandRequest{
		Command: GMCommandSilenceOn,
	}); err != nil {
		t.Fatal(err)
	}
	if !server.channelChatAllowed(ordinary) {
		t.Fatal("unauthorized command changed channel silence")
	}
}

func TestStaffSilenceAndMuteAreEnforced(t *testing.T) {
	users := &moderationFakeUsers{fakeUsers: &fakeUsers{}}
	server := moderationTestServer(users)
	staff := authenticatedTestSession(1, "Staff", 12)
	target := authenticatedTestSession(2, "Player", 0)
	server.addSession(staff)
	server.addSession(target)

	if err := server.handleGMCommand(context.Background(), staff, GMCommandRequest{
		Command: GMCommandSilenceOn,
	}); err != nil {
		t.Fatal(err)
	}
	if server.channelChatAllowed(target) {
		t.Fatal("ordinary chat remained enabled during global silence")
	}
	if !server.channelChatAllowed(staff) {
		t.Fatal("staff chat was blocked during global silence")
	}

	if err := server.handleGMCommand(context.Background(), staff, GMCommandRequest{
		Command:  GMCommandMuteBan,
		Target:   "player",
		Duration: 30 * time.Minute,
		Reason:   "spam",
	}); err != nil {
		t.Fatal(err)
	}
	if !target.muted() || users.mutedTarget != "player" ||
		users.mutedState.Reason != "spam" ||
		users.mutedState.ExpiresAt.Sub(users.mutedState.StartedAt) != 30*time.Minute {
		t.Fatalf("mute state = %v, persisted %q/%+v", target.muted(), users.mutedTarget, users.mutedState)
	}

	if err := server.handleGMCommand(context.Background(), staff, GMCommandRequest{
		Command: GMCommandSilenceOff,
	}); err != nil {
		t.Fatal(err)
	}
	if server.channelChatAllowed(target) {
		t.Fatal("muted player chat was enabled after global silence ended")
	}
}

func TestOfflineBanIsPersisted(t *testing.T) {
	users := &moderationFakeUsers{fakeUsers: &fakeUsers{}}
	server := moderationTestServer(users)
	staff := authenticatedTestSession(1, "Staff", 13)
	server.addSession(staff)

	if err := server.handleGMCommand(context.Background(), staff, GMCommandRequest{
		Command:  GMCommandBan,
		Target:   "Offline",
		Duration: 7 * 24 * time.Hour,
		Reason:   "abuse",
	}); err != nil {
		t.Fatal(err)
	}
	if users.bannedTarget != "Offline" || users.bannedState.Reason != "abuse" ||
		users.bannedState.ExpiresAt.Sub(users.bannedState.StartedAt) != 7*24*time.Hour {
		t.Fatalf("persisted ban = %q/%+v", users.bannedTarget, users.bannedState)
	}
}

func TestExpiredSessionMuteStopsBlockingChat(t *testing.T) {
	session := authenticatedTestSession(2, "Player", 0)
	session.setMutedUntil(true, time.Now().Add(-time.Second))
	if session.muted() {
		t.Fatal("expired session mute remained active")
	}
}

func TestGMSearchRecognizesLobbyAndRoomLocations(t *testing.T) {
	users := &moderationFakeUsers{fakeUsers: &fakeUsers{}}
	server := moderationTestServer(users)
	staff := authenticatedTestSession(1, "Staff", 12)
	target := authenticatedTestSession(2, "Player", 0)
	server.addSession(staff)
	server.addSession(target)
	if got := server.gmSearchDescription("player"); got != "Player is in the lobby on sheet 1." {
		t.Fatalf("lobby search = %q", got)
	}

	room := newRoom(17, RoomConfig{Title: "Search Room", Capacity: 8})
	server.mu.Lock()
	server.rooms[17] = room
	server.mu.Unlock()
	if _, ok := room.Add(target); !ok {
		t.Fatal("failed to place search target in room")
	}
	if got := server.gmSearchDescription("Player"); got != "Player is in room 17 (waiting)." {
		t.Fatalf("room search = %q", got)
	}
	room.mu.Lock()
	room.GameStarted = true
	room.mu.Unlock()
	if got := server.gmSearchDescription("Player"); got != "Player is in room 17 (playing)." {
		t.Fatalf("playing search = %q", got)
	}
	if got := server.gmSearchDescription("Missing"); got != "Missing is offline." {
		t.Fatalf("offline search = %q", got)
	}
}
