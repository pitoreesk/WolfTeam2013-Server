package game

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"

	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

type privateLadderTestUsers struct {
	*fakeUsers
	store.PrivateLadderStore
}

func (p *privateLadderTestUsers) PrivateLadderInfo(context.Context, int) (store.PrivateLadderInfo, error) {
	return store.PrivateLadderInfo{Rank: 2, Points: 99}, nil
}

func (p *privateLadderTestUsers) PrivateLadderRanks(context.Context, int, int) ([]store.PrivateLadderRank, error) {
	return []store.PrivateLadderRank{{Username: "user", Callsign: "Player", Level: 7, Rank: 2, Points: 99, Wins: 4}}, nil
}

func (p *privateLadderTestUsers) PrivateLadderNotice(context.Context, time.Time) (store.PrivateLadderNotice, error) {
	return store.PrivateLadderNotice{Title: "Season"}, nil
}

func TestPrivateLadderInfoAndRankDispatch(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	defer serverNet.Close()
	defer clientNet.Close()
	_ = clientNet.SetDeadline(time.Now().Add(3 * time.Second))
	loopback := netip.MustParseAddr("127.0.0.1")
	users := &privateLadderTestUsers{fakeUsers: &fakeUsers{}}
	server := NewServer("unused", 40707, 40708, loopback, loopback, users, slog.New(slog.NewTextHandler(io.Discard, nil)))
	session, err := newSession(transport.NewEncryptedConn(serverNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	session.setAuthenticated("user", 42, "Player", 7)
	client := transport.NewEncryptedConn(clientNet, nil)

	done := make(chan error, 1)
	go func() {
		done <- server.handleMessage(context.Background(), session, transport.Frame{MessageID: RequestPrivateInfo})
	}()
	info := assertFrameID(t, client, ReplyPrivateInfo)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if info.Payload[0] != 0 || binary.LittleEndian.Uint32(info.Payload[1:5]) != 99 || binary.LittleEndian.Uint32(info.Payload[5:9]) != 2 {
		t.Fatalf("private info reply = %x", info.Payload)
	}

	go func() {
		done <- server.handleMessage(context.Background(), session, transport.Frame{MessageID: RequestPrivateRankList, Payload: []byte{0, 9, 3, 0}})
	}()
	ranks := assertFrameID(t, client, ReplyPrivateRankList)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if ranks.Payload[0] != 0 || ranks.Payload[1] != 1 || ranks.Payload[len(ranks.Payload)/2] == 0xff {
		t.Fatalf("private rank reply = %x", ranks.Payload)
	}
}

var (
	ladderRandom     = PrivateLadderSearchRequest{ModeWord: ladderAnyMode, Map: ladderAnyByte, Capacity: ladderAnyByte}
	ladderHumanDM    = PrivateLadderSearchRequest{ModeWord: 0x412, Map: ladderAnyByte, Capacity: ladderAnyByte}
	ladderWolfDM     = PrivateLadderSearchRequest{ModeWord: 0x40a, Map: ladderAnyByte, Capacity: ladderAnyByte}
	ladderHumanDMMap = PrivateLadderSearchRequest{ModeWord: 0x412, Map: 5, Capacity: ladderAnyByte}
)

func TestLadderSearchCompatibility(t *testing.T) {
	for _, test := range []struct {
		name string
		a, b PrivateLadderSearchRequest
		want bool
	}{
		{"random meets human", ladderRandom, ladderHumanDM, true},
		{"human meets human", ladderHumanDM, ladderHumanDM, true},
		{"human refuses wolf", ladderHumanDM, ladderWolfDM, false},
		{"any map meets a map", ladderHumanDM, ladderHumanDMMap, true},
		{"different maps refuse", ladderHumanDMMap, PrivateLadderSearchRequest{ModeWord: 0x412, Map: 6, Capacity: ladderAnyByte}, false},
	} {
		if got := test.a.Compatible(test.b); got != test.want || test.b.Compatible(test.a) != test.want {
			t.Fatalf("%s: compatible = %v, want %v", test.name, got, test.want)
		}
	}
}

func newLadderTestServer(t *testing.T) *Server {
	t.Helper()
	loopback := netip.MustParseAddr("127.0.0.1")
	return NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func ladderSession(server *Server, name string, id int, channel store.ChannelType) *Session {
	loopback := netip.MustParseAddr("127.0.0.1")
	session := &Session{LoginName: name, Callsign: name, UserID: id, RoomNumber: -1, RoomSlot: -1, UDPIP: loopback, UDPPort: uint16(5000 + id)}
	session.setChannelType(channel)
	server.addSession(session)
	return session
}

// Ladder (type 3): the first searcher gets no room and creates one; a later
// compatible searcher is sent to that room, and a random creator's room takes
// the joiner's concrete mode.
func TestLadderRoomSearchSendsTheNextSearcherToTheWaitingRoom(t *testing.T) {
	server := newLadderTestServer(t)
	creator := ladderSession(server, "one", 1, store.ChannelTypeLadder)
	joiner := ladderSession(server, "two", 2, store.ChannelTypeLadder)
	if err := server.startPrivateLadderSearch(context.Background(), creator, ladderRandom); err != nil {
		t.Fatal(err)
	}
	if _, ok := server.findLadderRoom(joiner, ladderHumanDM); ok {
		t.Fatal("a room was offered before anybody created one")
	}
	// The client answers n = 0 with CREATE_REQ; model the created room.
	room, err := server.allocateRoom(RoomConfig{Capacity: 2, Mode: 0x2408, Map: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := room.Add(creator); !ok {
		t.Fatal("add creator")
	}
	search, _ := server.ladderSearchOf(creator)
	room.SetLadderSearch(creator, search)

	if err := server.startPrivateLadderSearch(context.Background(), joiner, ladderHumanDM); err != nil {
		t.Fatal(err)
	}
	if got := room.Snapshot().Config.Mode; got != 0x412 {
		t.Fatalf("random creator's room mode = 0x%x, want the joiner's 0x412", got)
	}
	if _, searching := server.ladderSearchOf(joiner); searching {
		t.Fatal("the matched joiner is still searching")
	}
}

func TestLadderRoomSearchRefusesAnIncompatibleRoom(t *testing.T) {
	server := newLadderTestServer(t)
	creator := ladderSession(server, "one", 1, store.ChannelTypeLadder)
	joiner := ladderSession(server, "two", 2, store.ChannelTypeLadder)
	_ = server.startPrivateLadderSearch(context.Background(), creator, ladderWolfDM)
	room, _ := server.allocateRoom(RoomConfig{Capacity: 2, Mode: 0x40a})
	room.Add(creator)
	room.SetLadderSearch(creator, ladderWolfDM)
	if _, ok := server.findLadderRoom(joiner, ladderHumanDM); ok {
		t.Fatal("a wolf-deathmatch room was offered to a human-deathmatch search")
	}
	// A creator who stopped searching no longer offers the room.
	server.clearLadderSearch(creator)
	if _, ok := server.findLadderRoom(joiner, ladderRandom); ok {
		t.Fatal("a room whose creator stopped searching was offered")
	}
}

// Private ladder (type 4) pairs queued players; random meets a concrete mode.
func TestPrivateLadderQueuePairsCompatibleSearches(t *testing.T) {
	server := newLadderTestServer(t)
	first := ladderSession(server, "one", 1, store.ChannelTypePrivateLadder)
	other := ladderSession(server, "two", 2, store.ChannelTypePrivateLadder)
	match := ladderSession(server, "three", 3, store.ChannelTypePrivateLadder)
	if err := server.startPrivateLadderSearch(context.Background(), first, ladderWolfDM); err != nil {
		t.Fatal(err)
	}
	if err := server.startPrivateLadderSearch(context.Background(), other, ladderHumanDM); err != nil {
		t.Fatal(err)
	}
	if other.RoomNumber != -1 || first.RoomNumber != -1 {
		t.Fatal("incompatible searches were paired")
	}
	if err := server.startPrivateLadderSearch(context.Background(), match, ladderRandom); err != nil {
		t.Fatal(err)
	}
	if first.RoomNumber < 0 || match.RoomNumber != first.RoomNumber {
		t.Fatalf("random search did not meet the first queued search: %d/%d", first.RoomNumber, match.RoomNumber)
	}
	room := server.roomByNumber(first.RoomNumber)
	if room == nil || room.Snapshot().Config.Mode != 0x40a || room.Snapshot().Users != 2 {
		t.Fatalf("private-ladder room = %+v", room.Snapshot().Config)
	}
	if server.privateLadderSearches[other] == nil || len(server.privateLadderSearches) != 1 {
		t.Fatalf("the unmatched search left the queue: %d", len(server.privateLadderSearches))
	}
	server.removePrivateLadderSearch(other)
	if len(server.privateLadderSearches) != 0 || server.privateLadderQueue.Len() != 0 {
		t.Fatal("cancelled search remains queued")
	}
}

func TestLadderOptionsPayloadLayout(t *testing.T) {
	payload, err := LadderOptionsPayload([]LadderOptionMode{{ModeWord: 0x412, Maps: []LadderOptionMap{{Map: 3, Capacities: 2}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 1, 0, 0x12, 4, 0, 0, 1, 3, 2}
	if string(payload) != string(want) {
		t.Fatalf("ladder options = %x, want %x", payload, want)
	}
	if got := LadderRoomListPayload([]int16{7}); string(got) != string([]byte{0, 1, 0, 7, 0}) {
		t.Fatalf("ladder room list = %x", got)
	}
}

func TestPrivateChallengeParticipantIndexTracksLifecycle(t *testing.T) {
	loopback := netip.MustParseAddr("127.0.0.1")
	server := NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	challenger := &Session{LoginName: "one", Callsign: "One", UserID: 1, RoomNumber: -1, RoomSlot: -1}
	target := &Session{LoginName: "two", Callsign: "Two", UserID: 2, RoomNumber: -1, RoomSlot: -1}
	third := &Session{LoginName: "three", Callsign: "Three", UserID: 3, RoomNumber: -1, RoomSlot: -1}
	server.addSession(challenger)
	server.addSession(target)
	server.addSession(third)

	if err := server.createPrivateChallenge(context.Background(), challenger, "Two"); err != nil {
		t.Fatal(err)
	}
	token := server.privateChallengeByUser[challenger]
	if token == 0 || server.privateChallengeByUser[target] != token || len(server.privateLadderChallenges) != 1 {
		t.Fatalf("challenge indexes = token %d target %d challenges %d", token, server.privateChallengeByUser[target], len(server.privateLadderChallenges))
	}
	if err := server.createPrivateChallenge(context.Background(), third, "Two"); err != nil {
		t.Fatal(err)
	}
	if len(server.privateLadderChallenges) != 1 || server.privateChallengeByUser[third] != 0 {
		t.Fatalf("busy target accepted a second challenge: challenges=%d third=%d", len(server.privateLadderChallenges), server.privateChallengeByUser[third])
	}
	if err := server.resolvePrivateChallenge(context.Background(), target, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	if len(server.privateLadderChallenges) != 0 || len(server.privateChallengeByUser) != 0 {
		t.Fatalf("resolved challenge remains indexed: challenges=%d participants=%d", len(server.privateLadderChallenges), len(server.privateChallengeByUser))
	}
}
