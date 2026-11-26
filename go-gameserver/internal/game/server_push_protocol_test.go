package game

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestEventAndAccountPushOpcodes(t *testing.T) {
	want := map[uint16]uint16{
		ReplyChangeTotalGrade: 0x163f,
		ReplyEventKillReward:  0x1655,
		ReplyPBFieldName:      0x1657,
		ReplyGamingUserKick:   0x166d,
		ReplyRewardPlaytime:   0x16a7,
	}
	for got, expected := range want {
		if got != expected {
			t.Fatalf("reply opcode %#x, want %#x", got, expected)
		}
	}
}

func TestEventAndAccountPushLayouts(t *testing.T) {
	if got := ChangeTotalGradePayload(2, 7); !bytes.Equal(got, []byte{0, 2, 7}) {
		t.Fatalf("grade payload = %x", got)
	}
	field, err := PBFieldNamePayload("A")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(field, []byte{0, 2, 'A', 0}) {
		t.Fatalf("field-name payload = %x", field)
	}
	if got := GamingUserKickPayload(4); !bytes.Equal(got, []byte{0, 4}) {
		t.Fatalf("gaming-user-kick payload = %x", got)
	}
	played := RewardPlaytimePayload(824349, 500)
	if len(played) != 9 || binary.LittleEndian.Uint32(played[1:5]) != 824349 || binary.LittleEndian.Uint32(played[5:9]) != 500 {
		t.Fatalf("playtime payload = %x", played)
	}
	reward, err := EventKillRewardPayload([]EventKillReward{{
		EventID: 9,
		Values:  [3]uint32{10, 20, 30},
		Items:   []EventKillItem{{ItemID: 17001, Variant: 2, Days: 7}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reward) != 24 || reward[1] != 1 || binary.LittleEndian.Uint32(reward[2:6]) != 9 || reward[18] != 1 || binary.LittleEndian.Uint16(reward[19:21]) != 17001 || reward[21] != 2 || binary.LittleEndian.Uint16(reward[22:24]) != 7 {
		t.Fatalf("event-kill reward payload = %x", reward)
	}
}
