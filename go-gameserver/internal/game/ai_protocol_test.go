package game

import (
	"bytes"
	"testing"
)

func TestAddAISlotPayloadHasExpectedLayout(t *testing.T) {
	payload := AddAISlotPayload(3, 6, 1, 1, 2, 4, 2)
	if len(payload) != 22 || payload[0] != 0 || payload[1] != 3 || payload[2] != 6 || payload[3] != 1 || payload[4] != 1 || payload[5] != 2 || payload[6] != 4 || payload[7] != 1 || payload[20] != 0 || payload[21] != 2 {
		t.Fatalf("AI payload = %x", payload)
	}
	if !bytes.Equal(payload[8:20], make([]byte, 12)) {
		t.Fatalf("skipped AI fields are not zero: %x", payload[8:20])
	}
	if invalid := AddAISlotPayload(0, 0, 0, 0, 1, 1, 3); invalid[21] != 1 {
		t.Fatalf("invalid form was not clamped: %x", invalid)
	}
	deleted := DeleteAISlotPayload(3)
	if len(deleted) != 2 || deleted[0] != 0 || deleted[1] != 3 {
		t.Fatalf("delete-AI payload = %x", deleted)
	}
}

func TestRoomAddsAndPromotesF1Bots(t *testing.T) {
	room := newRoom(7, RoomConfig{Capacity: 8, Mode: 0x80})
	human := &Session{RoomNumber: -1, RoomSlot: -1}
	if _, ok := room.Add(human); !ok {
		t.Fatal("add human")
	}
	bot := room.ProvisionBot(2, 3, true, 40708, 3)
	if bot == nil {
		t.Fatal("add bot")
	}
	if !bot.IsBot || bot.LoginName != "AI HARD 1" || bot.RoomSlot != 1 || bot.RoomTeam != 2 || bot.RoomTeamSlot != 3 || bot.UDPPort != 40708 || bot.SessionID() != 0x08e0 {
		t.Fatalf("bot = %+v session=%#x", bot, bot.SessionID())
	}
	if room.Config.Mode != 0x040002 {
		t.Fatalf("AI mode = %#x", room.Config.Mode)
	}
	room.Round = 1
	if slots := room.PromoteBots(); len(slots) != 1 || slots[0] != 1 || bot.RoomStatus != 3 {
		t.Fatalf("promoted slots = %v status=%d", slots, bot.RoomStatus)
	}
	if slots := room.PromoteBots(); len(slots) != 0 {
		t.Fatalf("bots promoted twice: %v", slots)
	}
	_, _, empty := room.Remove(human)
	if !empty || room.Users != 0 {
		t.Fatalf("bot-only room was not torn down: empty=%v users=%d", empty, room.Users)
	}
}

func TestRoomAddsBotsToFirstSlotOnEitherTeam(t *testing.T) {
	redRoom := newRoom(7, RoomConfig{Capacity: 8})
	redHuman := &Session{RoomNumber: -1, RoomSlot: -1}
	if _, ok := redRoom.Add(redHuman); !ok {
		t.Fatal("add red-room human")
	}
	if _, _, _, ok := redRoom.MoveToOtherTeam(redHuman); !ok {
		t.Fatal("move human off red first row")
	}
	red := redRoom.ProvisionBot(1, 1, false, 40708, 0)
	if red == nil || red.RoomTeam != 1 || red.RoomTeamSlot != 0 {
		t.Fatalf("red first-row bot = %+v", red)
	}

	blueRoom := newRoom(8, RoomConfig{Capacity: 8})
	blueHuman := &Session{RoomNumber: -1, RoomSlot: -1}
	if _, ok := blueRoom.Add(blueHuman); !ok {
		t.Fatal("add blue-room human")
	}
	blue := blueRoom.ProvisionBot(2, 1, false, 40708, 0)
	if blue == nil || blue.RoomTeam != 2 || blue.RoomTeamSlot != 0 {
		t.Fatalf("blue first-row bot = %+v", blue)
	}
}

func TestChangeTeamRejectsFullAITeam(t *testing.T) {
	room := newRoom(9, RoomConfig{Capacity: 16, Mode: aiModeFlag | 2})
	human := &Session{RoomNumber: -1, RoomSlot: -1}
	if _, ok := room.Add(human); !ok {
		t.Fatal("add human")
	}
	for row := 0; row < teamSlotCount; row++ {
		if bot := room.ProvisionBot(blueTeam, 1, false, 40708, row); bot == nil {
			t.Fatalf("add blue bot at row %d", row)
		}
	}

	beforeSlot, beforeTeam, beforeTeamSlot := human.RoomSlot, human.RoomTeam, human.RoomTeamSlot
	if slot, team, teamSlot, ok := room.MoveToOtherTeam(human); ok {
		t.Fatalf("change to full blue team succeeded: slot=%d team=%d row=%d", slot, team, teamSlot)
	}
	if human.RoomSlot != beforeSlot || human.RoomTeam != beforeTeam || human.RoomTeamSlot != beforeTeamSlot {
		t.Fatalf("rejected team change mutated human: before=%d/%d/%d after=%d/%d/%d",
			beforeSlot, beforeTeam, beforeTeamSlot, human.RoomSlot, human.RoomTeam, human.RoomTeamSlot)
	}
	if got := room.teamCountLocked(blueTeam); got != teamSlotCount {
		t.Fatalf("blue team count = %d, want %d", got, teamSlotCount)
	}
}

func TestRoomEditsBotInPlace(t *testing.T) {
	room := newRoom(7, RoomConfig{Capacity: 8})
	bot := room.ProvisionBot(2, 1, false, 40708, 0)
	if bot == nil {
		t.Fatal("add bot")
	}
	edited := room.ProvisionBot(2, 3, true, 40708, 0)
	if edited != bot {
		t.Fatalf("edit created a different bot: %p != %p", edited, bot)
	}
	if room.Users != 1 || bot.BotDifficulty != 3 || !bot.BotWolf || bot.LoginName != "AI HARD 1" || bot.BotNumber != 1 {
		t.Fatalf("edited bot = %+v users=%d", bot, room.Users)
	}
}

func TestRoomRemovesOnlyBotsAndClearsAIMode(t *testing.T) {
	room := newRoom(7, RoomConfig{Capacity: 8})
	human := &Session{RoomNumber: -1, RoomSlot: -1}
	if _, ok := room.Add(human); !ok {
		t.Fatal("add human")
	}
	bot := room.ProvisionBot(2, 1, false, 40708, 0)
	if bot == nil {
		t.Fatal("add bot")
	}
	if room.RemoveBot(0) {
		t.Fatal("removed human as AI")
	}
	if !room.RemoveBot(byte(bot.RoomSlot)) {
		t.Fatal("remove bot")
	}
	if room.SessionAt(1) != nil || room.Snapshot().Users != 1 || room.Config.Mode&0x040000 != 0 {
		t.Fatalf("room after bot removal: users=%d mode=%#x slot=%v", room.Snapshot().Users, room.Config.Mode, room.SessionAt(1))
	}
	if bot.RoomNumber != -1 || bot.RoomSlot != -1 {
		t.Fatalf("removed bot still belongs to room: %+v", bot)
	}
}

func TestBotRosterUsesFactionDefaults(t *testing.T) {
	server := &Server{matchPort: 40707}
	bot := newBotSession("AI", 0, 40708)
	bot.RoomSlot, bot.RoomTeam, bot.RoomTeamSlot, bot.BotWolf = 1, 2, 0, true
	member := server.rosterMember(bot, rosterState{RedCharacter: 30006, BlueCharacter: 30005})
	if member.RedCharacter != 30006 || member.BlueCharacter != 30005 {
		t.Fatalf("wolf bot characters = %d/%d", member.RedCharacter, member.BlueCharacter)
	}
}
