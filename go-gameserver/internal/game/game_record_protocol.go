package game

import (
	"encoding/binary"
	"fmt"
	"unicode/utf16"
)

// GameRecordRequest carries killer and victim slots with a one-byte display
// kind for each, followed by a one-byte UTF-16 code-unit count and localized
// game-record text.
type GameRecordRequest struct {
	Killer     byte
	KillerKind byte
	Victim     byte
	VictimKind byte
	Text       string
}

func ParseGameRecordRequest(payload []byte) (GameRecordRequest, error) {
	if len(payload) < 5 {
		return GameRecordRequest{}, fmt.Errorf("game-record payload is too short")
	}
	unitCount := int(payload[4])
	required := 5 + unitCount*2
	if len(payload) < required {
		return GameRecordRequest{}, fmt.Errorf("game-record text requires %d bytes, got %d", required, len(payload))
	}
	units := make([]uint16, unitCount)
	for index := range units {
		offset := 5 + index*2
		units[index] = binary.LittleEndian.Uint16(payload[offset : offset+2])
	}
	return GameRecordRequest{
		Killer:     payload[0],
		KillerKind: payload[1],
		Victim:     payload[2],
		VictimKind: payload[3],
		Text:       string(utf16.Decode(units)),
	}, nil
}
