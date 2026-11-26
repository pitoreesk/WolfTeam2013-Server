package game

import (
	"fmt"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

func TutorialRewardPayload(track, level byte, rewards []store.TutorialReward) ([]byte, error) {
	if len(rewards) > 255 {
		return nil, fmt.Errorf("tutorial reward count exceeds one-byte protocol limit")
	}
	writer := protocol.NewWriter(5 + len(rewards)*4).U8(0)
	if len(rewards) == 0 {
		return EmptyTutorialRewardPayload(), nil
	}
	writer.U8(1).U16(uint16(track)<<8 | uint16(level)).U8(byte(len(rewards)))
	for _, reward := range rewards {
		if reward.ItemID < 1 || reward.ItemID > 0xffff || reward.Days < 0 || reward.Days > 0xffff {
			return nil, fmt.Errorf("tutorial reward is outside protocol range")
		}
		writer.U16(uint16(reward.ItemID)).U16(uint16(reward.Days))
	}
	return writer.Clone(), nil
}

func EmptyTutorialRewardPayload() []byte { return []byte{0, 0} }

// 2012 Battle Room chapters (tutorialmissionbutton.txt): buttons 52001-52005
// are Human 1-2 and Wolf 1-3. CS_CH_TUTORIALCLEAR_LEVEL_ACK treats human 2 /
// wolf 3 as "all cleared".
const (
	tutorialHumanChapters = 2
	tutorialWolfChapters  = 3
	tutorialChapterCount  = tutorialHumanChapters + tutorialWolfChapters
	tutorialMaxGiveItems  = 4 // CS_IN_TUTORIALREWARDGIVE_ACK parses at most 4 records
)

// tutorialChapter maps a chapter button index (0-4) to its track and level.
func tutorialChapter(index int) (track, level byte) {
	if index < tutorialHumanChapters {
		return 1, byte(index + 1)
	}
	return 2, byte(index - tutorialHumanChapters + 1)
}

func tutorialMaxLevel(track byte) byte {
	if track == 1 {
		return tutorialHumanChapters
	}
	return tutorialWolfChapters
}

// TutorialRewardListPayload encodes 2012 CS_FD_TUTORIALREWARD_ACK
// (sub_341CE500): u8 n, then per entry u16 (skipped), u8 m, m x {u16 item,
// u16 days}. The client stores entry i as the reward list of chapter button
// i (sub_34252AC0 shows the list of the selected chapter), so one entry is
// sent per chapter in button order.
func TutorialRewardListPayload(chapters [tutorialChapterCount][]store.TutorialReward) ([]byte, error) {
	writer := protocol.NewWriter(64).U8(0).U8(tutorialChapterCount)
	for index, rewards := range chapters {
		if len(rewards) > tutorialMaxGiveItems {
			rewards = rewards[:tutorialMaxGiveItems]
		}
		track, level := tutorialChapter(index)
		writer.U16(uint16(track)<<8 | uint16(level)).U8(byte(len(rewards)))
		for _, reward := range rewards {
			if reward.ItemID < 1 || reward.ItemID > 0xffff || reward.Days < 0 || reward.Days > 0xffff {
				return nil, fmt.Errorf("tutorial reward is outside protocol range")
			}
			writer.U16(uint16(reward.ItemID)).U16(uint16(reward.Days))
		}
	}
	return writer.Clone(), nil
}

func TutorialRewardGivePayload(items []store.Item) ([]byte, error) {
	// Tutorial rewards reuse the counted normal-item record.
	return ItemListPayload(items)
}

func TutorialClearPayload(progress store.TutorialProgress) []byte {
	return []byte{0, 2, 1, progress.HumanLevel, 2, progress.WolfLevel}
}

func tutorialTrack(mode byte) byte {
	switch mode {
	case 9:
		return 1
	case 0x13:
		return 2
	default:
		return 0
	}
}
