package game

import (
	"testing"
	"time"
)

// CS_FD_ENDROUND_ACK announces the side change when round 5 ends
// (sub_342E2900, stringtable 1102), so the swap goes out before round 6 and
// never earlier, whatever the score.
func TestDestructionSwapsSidesAfterRoundFive(t *testing.T) {
	room := newRoom(7, RoomConfig{Capacity: 8, Mode: 4, Wins: 5, Time: 90, WeaponFlags: destructionSideSwitchFlag})
	red := &Session{RoomNumber: -1, RoomSlot: -1}
	blue := &Session{RoomNumber: -1, RoomSlot: -1}
	redSlot, _ := room.Add(red)
	if _, ok := room.Add(blue); !ok {
		t.Fatal("add blue player")
	}
	room.BeginMatch()
	start := time.Unix(1_000, 0)
	room.BeginRound(start)
	for ended := 1; ended <= 5; ended++ {
		// Red plants and the bomb goes off every round: 3-0 already after
		// round 3, which the old score threshold rotated on.
		if !room.RecordBombPlant(red, redSlot, start.Add(5*time.Second)) {
			t.Fatalf("round %d: plant by team 1 rejected", ended)
		}
		// The fifth win ends the match before any halftime.
		if _, endGame, triggered := room.BombTimeout(start.Add(60 * time.Second)); !triggered || endGame != (ended == 5) {
			t.Fatalf("round %d: explosion triggered=%v end=%v", ended, triggered, endGame)
		}
		if ended == 5 {
			break
		}
		room.FinishRound(false)
		if switched, _, _ := room.CheckAndTriggerSwitchTeam(); switched {
			t.Fatalf("sides swapped after round %d", ended)
		}
		start = start.Add(100 * time.Second)
		room.CompleteRoundDelay(false, start)
	}
	if room.RedScore != 5 {
		t.Fatalf("red score = %d", room.RedScore)
	}
}

func TestDestructionHalftimeMovesPlayersNotRoles(t *testing.T) {
	room := newRoom(8, RoomConfig{Capacity: 8, Mode: 4, Wins: 5, Time: 90, WeaponFlags: destructionSideSwitchFlag})
	first := &Session{RoomNumber: -1, RoomSlot: -1}
	second := &Session{RoomNumber: -1, RoomSlot: -1}
	firstSlot, _ := room.Add(first)
	secondSlot, _ := room.Add(second)
	room.BeginMatch()
	start := time.Unix(1_000, 0)
	room.BeginRound(start)
	// Alternate the rounds so nobody reaches 5 wins before halftime.
	for ended := 1; ended <= 5; ended++ {
		if ended%2 == 1 {
			room.RecordBombPlant(first, firstSlot, start.Add(5*time.Second))
			room.BombTimeout(start.Add(60 * time.Second))
		} else {
			room.RoundTimeout(start.Add(91 * time.Second))
		}
		room.FinishRound(false)
		switched, redScore, blueScore := room.CheckAndTriggerSwitchTeam()
		if switched != (ended == 5) {
			t.Fatalf("after round %d switched=%v", ended, switched)
		}
		if switched && (redScore != 2 || blueScore != 3) {
			t.Fatalf("swapped scores = %d/%d, want 2/3", redScore, blueScore)
		}
		start = start.Add(100 * time.Second)
		room.CompleteRoundDelay(false, start)
	}
	if roomTeam(first) != 2 || roomTeam(second) != 1 {
		t.Fatalf("teams after halftime = %d/%d", roomTeam(first), roomTeam(second))
	}
	// Team 1 still attacks: now that is the second player.
	if room.RecordBombPlant(first, firstSlot, start.Add(5*time.Second)) {
		t.Fatal("the new defender planted")
	}
	if !room.RecordBombPlant(second, secondSlot, start.Add(5*time.Second)) {
		t.Fatal("the new attacker could not plant")
	}
}

// A team reaching the win count ends the match even before halftime.
func TestDestructionMatchEndsBeforeHalftime(t *testing.T) {
	room := newRoom(9, RoomConfig{Capacity: 8, Mode: 4, Wins: 3, Time: 90, WeaponFlags: destructionSideSwitchFlag})
	red := &Session{RoomNumber: -1, RoomSlot: -1}
	redSlot, _ := room.Add(red)
	room.Add(&Session{RoomNumber: -1, RoomSlot: -1})
	room.BeginMatch()
	start := time.Unix(1_000, 0)
	room.BeginRound(start)
	for ended := 1; ended <= 3; ended++ {
		room.RecordBombPlant(red, redSlot, start.Add(5*time.Second))
		_, endGame, _ := room.BombTimeout(start.Add(60 * time.Second))
		if endGame != (ended == 3) {
			t.Fatalf("round %d end=%v", ended, endGame)
		}
		room.FinishRound(endGame)
		start = start.Add(100 * time.Second)
		room.CompleteRoundDelay(endGame, start)
	}
}

// Without the create dialog's TeamChangeRoleOption box (weapon flag 0x04) a
// normal-channel room never swaps.
func TestDestructionKeepsSidesWithoutTheOption(t *testing.T) {
	room := newRoom(10, RoomConfig{Capacity: 8, Mode: 4, Wins: 9, Time: 90})
	room.Add(&Session{RoomNumber: -1, RoomSlot: -1})
	room.Add(&Session{RoomNumber: -1, RoomSlot: -1})
	room.BeginMatch()
	start := time.Unix(1_000, 0)
	room.BeginRound(start)
	for ended := 1; ended <= 8; ended++ {
		room.RoundTimeout(start.Add(91 * time.Second))
		room.FinishRound(false)
		if switched, _, _ := room.CheckAndTriggerSwitchTeam(); switched {
			t.Fatalf("sides swapped after round %d without the option", ended)
		}
		start = start.Add(100 * time.Second)
		room.CompleteRoundDelay(false, start)
	}
}

func roomTeam(session *Session) byte {
	session.mu.RLock()
	defer session.mu.RUnlock()
	return session.RoomTeam
}

// The Pride Battle channel plays fixed lengths (sub_342B2370).
func TestPrideBattleFixedRules(t *testing.T) {
	for _, test := range []struct {
		mode         uint32
		wins, points byte
		pointsWant   uint16
	}{
		{mode: 0x14, wins: 6},
		{mode: 0x30, wins: 6},
		{mode: 0x11, wins: 6},
		{mode: 0x12, pointsWant: 160},
	} {
		config := prideBattleFixedRules(RoomConfig{Mode: test.mode, Wins: 3, Points: 30})
		if test.wins != 0 && config.Wins != test.wins {
			t.Fatalf("mode 0x%x wins = %d, want %d", test.mode, config.Wins, test.wins)
		}
		if test.pointsWant != 0 && config.Points != test.pointsWant {
			t.Fatalf("mode 0x%x points = %d, want %d", test.mode, config.Points, test.pointsWant)
		}
	}
}
