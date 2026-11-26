package game

import (
	"bytes"
	"testing"

	"wtserver-go/internal/store"
)

// TestTutorialRewardList2012 translates CS_FD_TUTORIALREWARD_ACK
// (sub_341CE500): u8 n; per entry u16 skipped, u8 m, m x {u16 item, u16 days}.
// Entry i is the reward list of Battle Room chapter button i.
func TestTutorialRewardList2012(t *testing.T) {
	var chapters [tutorialChapterCount][]store.TutorialReward
	chapters[0] = []store.TutorialReward{{ItemID: 1013, Days: 7}}
	chapters[4] = []store.TutorialReward{{ItemID: 3038, Days: 30}, {ItemID: 2001, Days: 0}}
	payload, err := TutorialRewardListPayload(chapters)
	if err != nil {
		t.Fatal(err)
	}
	r := newReader2012(t, payload)
	n := r.u8()
	if n != tutorialChapterCount {
		t.Fatalf("entries = %d", n)
	}
	got := make([][][2]uint16, n)
	keys := make([]uint16, n)
	for i := range got {
		keys[i] = r.u16()
		for m := r.u8(); m > 0; m-- {
			got[i] = append(got[i], [2]uint16{r.u16(), r.u16()})
		}
	}
	r.done()
	if keys[0] != 0x0101 || keys[1] != 0x0102 || keys[2] != 0x0201 || keys[4] != 0x0203 {
		t.Fatalf("chapter keys = %x", keys)
	}
	if len(got[0]) != 1 || got[0][0] != [2]uint16{1013, 7} || len(got[1]) != 0 ||
		len(got[4]) != 2 || got[4][0] != [2]uint16{3038, 30} {
		t.Fatalf("chapter rewards = %v", got)
	}
}

func TestTutorialChapterMapping2012(t *testing.T) {
	want := [][2]byte{{1, 1}, {1, 2}, {2, 1}, {2, 2}, {2, 3}}
	for index, pair := range want {
		if track, level := tutorialChapter(index); track != pair[0] || level != pair[1] {
			t.Fatalf("chapter %d = %d/%d", index, track, level)
		}
	}
	if tutorialMaxLevel(1) != 2 || tutorialMaxLevel(2) != 3 {
		t.Fatal("max levels")
	}
	// CS_CH_TUTORIALCLEAR_LEVEL_ACK (sub_341CB1B0): u8 n, n x {u8 track, u8 level}.
	if got := TutorialClearPayload(store.TutorialProgress{HumanLevel: 2, WolfLevel: 1}); !bytes.Equal(got, []byte{0, 2, 1, 2, 2, 1}) {
		t.Fatalf("progress = %x", got)
	}
}
