package game

import (
	"fmt"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
)

func ChangeTotalGradePayload(slot, grade byte) []byte {
	return []byte{0, slot, grade}
}

func PBFieldNamePayload(name string) ([]byte, error) {
	units := utf16.Encode([]rune(name))
	if len(units)*2 > 255 {
		return nil, fmt.Errorf("field name exceeds one-byte UTF-16 length")
	}
	writer := protocol.NewWriter(2 + len(units)*2).U8(0).U8(byte(len(units) * 2)).UTF16LE(name)
	return writer.Clone(), nil
}

func GamingUserKickPayload(slot byte) []byte {
	return []byte{0, slot}
}

func RewardPlaytimePayload(goldBalance, reward uint32) []byte {
	return protocol.NewWriter(9).U8(0).U32(goldBalance).U32(reward).Clone()
}

type EventKillItem struct {
	ItemID  uint16
	Variant byte
	Days    uint16
}

type EventKillReward struct {
	EventID uint32
	Values  [3]uint32
	Items   []EventKillItem
}

func EventKillRewardPayload(rewards []EventKillReward) ([]byte, error) {
	if len(rewards) > 255 {
		return nil, fmt.Errorf("event-kill reward count exceeds one-byte protocol limit")
	}
	writer := protocol.NewWriter(2 + len(rewards)*18).U8(0).U8(byte(len(rewards)))
	for _, reward := range rewards {
		if len(reward.Items) > 255 {
			return nil, fmt.Errorf("event %d item count exceeds one-byte protocol limit", reward.EventID)
		}
		writer.U32(reward.EventID)
		for _, value := range reward.Values {
			writer.U32(value)
		}
		writer.U8(byte(len(reward.Items)))
		for _, item := range reward.Items {
			writer.U16(item.ItemID).U8(item.Variant).U16(item.Days)
		}
	}
	return writer.Clone(), nil
}
