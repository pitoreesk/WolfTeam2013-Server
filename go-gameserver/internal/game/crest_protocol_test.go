package game

import (
	"bytes"
	"encoding/binary"
	"testing"

	"wtserver-go/internal/store"
)

func TestCrestSinkPayloadLayout(t *testing.T) {
	if RequestCrestSink != 0x1698 || ReplyCrestSink != 0x1699 {
		t.Fatal("crest-sink opcode drifted from the compatibility contract")
	}
	payload, err := CrestSinkPayload([]store.CrestActiveChallenge{
		{CrestID: 0x1234, Progress: 500},
		{CrestID: 9, Progress: 0x10203040},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != 16 || payload[0] != 0 || payload[1] != 0 || payload[2] != 0 || payload[3] != 2 {
		t.Fatalf("crest-sink header = %x", payload)
	}
	if binary.LittleEndian.Uint16(payload[4:6]) != 0x1234 || binary.LittleEndian.Uint32(payload[6:10]) != 500 ||
		binary.LittleEndian.Uint16(payload[10:12]) != 9 || binary.LittleEndian.Uint32(payload[12:16]) != 0x10203040 {
		t.Fatalf("crest-sink entries = %x", payload)
	}
	if _, err := CrestSinkPayload(make([]store.CrestActiveChallenge, 256)); err == nil {
		t.Fatal("accepted more than 255 crest-progress entries")
	}
}

// Go translation of sub_3418A440 (0x134D): skip 2, u8 n x {u8, u16},
// u16 m x {u16, u16, u8 flag, [u32]}; checks exact consumption.
func TestCrestUserPayloadMatchesClientParser(t *testing.T) {
	payload, err := CrestUserPayload([]store.CrestDelegateSlot{{Slot: 1, CrestID: 3004}},
		[]CrestUserEntry{{CrestID: 3004, Percent: 100}, {CrestID: 1002, Active: true, Progress: 17}})
	if err != nil {
		t.Fatal(err)
	}
	pos := 3
	n := int(payload[pos])
	pos++
	if n != 1 || payload[pos] != 1 || binary.LittleEndian.Uint16(payload[pos+1:]) != 3004 {
		t.Fatalf("delegates = %x", payload)
	}
	pos += 3 * n
	m := int(binary.LittleEndian.Uint16(payload[pos:]))
	pos += 2
	var progress uint32
	for i := 0; i < m; i++ {
		flag := payload[pos+4]
		pos += 5
		if flag != 0 {
			progress = binary.LittleEndian.Uint32(payload[pos:])
			pos += 4
		}
	}
	if m != 2 || progress != 17 || pos != len(payload) {
		t.Fatalf("entries m=%d progress=%d consumed=%d len=%d", m, progress, pos, len(payload))
	}
}

func TestCrestRequestAndReplyLayouts(t *testing.T) {
	if slot, crest, err := ParseCrestEquipRequest([]byte{2, 0xbc, 0x0b, 0xee}); err != nil || slot != 2 || crest != 3004 {
		t.Fatalf("equip request = %d %d %v", slot, crest, err)
	}
	if got := CrestEquipPayload([]store.CrestDelegateSlot{{Slot: 0, CrestID: 1001}}); !bytes.Equal(got, []byte{0, 0, 0, 1, 0, 0xe9, 0x03}) {
		t.Fatalf("equip reply = %x", got)
	}
	crests, err := ParseCrestChallengeRequest([]byte{2, 0xe9, 0x03, 0xbc, 0x0b, 0xaa})
	if err != nil || len(crests) != 2 || crests[0] != 1001 || crests[1] != 3004 {
		t.Fatalf("challenge request = %v %v", crests, err)
	}
	if _, err := ParseCrestChallengeRequest([]byte{3, 1, 2}); err == nil {
		t.Fatal("accepted a truncated challenge list")
	}
	if got := CrestChallengePayload(95000, []uint16{1001}); !bytes.Equal(got, []byte{0, 0, 0, 0x18, 0x73, 0x01, 0, 1, 0xe9, 0x03, 0, 0}) {
		t.Fatalf("challenge reply = %x", got)
	}
	if got := CrestCancelPayload(1001); !bytes.Equal(got, []byte{0, 0, 0, 0xe9, 0x03}) {
		t.Fatalf("cancel reply = %x", got)
	}
	if got := CrestRewardPayload(450, 3004); !bytes.Equal(got, []byte{0, 0, 0, 0xc2, 0x01, 0, 0, 0xbc, 0x0b, 100, 0}) {
		t.Fatalf("reward = %x", got)
	}
	if RequestCrestEquip != 0x134e || ReplyCrestEquip != 0x134f || RequestCrestChallenge != 0x1352 ||
		ReplyCrestChallenge != 0x1353 || RequestCrestCancel != 0x1358 || ReplyCrestCancel != 0x1359 || ReplyCrestReward != 0x145f {
		t.Fatal("crest opcodes drifted")
	}
}
