package store

import "testing"

func TestPrideBattlePointsFollowTheRankGap(t *testing.T) {
	for _, test := range []struct {
		winner, loser, want int
	}{
		{winner: 3, loser: 3, want: 10},  // equal ranks
		{winner: 9, loser: 1, want: 14},  // upset: 10 + 8/2
		{winner: 1, loser: 9, want: 6},   // favourite wins: 10 - 4
		{winner: 40, loser: 1, want: 15}, // clamped
		{winner: 1, loser: 40, want: 5},  // clamped
	} {
		if got := PrideBattlePoints(test.winner, test.loser); got != test.want {
			t.Fatalf("points(%d beats %d) = %d, want %d", test.winner, test.loser, got, test.want)
		}
	}
}

func TestPrideAcePicksTheTopScorer(t *testing.T) {
	members := []PrideBattleMemberEntry{
		{UserID: 1, Score: 4, Kills: 4, Deaths: 1},
		{UserID: 2, Score: 6, Kills: 2, Deaths: 3},
		{UserID: 3, Score: 6, Kills: 6, Deaths: 5},
		{UserID: 4, Score: 6, Kills: 6, Deaths: 2},
	}
	if ace := PrideAce(members); members[ace].UserID != 4 {
		t.Fatalf("ace = user %d, want 4 (score, then kills, then fewest deaths)", members[ace].UserID)
	}
	if PrideAce(nil) != -1 {
		t.Fatal("an empty side has an ace")
	}
}
