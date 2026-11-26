package game

import (
	"context"
	"log/slog"
	"net/netip"
	"sort"
	"testing"

	"wtserver-go/internal/store"
)

type chatRoutingUsers struct {
	*fakeUsers
	prideByUser map[int]uint32
}

func (users *chatRoutingUsers) PrideForUser(
	_ context.Context,
	userID int,
) (store.Pride, store.PrideMember, bool, error) {
	prideID := users.prideByUser[userID]
	if prideID == 0 {
		return store.Pride{}, store.PrideMember{}, false, nil
	}
	return store.Pride{ID: prideID}, store.PrideMember{UserID: userID}, true, nil
}

func (users *chatRoutingUsers) PrideMembers(
	_ context.Context,
	prideID uint32,
) ([]store.PrideMember, error) {
	members := make([]store.PrideMember, 0)
	for userID, candidatePrideID := range users.prideByUser {
		if candidatePrideID == prideID {
			members = append(members, store.PrideMember{UserID: userID})
		}
	}
	return members, nil
}

func newChatRoutingServer(users *chatRoutingUsers) *Server {
	loopback := netip.MustParseAddr("127.0.0.1")
	return NewServer("", 0, 0, loopback, loopback, users, slog.Default())
}

func chatRoutingSession(server *Server, userID int, callsign string) *Session {
	session := authenticatedTestSession(userID, callsign, 0)
	server.addSession(session)
	return session
}

func recipientUserIDs(sessions []*Session) []int {
	result := make([]int, 0, len(sessions))
	for _, session := range sessions {
		_, _, userID, _ := session.identity()
		result = append(result, userID)
	}
	sort.Ints(result)
	return result
}

func requireRecipientUserIDs(t *testing.T, sessions []*Session, want ...int) {
	t.Helper()
	sort.Ints(want)
	got := recipientUserIDs(sessions)
	if len(got) != len(want) {
		t.Fatalf("recipient user IDs = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("recipient user IDs = %v, want %v", got, want)
		}
	}
}

func addChatRoutingRoom(
	t *testing.T,
	server *Server,
	number int16,
	started bool,
	members ...*Session,
) *Room {
	t.Helper()
	room := newRoom(number, RoomConfig{Title: "Chat", Capacity: 8})
	server.mu.Lock()
	server.rooms[number] = room
	server.mu.Unlock()
	for _, member := range members {
		if _, added := room.Add(member); !added {
			t.Fatalf("failed to add user to room %d", number)
		}
		server.refreshSessionLobby(member)
	}
	room.mu.Lock()
	room.GameStarted = started
	room.mu.Unlock()
	return room
}

func TestChannelChatTeamRecipientsStayOnTeam(t *testing.T) {
	users := &chatRoutingUsers{fakeUsers: &fakeUsers{}, prideByUser: map[int]uint32{}}
	server := newChatRoutingServer(users)
	sender := chatRoutingSession(server, 1, "Sender")
	teammate := chatRoutingSession(server, 2, "Teammate")
	opponent := chatRoutingSession(server, 3, "Opponent")
	addChatRoutingRoom(t, server, 7, true, sender, teammate, opponent)

	sender.mu.Lock()
	sender.RoomTeam = redTeam
	sender.mu.Unlock()
	teammate.mu.Lock()
	teammate.RoomTeam = redTeam
	teammate.mu.Unlock()
	opponent.mu.Lock()
	opponent.RoomTeam = blueTeam
	opponent.mu.Unlock()

	recipients, err := server.channelChatRecipients(
		context.Background(), sender, channelChatPrideOrTeam,
	)
	if err != nil {
		t.Fatal(err)
	}
	requireRecipientUserIDs(t, recipients, 1, 2)

	recipients, err = server.channelChatRecipients(
		context.Background(), sender, channelChatTeamDead,
	)
	if err != nil {
		t.Fatal(err)
	}
	requireRecipientUserIDs(t, recipients, 1, 2)
}

func TestChannelChatPrideRecipientsFollowMembershipAcrossRooms(t *testing.T) {
	users := &chatRoutingUsers{
		fakeUsers:   &fakeUsers{},
		prideByUser: map[int]uint32{1: 10, 2: 10, 3: 20, 4: 10},
	}
	server := newChatRoutingServer(users)
	sender := chatRoutingSession(server, 1, "Sender")
	lobbyMember := chatRoutingSession(server, 2, "LobbyMember")
	outsider := chatRoutingSession(server, 3, "Outsider")
	roomMember := chatRoutingSession(server, 4, "RoomMember")
	_ = lobbyMember
	_ = outsider
	addChatRoutingRoom(t, server, 8, true, roomMember)

	recipients, err := server.channelChatRecipients(
		context.Background(), sender, channelChatPrideOrTeam,
	)
	if err != nil {
		t.Fatal(err)
	}
	requireRecipientUserIDs(t, recipients, 1, 2, 4)

	recipients, err = server.channelChatRecipients(
		context.Background(), roomMember, channelChatPride,
	)
	if err != nil {
		t.Fatal(err)
	}
	requireRecipientUserIDs(t, recipients, 1, 2, 4)
}

func TestChannelChatShoutIncludesLobbyAndRoomSessions(t *testing.T) {
	users := &chatRoutingUsers{fakeUsers: &fakeUsers{}, prideByUser: map[int]uint32{}}
	server := newChatRoutingServer(users)
	sender := chatRoutingSession(server, 1, "Sender")
	_ = chatRoutingSession(server, 2, "Lobby")
	roomMember := chatRoutingSession(server, 3, "Room")
	addChatRoutingRoom(t, server, 9, true, roomMember)

	recipients, err := server.channelChatRecipients(
		context.Background(), sender, channelChatShout,
	)
	if err != nil {
		t.Fatal(err)
	}
	requireRecipientUserIDs(t, recipients, 1, 2, 3)
}

func TestOrdinaryChannelAndRoomChatRemainLocal(t *testing.T) {
	users := &chatRoutingUsers{fakeUsers: &fakeUsers{}, prideByUser: map[int]uint32{}}
	server := newChatRoutingServer(users)
	lobbySender := chatRoutingSession(server, 1, "LobbySender")
	_ = chatRoutingSession(server, 2, "Lobby")
	roomSender := chatRoutingSession(server, 3, "RoomSender")
	roomPeer := chatRoutingSession(server, 4, "RoomPeer")
	addChatRoutingRoom(t, server, 10, false, roomSender, roomPeer)

	recipients, err := server.channelChatRecipients(
		context.Background(), lobbySender, channelChatAll,
	)
	if err != nil {
		t.Fatal(err)
	}
	requireRecipientUserIDs(t, recipients, 1, 2)

	recipients, err = server.channelChatRecipients(
		context.Background(), roomSender, channelChatAll,
	)
	if err != nil {
		t.Fatal(err)
	}
	requireRecipientUserIDs(t, recipients, 3, 4)
}
