package ptbonus

import "testing"

func TestTableAndReward(t *testing.T) {
	tiers := Tiers()
	if len(tiers) != 48 || tiers[0] != (Tier{Kind: 2, Minutes: 30, Gold: 3000}) || tiers[4].Gold != 1000 || tiers[47].Minutes != 1440 {
		t.Fatalf("tiers = %d %+v %+v", len(tiers), tiers[0], tiers[47])
	}
	if gold, reached := Reward(tiers, 0, 29*60); gold != 0 || reached != 0 {
		t.Fatalf("below first tier = %d/%d", gold, reached)
	}
	if gold, reached := Reward(tiers, 0, 150*60); gold != 4*3000+1000 || reached != 5 {
		t.Fatalf("150 minutes = %d/%d", gold, reached)
	}
	if gold, reached := Reward(tiers, 5, 150*60); gold != 0 || reached != 5 {
		t.Fatalf("already paid = %d/%d", gold, reached)
	}
}
