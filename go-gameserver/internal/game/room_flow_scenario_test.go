package game

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"wtserver-go/internal/transport"
)

// flowPeer is one connected client whose frames are drained in the
// background, so synchronous room broadcasts never block the handler.
type flowPeer struct {
	session *Session
	mu      sync.Mutex
	frames  []transport.Frame
}

func (p *flowPeer) received() []transport.Frame {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]transport.Frame(nil), p.frames...)
}

func (p *flowPeer) reset() {
	p.mu.Lock()
	p.frames = nil
	p.mu.Unlock()
}

// ids lists the message IDs received so far, in order.
func (p *flowPeer) ids() []uint16 {
	var out []uint16
	for _, frame := range p.received() {
		out = append(out, frame.MessageID)
	}
	return out
}

func (p *flowPeer) find(id uint16) (transport.Frame, bool) {
	for _, frame := range p.received() {
		if frame.MessageID == id {
			return frame, true
		}
	}
	return transport.Frame{}, false
}

type flowRoom struct {
	server *Server
	room   *Room
	peers  []*flowPeer
}

func newFlowRoom(t *testing.T, config RoomConfig, players int) *flowRoom {
	t.Helper()
	loopback := netip.MustParseAddr("127.0.0.1")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, logger)
	server.roundEndDelay = 0
	room := newRoom(0, config)
	server.mu.Lock()
	server.rooms[room.Number] = room
	server.mu.Unlock()
	flow := &flowRoom{server: server, room: room}
	for index := 0; index < players; index++ {
		flow.addPeer(t)
	}
	return flow
}

func (f *flowRoom) addPeer(t *testing.T) *flowPeer {
	t.Helper()
	loopback := netip.MustParseAddr("127.0.0.1")
	serverNet, clientNet := net.Pipe()
	t.Cleanup(func() {
		_ = serverNet.Close()
		_ = clientNet.Close()
	})
	session, err := newSession(transport.NewEncryptedConn(serverNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	session.setAuthenticated("p", 100+len(f.peers), "Player", 7)
	peer := &flowPeer{session: session}
	client := transport.NewEncryptedConn(clientNet, nil)
	go func() {
		for {
			frame, err := client.ReadFrame()
			if err != nil {
				return
			}
			peer.mu.Lock()
			peer.frames = append(peer.frames, frame)
			peer.mu.Unlock()
		}
	}()
	if _, ok := f.room.Add(session); !ok {
		t.Fatal("room rejected a player")
	}
	f.peers = append(f.peers, peer)
	return peer
}

func (f *flowRoom) send(t *testing.T, peer *flowPeer, id uint16, payload []byte) {
	t.Helper()
	if err := f.server.handleMessage(context.Background(), peer.session, transport.Frame{MessageID: id, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	settle()
}

func settle() { time.Sleep(20 * time.Millisecond) }

func statusOf(session *Session) byte {
	session.mu.RLock()
	defer session.mu.RUnlock()
	return session.RoomStatus
}

// startWithReady starts the match from the master (peer 0) with the given
// peers ready.
func (f *flowRoom) startWithReady(t *testing.T, ready ...int) {
	t.Helper()
	for _, index := range ready {
		f.send(t, f.peers[index], RequestUDPReady, []byte{1})
	}
	f.send(t, f.peers[0], RequestStartGame, nil)
}

// 0x1619 makes the room section load the field whatever the receiver's own
// status, so only the master and the ready members may receive it. A member
// who did not ready stays on the room screen and learns that the match runs
// from the master's 0x1617 (status 3), which turns his button into Start.
func TestStartGameOrdersOnlyTheReadyMembersIntoTheField(t *testing.T) {
	flow := newFlowRoom(t, RoomConfig{Capacity: 8, Mode: 2, Time: 300, Points: 30}, 3)
	flow.startWithReady(t, 1)
	for index, peer := range flow.peers[:2] {
		if _, ok := peer.find(ReplyStartGame); !ok {
			t.Fatalf("peer %d (master/ready) got no 0x1619: %x", index, peer.ids())
		}
	}
	idle := flow.peers[2]
	if _, ok := idle.find(ReplyStartGame); ok {
		t.Fatal("the member who did not ready was ordered into the field")
	}
	announce, ok := idle.find(ReplyChangeSlot)
	if !ok || announce.Payload[1] != flow.room.Snapshot().Master || announce.Payload[2] != spectatorRoomStatus {
		t.Fatalf("idle member did not see the master enter the match: %x", announce.Payload)
	}
	if statusOf(idle.session) != 0 {
		t.Fatalf("idle member status = %d, want the room screen", statusOf(idle.session))
	}
}

// A member who was in the field inventory when the match started comes back to
// the room screen, which sends INITFIELD; he gets the master's status again so
// his button reads Start.
func TestRoomScreenReturnDuringMatchRepeatsTheMasterStatus(t *testing.T) {
	flow := newFlowRoom(t, RoomConfig{Capacity: 8, Mode: 2, Time: 300, Points: 30}, 3)
	flow.startWithReady(t, 1)
	master := flow.room.Snapshot().Master
	flow.send(t, flow.peers[0], RequestStartRound, nil)
	returning := flow.peers[2]
	returning.reset()
	flow.send(t, returning, RequestInitField, nil)
	ids := returning.ids()
	if len(ids) != 2 || ids[0] != ReplyInitField || ids[1] != ReplyChangeSlot {
		t.Fatalf("room-screen return = %x, want INITFIELD then 0x1617", ids)
	}
	frame, _ := returning.find(ReplyChangeSlot)
	if frame.Payload[1] != master || frame.Payload[2] != activeRoomStatus {
		t.Fatalf("repeated master status = %x", frame.Payload)
	}
	// Outside a match the room screen gets only the plain reply.
	idleFlow := newFlowRoom(t, RoomConfig{Capacity: 8, Mode: 2, Time: 300, Points: 30}, 2)
	idleFlow.send(t, idleFlow.peers[1], RequestInitField, nil)
	if ids := idleFlow.peers[1].ids(); len(ids) != 1 || ids[0] != ReplyInitField {
		t.Fatalf("idle room INITFIELD = %x", ids)
	}
}

func TestReadyMemberCannotChangeTeam(t *testing.T) {
	flow := newFlowRoom(t, RoomConfig{Capacity: 8, Mode: 2, Time: 300, Points: 30}, 2)
	member := flow.peers[1]
	flow.send(t, member, RequestUDPReady, []byte{1})
	member.reset()
	flow.send(t, member, RequestChangeTeam, nil)
	frame, ok := member.find(ReplyChangeTeam)
	if !ok || frame.Payload[0] != 184 {
		t.Fatalf("ready team change = %x, want result 184", frame.Payload)
	}
	flow.send(t, member, RequestUDPReady, []byte{0})
	member.reset()
	flow.send(t, member, RequestChangeTeam, nil)
	if frame, ok := member.find(ReplyChangeTeam); !ok || frame.Payload[0] != 0 {
		t.Fatalf("unready team change = %x, want success", frame.Payload)
	}
}

// The late joiner is announced with 0x1617 before ENTERGAME so field clients
// list him, and in Destruction he sits the running round out as a spectator.
func TestDestructionLateJoinerSpectatesTheRunningRound(t *testing.T) {
	flow := newFlowRoom(t, RoomConfig{Capacity: 8, Mode: 4, Wins: 3, Time: 180}, 3)
	flow.startWithReady(t, 1)
	flow.send(t, flow.peers[0], RequestStartRound, nil)
	flow.send(t, flow.peers[1], RequestStartRound, nil)
	late := flow.peers[2]
	watcher := flow.peers[0]
	watcher.reset()
	flow.send(t, late, RequestEnterGame, nil)
	ids := watcher.ids()
	if len(ids) < 2 || ids[0] != ReplyChangeSlot || ids[1] != ReplyEnterGame {
		t.Fatalf("late-join announcement order = %x, want 0x1617 then 0x161D", ids)
	}
	flow.send(t, late, RequestStartRound, nil)
	if statusOf(late.session) != spectatorRoomStatus {
		t.Fatalf("late joiner status = %d, want the spectator shell", statusOf(late.session))
	}
	// His team loses the round when its only playing member dies: the
	// spectator has no body and does not keep the team alive.
	lateTeam := roomTeam(late.session)
	var victim *flowPeer
	for _, peer := range flow.peers[:2] {
		if roomTeam(peer.session) == lateTeam {
			victim = peer
		}
	}
	if victim == nil {
		t.Skip("late joiner shares no team with a player")
	}
	result := flow.room.RecordKill(nil, victim.session)
	if !result.FinishRound {
		t.Fatalf("the round did not end although only a spectator was left: %+v", result)
	}
}

// A member who never left the room screen is not in the match: he does not
// keep his team alive, is not listed for rewards and is not announced as
// active at the next round.
func TestRoomScreenMemberStaysOutOfTheMatch(t *testing.T) {
	flow := newFlowRoom(t, RoomConfig{Capacity: 8, Mode: 32, Wins: 3, Time: 180}, 3)
	flow.startWithReady(t, 1)
	flow.send(t, flow.peers[0], RequestStartRound, nil)
	flow.send(t, flow.peers[1], RequestStartRound, nil)
	idle := flow.peers[2]
	for _, player := range flow.room.MatchPlayers() {
		if player.Session == idle.session {
			t.Fatal("the room-screen member is listed as a match player")
		}
	}
	idleTeam := roomTeam(idle.session)
	var teammate *flowPeer
	for _, peer := range flow.peers[:2] {
		if roomTeam(peer.session) == idleTeam {
			teammate = peer
		}
	}
	if teammate == nil {
		t.Fatal("expected the room-screen member to share a team")
	}
	result := flow.room.RecordKill(nil, teammate.session)
	if !result.FinishRound {
		t.Fatalf("Ice Hold round kept running for a room-screen member: %+v", result)
	}
	idle.reset()
	flow.room.FinishRound(false)
	promoted := flow.room.CompleteRoundDelay(false, time.Now())
	for _, slot := range promoted {
		if flow.room.SessionAt(slot) == idle.session {
			t.Fatal("the room-screen member was promoted to active")
		}
	}
	if statusOf(idle.session) != 0 {
		t.Fatalf("room-screen status = %d after the round boundary", statusOf(idle.session))
	}
}

// Ice Hold ("Uyku Modu"): a late joiner arrives asleep, a team-mate can stand
// him up into the running round, and the revival lifts him to active before
// the 0x1663 goes out.
func TestIceHoldLateJoinerArrivesAsleepAndCanBeRevived(t *testing.T) {
	flow := newFlowRoom(t, RoomConfig{Capacity: 8, Mode: 32, Wins: 3, Time: 180}, 3)
	flow.startWithReady(t, 1)
	flow.send(t, flow.peers[0], RequestStartRound, nil)
	flow.send(t, flow.peers[1], RequestStartRound, nil)
	late := flow.peers[2]
	flow.send(t, late, RequestEnterGame, nil)
	flow.send(t, late, RequestStartRound, nil)
	slot, _ := flow.room.MemberSlot(late.session)
	if !flow.room.Snapshot().Dead[slot] || statusOf(late.session) != spectatorRoomStatus {
		t.Fatalf("Ice Hold late joiner dead=%v status=%d, want asleep", flow.room.Snapshot().Dead[slot], statusOf(late.session))
	}
	watcher := flow.peers[0]
	watcher.reset()
	// 0x1662: reviver slot, revived (sender) slot.
	flow.send(t, late, RequestRevival, []byte{0, slot})
	ids := watcher.ids()
	if len(ids) != 2 || ids[0] != ReplyChangeSlot || ids[1] != ReplyRevival {
		t.Fatalf("revival = %x, want 0x1617 (active) then 0x1663", ids)
	}
	if statusOf(late.session) != activeRoomStatus || flow.room.Snapshot().Dead[slot] {
		t.Fatal("the revived late joiner is not an active body")
	}
}

// Wolf Hunt never crowns a member who is not in the field.
func TestWolfHuntKingIsChosenFromTheField(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		flow := newFlowRoom(t, RoomConfig{Capacity: 8, Mode: 2 | wolfHuntModeFlag, Wins: 3, Time: 180}, 3)
		flow.startWithReady(t, 1)
		flow.send(t, flow.peers[0], RequestStartRound, nil)
		idle := flow.peers[2]
		slot, _ := flow.room.MemberSlot(idle.session)
		flow.room.mu.RLock()
		crowned := flow.room.WolfKings[slot]
		flow.room.mu.RUnlock()
		if crowned {
			t.Fatal("a room-screen member was crowned Wolf King")
		}
	}
}

// A human who dies without a wolf killer (a fall or self-blast arrives as
// killer == victim) is infected like any other human death; the last human
// dying that way hands the round to the wolves instead of hanging until the
// clock gives it to the humans.
func TestWolfHuntHumanSuicideInfects(t *testing.T) {
	room := newRoom(1, RoomConfig{Capacity: 4, Mode: 0x980, Wins: 3, Time: 90})
	for index := 0; index < 2; index++ {
		if _, ok := room.Add(&Session{RoomNumber: -1, RoomSlot: -1}); !ok {
			t.Fatalf("add player %d", index)
		}
	}
	beginTestMatch(room)
	room.BeginRound(testTime)
	state, _ := room.PrepareSpecialRound()
	humans := wolfHuntSlotsForTeam(state.Players, wolfHuntHumanTeam)
	if len(state.Kings) != 1 || len(humans) != 1 {
		t.Fatalf("Wolf Hunt state = %+v", state)
	}
	human := room.SessionAt(humans[0])
	result := room.RecordKill(human, human)
	if !result.WolfHuntConverted || result.WolfHuntTeam != wolfHuntCreatureTeam {
		t.Fatalf("human suicide did not infect: %+v", result)
	}
	if !result.FinishRound || result.WinTeam != wolfHuntCreatureTeam {
		t.Fatalf("the last human's suicide did not end the round for the wolves: %+v", result)
	}
}
