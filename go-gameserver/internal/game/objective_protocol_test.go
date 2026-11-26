package game

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func TestRecoveredObjectivePayloadLayouts(t *testing.T) {
	if RequestWeaponKill != 0x1672 || ReplyWeaponKill != 0x1673 || RequestDestroyTower != 0x1680 || ReplyDestroyTower != 0x1681 || RequestFlagPointGet != 0x168a || ReplyFlagPointGet != 0x168b {
		t.Fatal("recovered objective packet IDs changed")
	}
	if payload := WeaponKillPayload(); !bytes.Equal(payload, []byte{0}) {
		t.Fatalf("weapon-kill payload = %x", payload)
	}
	tower := DestroyTowerPayload(12, 34)
	if !bytes.Equal(tower, []byte{0, 12, 0, 34, 0}) {
		t.Fatalf("destroy-tower payload = %x", tower)
	}
	flag := FlagPointGetPayload(3, 7, 2, 19)
	if !bytes.Equal(flag, []byte{0, 3, 7, 2, 19}) {
		t.Fatalf("flag-point payload = %x", flag)
	}
	price := ItemPriceInfoPayload(nil) // no overrides: 0/0 versions, empty list
	if len(price) != 10 ||
		binary.LittleEndian.Uint32(price[1:5]) != itemPriceVersionA ||
		binary.LittleEndian.Uint32(price[5:9]) != itemPriceVersionB ||
		price[9] != 0 {
		t.Fatalf("item-price-info payload = %x", price)
	}
}

func TestPrivateLadderBossOutLayout(t *testing.T) {
	if RequestLadderBossOut != 0x1684 || ReplyLadderBossOut != 0x1685 {
		t.Fatal("private-ladder boss-out opcode drifted from the compatibility contract")
	}
	payload := LadderBossOutPayload()
	if !bytes.Equal(payload, []byte{0}) {
		t.Fatalf("private-ladder boss-out payload = %x", payload)
	}
}

func TestObjectivePacketsUseReportedActorAndUpdateScores(t *testing.T) {
	room := newRoom(20, RoomConfig{Capacity: 8, Mode: 1, Points: 100, Time: 600})
	red := &Session{RoomNumber: -1, RoomSlot: -1}
	blue := &Session{RoomNumber: -1, RoomSlot: -1}
	if _, ok := room.Add(red); !ok {
		t.Fatal("add red player")
	}
	blueSlot, ok := room.Add(blue)
	if !ok {
		t.Fatal("add blue player")
	}
	beginTestMatch(room)
	room.BeginRound(time.Unix(8_000, 0))

	// RED's crystal broke: Blue scores 70 (the HUD's fixed "+70 Points").
	redScore, blueScore, ok := room.RecordTowerDestroyed(red, 1)
	if !ok || redScore != 0 || blueScore != 70 {
		t.Fatalf("tower score = ok=%v score=%d/%d", ok, redScore, blueScore)
	}
	if _, _, ok := room.RecordTowerDestroyed(red, 0); ok {
		t.Fatal("tower report without a broken team was accepted")
	}
	// Pickup (event 1) is worth 1, the capture (event 4) 8, both for the
	// actor's own team whatever byte the client sends.
	personal, team, total, reached, ok := room.RecordFlagPoint(red, blueSlot, flagEventPickup)
	if !ok || team != 2 || personal != 1 || total != 71 || reached || room.BlueScore != 71 {
		t.Fatalf("flag pickup = ok=%v team=%d personal=%d total=%d score=%d/%d", ok, team, personal, total, room.RedScore, room.BlueScore)
	}
	personal, team, total, reached, ok = room.RecordFlagPoint(red, blueSlot, flagEventCapture)
	if !ok || team != 2 || personal != 9 || total != 79 || reached || room.RedScore != 0 || room.BlueScore != 79 {
		t.Fatalf("flag capture = ok=%v team=%d personal=%d total=%d score=%d/%d", ok, team, personal, total, room.RedScore, room.BlueScore)
	}
	if _, _, _, _, ok := room.RecordFlagPoint(red, blueSlot, 3); ok {
		t.Fatal("unknown flag event was accepted")
	}
	room.BlueScore = 95
	if _, _, total, reached, ok := room.RecordFlagPoint(red, blueSlot, flagEventCapture); !ok || total != 103 || !reached {
		t.Fatalf("capture reaching the target = ok=%v total=%d reached=%v", ok, total, reached)
	}
}

func TestCrystalModeKillScoresOnePoint(t *testing.T) {
	room := newRoom(21, RoomConfig{Capacity: 2, Mode: 0x2410, Points: 100, Time: 600})
	red := &Session{RoomNumber: -1, RoomSlot: -1}
	blue := &Session{RoomNumber: -1, RoomSlot: -1}
	room.Add(red)
	room.Add(blue)
	beginTestMatch(room)
	room.BeginRound(time.Unix(8_000, 0))
	result := room.RecordKill(blue, red)
	if !result.Accepted || result.RedScore != 0 || result.BlueScore != 1 {
		t.Fatalf("crystal-mode kill = %+v", result)
	}
}
