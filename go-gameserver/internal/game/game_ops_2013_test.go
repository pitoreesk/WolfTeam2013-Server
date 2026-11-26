package game

import (
	"testing"

	"wtserver-go/internal/store"
)

// TestEndGame2013 translates 2013 TR CS_FD_ENDGAME_ACK (sub_342E3C30): four
// u32 after the level and the weapon flags before the player count.
func TestEndGame2013(t *testing.T) {
	payload, err := EndGamePayload(EndGameResult{
		Info:            store.AccountSnapshot{Gold: 1234, GP: 5678, Level: 9},
		PreviousGP:      5378,
		PlayTimeSeconds: 300,
		RedPrideMarkID:  11,
		BluePrideMarkID: 22,
		RedPrideName:    "R",
		BluePrideName:   "Bluepridenamethatiswaytoolong",
		RedPridePoints:  33,
		BluePridePoints: 44,
		WeaponFlags:     0x44,
		Players: []EndGamePlayer{
			{Slot: 1, Team: 2, GoldReward: 100, GPReward: 200, BonusGold: 5, BonusGP: 6, BonusFlags: 7, Winner: true},
			{Slot: 2, Team: 1, GoldReward: 50, GPReward: 100},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := newReader2012(t, payload)
	gold, prevGP, gp, next, level := r.u32(), r.u32(), r.u32(), r.u32(), r.u8()
	var four [4]uint32
	for i := range four {
		four[i] = r.u32()
	}
	red, blue := r.utf16(), r.utf16()
	redPts, bluePts := r.u32(), r.u32()
	weapon, n := r.u8(), r.u8()
	type rec struct {
		slot                  byte
		gold, gp, bg, bgp, bf uint32
		winner, team          byte
	}
	recs := make([]rec, n)
	for i := range recs {
		recs[i] = rec{r.u8(), r.u32(), r.u32(), r.u32(), r.u32(), r.u32(), r.u8(), r.u8()}
	}
	r.done()
	if gold != 1234 || prevGP != uint32(store.LevelBaseThreshold(9)) || gp != 5678 || next != 10045000 || level != 9 ||
		four != [4]uint32{300, 0, 11, 22} || weapon != 0x44 || red != "R" || len(blue) != 19 ||
		redPts != 33 || bluePts != 44 || n != 2 {
		t.Fatalf("end-game header = %x", payload)
	}
	if recs[0] != (rec{1, 100, 200, 5, 6, 7, 1, 2}) || recs[1] != (rec{2, 50, 100, 0, 0, 0, 0, 1}) {
		t.Fatalf("end-game records = %+v", recs)
	}
}

// TestGamePoints2013 translates 2013 TR CS_FD_GAMEPOINT_ACK (sub_342067D0 +
// sub_3407BAE0).
func TestGamePoints2013(t *testing.T) {
	payload := GamePointsPayload(store.AccountSnapshot{Gold: 123, GP: 456, Level: 8, Kills: 12, Deaths: 3, PlayTimeSeconds: 77},
		GamePointSlot{BonusGold: 1, BonusGP: 2, BonusFlags: 3, Winner: true, Team: 2})
	if len(payload) != 46 {
		t.Fatalf("game-points length = %d", len(payload))
	}
	r := newReader2012(t, payload)
	gold, u32, gp, next, level := r.u32(), r.u32(), r.u32(), r.u32(), r.u8()
	playTime, ptBonus := r.u32(), r.u8()
	bonusGold, bonusGP, bonusFlags, winner, team := r.u32(), r.u32(), r.u32(), r.u8(), r.u8()
	kills, deaths := r.u32(), r.u32()
	records := r.u8()
	r.done()
	if gold != 123 || u32 != uint32(store.LevelBaseThreshold(8)) || gp != 456 || next != 10990000 || level != 8 || playTime != 77 || ptBonus != 0 || records != 0 ||
		bonusGold != 1 || bonusGP != 2 || bonusFlags != 3 || winner != 1 || team != 2 || kills != 12 || deaths != 3 {
		t.Fatalf("game-points payload = %x", payload)
	}
}
