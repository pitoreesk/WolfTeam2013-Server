package game

import (
	"testing"

	"wtserver-go/internal/store"
)

func TestIsPrideBattleMatchRequiresEveryHumanPlayerOnPrideChannel(t *testing.T) {
	pridePlayer := &Session{}
	pridePlayer.setChannelType(store.ChannelTypePrideBattle)
	ladderPlayer := &Session{}
	ladderPlayer.setChannelType(store.ChannelTypeLadder)
	normalPlayer := &Session{}
	bot := &Session{IsBot: true}

	tests := []struct {
		name    string
		players []MatchPlayer
		want    bool
	}{
		{name: "empty", want: false},
		{name: "normal player", players: []MatchPlayer{{Session: normalPlayer}}, want: false},
		{name: "pride players", players: []MatchPlayer{{Session: pridePlayer}, {Session: pridePlayer}}, want: true},
		{name: "ladder players", players: []MatchPlayer{{Session: ladderPlayer}, {Session: ladderPlayer}}, want: true},
		{name: "pride players with bot", players: []MatchPlayer{{Session: pridePlayer}, {Session: bot}}, want: true},
		{name: "mixed channels", players: []MatchPlayer{{Session: pridePlayer}, {Session: normalPlayer}}, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isPrideBattleMatch(test.players); got != test.want {
				t.Fatalf("isPrideBattleMatch() = %v, want %v", got, test.want)
			}
		})
	}
}
