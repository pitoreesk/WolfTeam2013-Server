package game

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"wtserver-go/internal/store"
)

type channelRewardUsers struct {
	*fakeUsers
	channels []store.Channel
}

func (u *channelRewardUsers) Channels(context.Context) ([]store.Channel, error) {
	return append([]store.Channel(nil), u.channels...), nil
}

func TestRewardCatalogMatchesExpectedBonuses(t *testing.T) {
	if len(rewardItemBonuses) != 95 {
		t.Fatalf("reward bonus catalog has %d entries, want 95", len(rewardItemBonuses))
	}
	tests := map[int]rewardItemBonus{
		10015: {gpPercent: 60, group: rewardBonusBooster},
		10025: {goldPercent: 100, gpPercent: 100, group: rewardBonusBooster},
		10027: {goldPercent: 50, gpPercent: 50, group: rewardBonusPowerUser},
		13006: {gpPercent: 20, group: rewardBonusBackpack},
		30071: {goldPercent: 30, gpPercent: 30, group: rewardBonusCharacter},
		30080: {goldPercent: 40, group: rewardBonusCharacter},
	}
	for itemID, want := range tests {
		if got := rewardItemBonuses[itemID]; got != want {
			t.Errorf("item %d bonus = %+v, want %+v", itemID, got, want)
		}
	}
}

func TestMatchRewardCombinesActiveRewardSources(t *testing.T) {
	users := &fakeUsers{
		items: []store.Item{
			{ID: 10003},
			{ID: powerUserItemID},
			{ID: 30071, Type: 1},
		},
		loadout: store.CharacterSelection{BlueCharacter: 30071},
	}
	users.equipmentSlots[24] = 2_000_013_006
	server := &Server{
		users:  users,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	got := server.calculateMatchReward(context.Background(), MatchPlayer{
		Team:  1,
		Stats: MatchPlayerStats{Kills: 2},
	}, true, 42, rewardItemBonus{})
	want := matchReward{
		gold:       120,
		gp:         240,
		bonusGold:  132,
		bonusGP:    312,
		bonusFlags: endGameBonusGoldPlus | endGameBonusGPPlus | endGameBonusPowerUser,
	}
	if got != want {
		t.Fatalf("combined match reward = %+v, want %+v", got, want)
	}
}

func TestMatchRewardStacksDistinctBoostersOnce(t *testing.T) {
	users := &fakeUsers{
		items: []store.Item{
			{ID: 10001},
			{ID: 10003},
			{ID: 10015},
			{ID: 10015},
			{ID: 10025},
		},
	}
	server := &Server{
		users:  users,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	got := server.calculateMatchReward(context.Background(), MatchPlayer{}, false, 42, rewardItemBonus{})
	want := matchReward{
		gold:       50,
		gp:         100,
		bonusGold:  65,
		bonusGP:    220,
		bonusFlags: endGameBonusGoldPlus | endGameBonusGPPlus,
	}
	if got != want {
		t.Fatalf("booster match reward = %+v, want %+v", got, want)
	}
}

func TestChannelBurningBonusUsesConfiguredGPAndGoldPercentages(t *testing.T) {
	users := &channelRewardUsers{
		fakeUsers: &fakeUsers{},
		channels: []store.Channel{
			{Name: "Other", Port: 40708, GPBonusPercent: 90, GoldBonusPercent: 80},
			{Name: "Burning", Port: 40707, GPBonusPercent: 25, GoldBonusPercent: 40},
		},
	}
	server := &Server{
		matchPort: 40707,
		users:     users,
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	burning := server.channelBurningBonus(context.Background())
	if burning != (rewardItemBonus{
		goldPercent: 40,
		gpPercent:   25,
		group:       rewardBonusBurning,
	}) {
		t.Fatalf("channel Burning bonus = %+v", burning)
	}

	got := server.calculateMatchReward(context.Background(), MatchPlayer{}, false, 42, burning)
	want := matchReward{
		gold:       50,
		gp:         100,
		bonusGold:  20,
		bonusGP:    25,
		bonusFlags: endGameBonusBurning,
	}
	if got != want {
		t.Fatalf("Burning match reward = %+v, want %+v", got, want)
	}
}
