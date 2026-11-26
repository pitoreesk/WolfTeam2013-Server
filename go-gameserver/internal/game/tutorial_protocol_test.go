package game

import (
	"bytes"
	"encoding/binary"
	"testing"

	"wtserver-go/internal/store"
)

func TestTutorialOpcodes(t *testing.T) {
	if RequestTutorialClear != 0x1344 || ReplyTutorialClear != 0x1345 ||
		RequestTutorialGive != 0x144e || ReplyTutorialGive != 0x144f ||
		RequestTutorialReward != 0x1686 || ReplyTutorialReward != 0x1687 {
		t.Fatal("tutorial opcode drifted from the compatibility contract")
	}
}

func TestTutorialRewardPayloadLayout(t *testing.T) {
	payload, err := TutorialRewardPayload(1, 2, []store.TutorialReward{
		{ItemID: 1001, Days: 7},
		{ItemID: 2001, Days: 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != 13 || payload[0] != 0 || payload[1] != 1 || binary.LittleEndian.Uint16(payload[2:4]) != 0x0102 || payload[4] != 2 {
		t.Fatalf("tutorial reward header = %x", payload)
	}
	if binary.LittleEndian.Uint16(payload[5:7]) != 1001 || binary.LittleEndian.Uint16(payload[7:9]) != 7 ||
		binary.LittleEndian.Uint16(payload[9:11]) != 2001 || binary.LittleEndian.Uint16(payload[11:13]) != 30 {
		t.Fatalf("tutorial reward items = %x", payload)
	}
	empty, err := TutorialRewardPayload(2, 1, nil)
	if err != nil || !bytes.Equal(empty, EmptyTutorialRewardPayload()) {
		t.Fatalf("empty tutorial reward = %x, %v", empty, err)
	}
}

func TestTutorialGiveAndClearPayloads(t *testing.T) {
	give, err := TutorialRewardGivePayload([]store.Item{{ID: 1001, Expiry: "permanent"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(give) < 2 || give[0] != 0 || give[1] != 1 || binary.LittleEndian.Uint16(give[6:8]) != 1001 || give[len(give)-1] != 0 {
		t.Fatalf("tutorial give payload = %x", give)
	}
	clear := TutorialClearPayload(store.TutorialProgress{HumanLevel: 2, WolfLevel: 3})
	if len(clear) != 6 || clear[1] != 2 || clear[2] != 1 || clear[3] != 2 || clear[4] != 2 || clear[5] != 3 {
		t.Fatalf("tutorial clear payload = %x", clear)
	}
}

func TestTutorialTrackAndPendingClaim(t *testing.T) {
	if tutorialTrack(9) != 1 || tutorialTrack(0x13) != 2 || tutorialTrack(4) != 0 {
		t.Fatal("tutorial mode-to-track mapping changed")
	}
	session := &Session{}
	session.setPendingTutorial(2, 3)
	track, level, ok := session.takePendingTutorial()
	if !ok || track != 2 || level != 3 {
		t.Fatalf("pending tutorial = %d/%d ok %t", track, level, ok)
	}
	if _, _, ok := session.takePendingTutorial(); ok {
		t.Fatal("tutorial claim was reusable")
	}
}
