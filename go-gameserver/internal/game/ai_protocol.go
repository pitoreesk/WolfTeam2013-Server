package game

import "wtserver-go/internal/protocol"

const (
	aiSlotLogicalSize = 22
	aiSlotIgnoredSize = 12
	aiSlotNamePresent = 1
)

func AddAISlotPayload(slot, column, teamRow, clientTeam, difficulty, botNumber, form byte) []byte {
	// Offset zero is the common result byte. Offsets 8..19 are reserved, and
	// the final field is at offset 21. Transport encryption pads this 22-byte
	// logical payload to the same 32-byte wire size.
	return protocol.NewWriter(aiSlotLogicalSize).
		U8(0). // common result
		U8(slot).
		U8(column).
		U8(teamRow).
		U8(clientTeam).
		U8(normalizeAIDifficulty(difficulty)).
		U8(botNumber).
		U8(aiSlotNamePresent).
		Bytes(make([]byte, aiSlotIgnoredSize)).
		U8(0). // alternate entity slot
		U8(normalizeAIForm(form)).
		Clone()
}

func DeleteAISlotPayload(slot byte) []byte {
	// The slot follows the common result byte. Framing supplies any remaining
	// zero padding on the wire.
	return protocol.NewWriter(2).U8(0).U8(slot).Clone()
}

func normalizeAIDifficulty(value byte) byte {
	if value >= 1 && value <= 3 {
		return value
	}
	return 1
}

func normalizeAIForm(value byte) byte {
	if value == 2 {
		return 2
	}
	return 1
}
