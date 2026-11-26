package game

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

func TestParseGameRecordRequest(t *testing.T) {
	units := utf16.Encode([]rune("Double kill"))
	payload := make([]byte, 5+len(units)*2)
	copy(payload, []byte{3, 2, 7, 1})
	payload[4] = byte(len(units))
	for index, unit := range units {
		binary.LittleEndian.PutUint16(payload[5+index*2:], unit)
	}
	record, err := ParseGameRecordRequest(payload)
	if err != nil {
		t.Fatal(err)
	}
	if record.Killer != 3 || record.KillerKind != 2 || record.Victim != 7 || record.VictimKind != 1 || record.Text != "Double kill" {
		t.Fatalf("game record = %+v", record)
	}
}

func TestParseGameRecordRequestRejectsTruncation(t *testing.T) {
	if _, err := ParseGameRecordRequest(make([]byte, 4)); err == nil {
		t.Fatal("accepted a missing game-record string length")
	}
	payload := make([]byte, 6)
	payload[4] = 2
	if _, err := ParseGameRecordRequest(payload); err == nil {
		t.Fatal("accepted truncated UTF-16 game-record text")
	}
}

func TestRoomValidatesGameRecordParticipants(t *testing.T) {
	room, players := activeVotingRoom(t, 2)
	record := GameRecordRequest{Killer: 0, Victim: 1}
	if !room.ValidateGameRecord(players[0], record) {
		t.Fatal("rejected valid game record")
	}
	record.Killer = 0xff
	if !room.ValidateGameRecord(players[0], record) {
		t.Fatal("rejected environmental game record")
	}
	record.Victim = 9
	if room.ValidateGameRecord(players[0], record) {
		t.Fatal("accepted missing victim")
	}
	room.FinishRound(false)
	record.Victim = 1
	if room.ValidateGameRecord(players[0], record) {
		t.Fatal("accepted game record outside an active round")
	}
}
