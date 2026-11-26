package game

import "wtserver-go/internal/protocol"

// LastPlayerInfoPayload carries status, red slot, blue slot, red percentage,
// and blue percentage.
func LastPlayerInfoPayload(redSlot, blueSlot, redRate, blueRate byte) []byte {
	return protocol.NewWriter(5).U8(0).
		U8(redSlot).U8(blueSlot).
		U8(redRate).U8(blueRate).
		Clone()
}

// GamingKickPayload announces the initiator, target, and the selected reason.
func GamingKickPayload(initiator, target, reason byte) []byte {
	return protocol.NewWriter(4).U8(0).
		U8(initiator).U8(target).U8(reason).
		Clone()
}

// GamingKickVotePayload carries three live counters followed by a reserved
// byte.
func GamingKickVotePayload(status KickVoteStatus) []byte {
	return protocol.NewWriter(6).U8(0).
		U8(status.Target).
		U8(status.Agreement).U8(status.Opposition).U8(status.Waiver).
		U8(0).
		Clone()
}

// GamingKickResultPayload uses result 1 for a passed vote and 0 for failure.
func GamingKickResultPayload(status KickVoteStatus) []byte {
	return protocol.NewWriter(7).U8(0).
		U8(status.Target).Bool(status.Passed).
		U8(status.Agreement).U8(status.Opposition).U8(status.Waiver).
		U8(0).
		Clone()
}

func playerWinRate(wins, losses int) byte {
	if wins < 0 {
		wins = 0
	}
	if losses < 0 {
		losses = 0
	}
	total := int64(wins) + int64(losses)
	if total == 0 {
		return 0
	}
	return byte(int64(wins) * 100 / total)
}
