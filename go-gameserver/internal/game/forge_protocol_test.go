package game

import (
	"encoding/binary"
	"testing"
	"time"

	"wtserver-go/internal/store"
)

func TestForgePacketLayoutsMatchProtocol(t *testing.T) {
	recipes := make([]store.ForgeRecipe, 0, forgeRoundCount*forgeChoicesPerRound)
	for round := range forgeRoundCount {
		for choice := range forgeChoicesPerRound {
			recipes = append(recipes, store.ForgeRecipe{
				Category: 2, Challenge: byte(round), Choice: byte(choice), CostCash: 500,
				RewardItem: 1002 + choice, RewardDays: (round + 1) * 7,
			})
		}
	}
	payload, err := ForgeItemListPayload(2, recipes)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != 217 || payload[0] != 0 || payload[1] != 2 || binary.LittleEndian.Uint32(payload[2:6]) != 500 || payload[6] != 5 || payload[7] != 0 || payload[8] != 10 || binary.LittleEndian.Uint16(payload[9:11]) != 1002 || payload[49] != 1 || payload[50] != 10 {
		t.Fatalf("forge item list = %x", payload)
	}

	request, err := ParseForgeChallengeRequest([]byte{2, 3, 1})
	if err != nil {
		t.Fatal(err)
	}
	challenge := ForgeChallengePayload(request, store.ForgeResult{Outcome: 1, Cash: 50, RewardItem: 12078, RewardDays: 7})
	if request.Category != 2 || request.Challenge != 3 || request.Action != 1 {
		t.Fatalf("forge request = %+v", request)
	}
	if len(challenge) != 13 || challenge[0] != 0 || challenge[1] != 2 || challenge[2] != 3 || binary.LittleEndian.Uint32(challenge[3:7]) != 50 || challenge[7] != 1 || binary.LittleEndian.Uint16(challenge[8:10]) != 12078 || challenge[10] != 0 || binary.LittleEndian.Uint16(challenge[11:13]) != 7 { // 2012: byte 10 = period type 0 (days)
		t.Fatalf("forge challenge = %x", challenge)
	}
	// 2012: a broken roll (outcome 0) carries item 0 so the client shows "broken".
	failure := ForgeChallengePayload(request, store.ForgeResult{RewardItem: 12078, RewardDays: 7})
	if failure[0] != 0 || failure[1] != request.Category || failure[2] != request.Challenge || failure[7] != 0 || binary.LittleEndian.Uint16(failure[8:10]) != 0 {
		t.Fatalf("forge failure = %x", failure)
	}

	winners, err := ForgeWinnersPayload(2, []store.ForgeWinner{{Callsign: "A", RewardItem: 12078, Category: 2, Days: 7, WonAt: time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC)}})
	if err != nil {
		t.Fatal(err)
	}
	if winners[0] != 0 || winners[1] != 2 || winners[2] != 1 || winners[3] != 2 || winners[4] != 'A' || binary.LittleEndian.Uint16(winners[6:8]) != 12078 || winners[11] != 14 {
		t.Fatalf("forge winners = %x", winners)
	}
}
