package game

import (
	"bytes"
	"testing"
	"time"
)

func TestLastPlayerAndGamingKickOpcodes(t *testing.T) {
	if RequestLastPlayerInfo != 0x165e || ReplyLastPlayerInfo != 0x165f ||
		RequestGamingKick != 0x1666 || ReplyGamingKick != 0x1667 ||
		RequestGamingKickVote != 0x1668 || ReplyGamingKickVote != 0x1669 ||
		ReplyGamingKickResult != 0x166b {
		t.Fatal("last-player or gaming-kick opcode drifted from the compatibility contract")
	}
}

func TestLastPlayerInfoPayloadLayout(t *testing.T) {
	payload := LastPlayerInfoPayload(3, 8, 61, 44)
	if !bytes.Equal(payload, []byte{0, 3, 8, 61, 44}) {
		t.Fatalf("last-player payload = %x", payload)
	}
}

func TestGamingKickPayloadLayouts(t *testing.T) {
	start := GamingKickPayload(2, 7, 4)
	if !bytes.Equal(start, []byte{0, 2, 7, 4}) {
		t.Fatalf("gaming-kick payload = %x", start)
	}
	status := KickVoteStatus{Target: 7, Agreement: 3, Opposition: 1, Waiver: 2, Passed: true}
	vote := GamingKickVotePayload(status)
	if !bytes.Equal(vote, []byte{0, 7, 3, 1, 2, 0}) {
		t.Fatalf("gaming-kick vote payload = %x", vote)
	}
	result := GamingKickResultPayload(status)
	if !bytes.Equal(result, []byte{0, 7, 1, 3, 1, 2, 0}) {
		t.Fatalf("gaming-kick result payload = %x", result)
	}
}

func TestPlayerWinRate(t *testing.T) {
	tests := []struct {
		wins, losses int
		want         byte
	}{
		{wins: 6, losses: 4, want: 60},
		{wins: 1, losses: 2, want: 33},
		{wins: 0, losses: 0, want: 0},
		{wins: -5, losses: 5, want: 0},
		{wins: 5, losses: -5, want: 100},
	}
	for _, test := range tests {
		if got := playerWinRate(test.wins, test.losses); got != test.want {
			t.Errorf("playerWinRate(%d, %d) = %d, want %d", test.wins, test.losses, got, test.want)
		}
	}
}

func TestRoomLastPlayersRequiresExactlyOneLivingPlayerPerTeam(t *testing.T) {
	room, players := activeVotingRoom(t, 4)
	room.mu.Lock()
	room.Dead[0], room.Dead[1] = true, true
	room.mu.Unlock()
	redSlot, red, blueSlot, blue, ok := room.LastPlayers()
	if !ok || redSlot != 2 || red != players[2] || blueSlot != 3 || blue != players[3] {
		t.Fatalf("last players = red %d/%p blue %d/%p ok %t", redSlot, red, blueSlot, blue, ok)
	}
	room.mu.Lock()
	room.Dead[3] = true
	room.mu.Unlock()
	if _, _, _, _, ok := room.LastPlayers(); ok {
		t.Fatal("reported last players without a living blue player")
	}
}

func TestKickVoteCountsExplicitVotesAndPassesAtStrictMajority(t *testing.T) {
	room, players := activeVotingRoom(t, 4)
	started := time.Unix(100, 0)
	initiator, ok := room.StartKickVote(players[0], 1, 3, started)
	if !ok || initiator != 0 {
		t.Fatalf("start vote = initiator %d ok %t", initiator, ok)
	}

	status, accepted := room.CastKickVote(players[0], 1, 1, started.Add(time.Second))
	if !accepted || status.Agreement != 1 || status.Resolved {
		t.Fatalf("first vote = %+v accepted %t", status, accepted)
	}
	if _, accepted := room.CastKickVote(players[0], 1, 1, started.Add(2*time.Second)); accepted {
		t.Fatal("accepted a duplicate vote")
	}
	if _, accepted := room.CastKickVote(players[1], 1, 1, started.Add(2*time.Second)); accepted {
		t.Fatal("accepted a vote from the target")
	}
	status, accepted = room.CastKickVote(players[2], 1, 1, started.Add(3*time.Second))
	if !accepted || !status.Resolved || !status.Passed || status.Agreement != 2 || status.TargetSession != players[1] {
		t.Fatalf("majority vote = %+v accepted %t", status, accepted)
	}
}

func TestKickVoteFailsWhenMajorityBecomesImpossible(t *testing.T) {
	room, players := activeVotingRoom(t, 4)
	started := time.Unix(200, 0)
	if _, ok := room.StartKickVote(players[0], 1, 0, started); !ok {
		t.Fatal("start vote")
	}
	status, accepted := room.CastKickVote(players[0], 1, 0, started.Add(time.Second))
	if !accepted || status.Resolved {
		t.Fatalf("first opposition = %+v accepted %t", status, accepted)
	}
	status, accepted = room.CastKickVote(players[2], 1, 0, started.Add(2*time.Second))
	if !accepted || !status.Resolved || status.Passed || status.Opposition != 2 {
		t.Fatalf("impossible majority = %+v accepted %t", status, accepted)
	}
}

func TestKickVoteTimeoutTurnsMissingVotesIntoWaivers(t *testing.T) {
	room, players := activeVotingRoom(t, 4)
	started := time.Unix(300, 0)
	if _, ok := room.StartKickVote(players[0], 1, 2, started); !ok {
		t.Fatal("start vote")
	}
	status, accepted := room.CastKickVote(players[0], 1, 1, started.Add(time.Second))
	if !accepted || status.Resolved {
		t.Fatalf("initial agreement = %+v accepted %t", status, accepted)
	}
	if _, expired := room.ExpireKickVote(started.Add(9 * time.Second)); expired {
		t.Fatal("vote expired before the client deadline")
	}
	status, expired := room.ExpireKickVote(started.Add(kickVoteDuration))
	if !expired || !status.Resolved || status.Passed || status.Agreement != 1 || status.Waiver != 2 {
		t.Fatalf("timeout result = %+v expired %t", status, expired)
	}
}

func TestKickVoteRejectsInvalidTargetsReasonsAndChoices(t *testing.T) {
	room, players := activeVotingRoom(t, 2)
	started := time.Unix(400, 0)
	if _, ok := room.StartKickVote(players[0], 0, 0, started); ok {
		t.Fatal("started a self-kick vote")
	}
	if _, ok := room.StartKickVote(players[0], 1, 5, started); ok {
		t.Fatal("started a vote with an unknown reason")
	}
	if _, ok := room.StartKickVote(players[0], 1, 4, started); !ok {
		t.Fatal("rejected reason 4")
	}
	if _, accepted := room.CastKickVote(players[0], 1, 3, started.Add(time.Second)); accepted {
		t.Fatal("accepted an unknown vote choice")
	}
}

func activeVotingRoom(t *testing.T, count int) (*Room, []*Session) {
	t.Helper()
	room := newRoom(1, RoomConfig{Capacity: 8, Mode: 4, Time: 120, Wins: 5})
	players := make([]*Session, count)
	for index := range players {
		players[index] = &Session{UserID: index + 1, RoomNumber: -1, RoomSlot: -1, RoomTeam: 0xff, RoomTeamSlot: 0xff}
		if slot, ok := room.Add(players[index]); !ok || int(slot) != index {
			t.Fatalf("add player %d = slot %d ok %t", index, slot, ok)
		}
	}
	beginTestMatch(room)
	room.BeginRound(time.Unix(1, 0))
	return room, players
}
