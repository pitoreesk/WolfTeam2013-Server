package game

import (
	"fmt"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

const (
	forgeRoundCount      = 5
	forgeChoicesPerRound = 10

	// 2012 forge result codes handled by WShopSection (sub_341C45D0).
	forgeResultFailed = 97 // string 9457: item list could not be obtained
	forgeResultClosed = 99 // string 9458: "EDS Kapalı"
)

// ForgeItemListPayload encodes an entry price followed by five fixed round
// lists. Every transmitted round contains ten choices.
func ForgeItemListPayload(category byte, recipes []store.ForgeRecipe) ([]byte, error) {
	type roundRecipes struct {
		items   [forgeChoicesPerRound]store.ForgeRecipe
		present [forgeChoicesPerRound]bool
	}

	var rounds [forgeRoundCount]roundRecipes
	entryPrice := 0
	priceSet := false
	for _, recipe := range recipes {
		if recipe.Challenge >= forgeRoundCount || recipe.Choice >= forgeChoicesPerRound {
			return nil, fmt.Errorf("forge recipe round or choice is outside protocol range")
		}
		if recipe.CostCash < 0 || recipe.RewardItem < 1 || recipe.RewardItem > 65535 || recipe.RewardDays < 0 || recipe.RewardDays > 65535 {
			return nil, fmt.Errorf("forge recipe is outside protocol range")
		}
		round := &rounds[recipe.Challenge]
		if round.present[recipe.Choice] {
			return nil, fmt.Errorf("forge recipe round %d choice %d is duplicated", recipe.Challenge, recipe.Choice)
		}
		round.items[recipe.Choice] = recipe
		round.present[recipe.Choice] = true
		if !priceSet || (recipe.Challenge == 0 && recipe.Choice == 0) {
			entryPrice = recipe.CostCash
			priceSet = true
		}
	}

	completeRounds := make([]byte, 0, forgeRoundCount)
	for roundIndex := range rounds {
		complete := true
		for _, present := range rounds[roundIndex].present {
			if !present {
				complete = false
				break
			}
		}
		if complete {
			completeRounds = append(completeRounds, byte(roundIndex))
		}
	}

	// The common ACK layer consumes the leading success byte.
	writer := protocol.NewWriter(7 + len(completeRounds)*(2+forgeChoicesPerRound*4)).U8(0).U8(category).U32(uint32(entryPrice)).U8(byte(len(completeRounds)))
	for _, roundIndex := range completeRounds {
		writer.U8(roundIndex).U8(forgeChoicesPerRound)
		for choice := range forgeChoicesPerRound {
			recipe := rounds[roundIndex].items[choice]
			writer.U16(uint16(recipe.RewardItem)).U16(uint16(recipe.RewardDays))
		}
	}
	return writer.Clone(), nil
}

type ForgeChallengeRequest struct {
	Category  byte
	Challenge byte
	Action    byte
}

func ParseForgeChallengeRequest(payload []byte) (ForgeChallengeRequest, error) {
	if len(payload) < 3 {
		return ForgeChallengeRequest{}, fmt.Errorf("forge challenge payload is too short")
	}
	return ForgeChallengeRequest{Category: payload[0], Challenge: payload[1], Action: payload[2]}, nil
}

func ForgeChallengePayload(request ForgeChallengeRequest, result store.ForgeResult) []byte {
	// The common ACK layer consumes the leading success byte.
	writer := protocol.NewWriter(13).U8(0)
	// The first packet-specific byte is the Forge currency category, followed
	// by the round index.
	writer.U8(request.Category).U8(request.Challenge).U32(uint32(result.Cash))
	// Outcome: 0 = broken, 1 = passed, 2 = collected. The 2012 client
	// (sub_342B0AC0 / sub_34245770) decides "broken" only by the item not being
	// in the round's panel, so a broken roll carries item 0. The byte after the
	// item is the period type (0 = days, "%s Gün"), not the echoed action.
	item, days := result.RewardItem, result.RewardDays
	if result.Outcome == 0 {
		item, days = 0, 0
	}
	writer.U8(result.Outcome).U16(uint16(max(0, item))).U8(0).U16(uint16(max(0, days)))
	return writer.Clone()
}

func ForgeWinnersPayload(category byte, winners []store.ForgeWinner) ([]byte, error) {
	if len(winners) > 255 {
		return nil, fmt.Errorf("forge winner count exceeds one-byte limit")
	}
	// The common ACK layer consumes the leading success byte.
	writer := protocol.NewWriter(3 + len(winners)*24).U8(0).U8(category).U8(byte(len(winners)))
	for _, winner := range winners {
		// The 2012 client copies the callsign into a 26-byte stack buffer
		// (sub_342B0380): at most 12 UTF-16 units.
		units := utf16.Encode([]rune(winner.Callsign))
		if len(units) > 12 {
			units = units[:12]
		}
		if winner.RewardItem < 1 || winner.RewardItem > 65535 {
			return nil, fmt.Errorf("forge winner is outside protocol range")
		}
		writer.U8(byte(len(units) * 2))
		for _, unit := range units {
			writer.U16(unit)
		}
		writer.U16(uint16(winner.RewardItem)).U8(winner.Category).U16(uint16(max(0, winner.Days)))
		stamp := winner.WonAt.UTC().Format("20060102150405")
		writer.U8(byte(len(stamp))).ASCII(stamp)
	}
	return writer.Clone(), nil
}
