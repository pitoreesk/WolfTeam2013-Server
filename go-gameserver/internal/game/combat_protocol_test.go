package game

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func TestCombatPayloadLayouts(t *testing.T) {
	dead := PlayerDeadPayload(3, 7, 12, 34, true)
	if !bytes.Equal(dead, []byte{0, 12, 0, 34, 0, 3, 7, 1}) {
		t.Fatalf("dead payload = %x", dead)
	}
	if PlayerDeadPayload(3, 7, 12, 34, false)[7] != 0 {
		t.Fatal("non-ending death set the end-round flag")
	}
	revival := RevivalPayload(2, 1, 3, 0xff)
	if !bytes.Equal(revival, []byte{0, 2, 1, 3, 0xff, 0, 0, 0, 0}) {
		t.Fatalf("revival payload = %x", revival)
	}
	flag, err := ParseChangeFlagRequest([]byte{4, 2, 9})
	if err != nil || flag != (ChangeFlagRequest{Flag: 4, Owner: 2, Actor: 9}) {
		t.Fatalf("change-flag request = %+v, err = %v", flag, err)
	}
	if _, err := ParseChangeFlagRequest([]byte{4, 2}); err == nil {
		t.Fatal("truncated change-flag request was accepted")
	}
	if reply := ChangeFlagPayload(); !bytes.Equal(reply, []byte{0}) {
		t.Fatalf("change-flag reply = %x", reply)
	}
	round := EndRoundPayload(5, 2, 12, 34)
	if len(round) != 13 || round[1] != 5 || round[2] != 2 || round[3] != 12 || round[4] != 34 ||
		binary.LittleEndian.Uint16(round[5:7]) != 12 || binary.LittleEndian.Uint16(round[7:9]) != 34 ||
		binary.LittleEndian.Uint16(round[9:11]) != 12 || binary.LittleEndian.Uint16(round[11:13]) != 34 {
		t.Fatalf("end-round payload = %x", round)
	}
	plant := PlantBombPayload(40)
	if !bytes.Equal(plant, []byte{0, 40, 0}) {
		t.Fatalf("plant-bomb payload = %x", plant)
	}
	players := []RoundPlayerState{{Slot: 0, Team: 1}, {Slot: 3, Team: 2}}
	start := StartRoundPayload(3, 90, 2, 1, players)
	if len(start) != 11 || start[1] != 3 || binary.LittleEndian.Uint16(start[2:4]) != 95 || start[4] != 2 || start[5] != 1 || start[6] != 2 || start[7] != 0 || start[8] != 1 || start[9] != 3 || start[10] != 2 {
		t.Fatalf("start-round payload = %x", start)
	}
	restart := RestartRoundPayload(4, 75, players)
	if len(restart) != 9 || restart[1] != 4 || binary.LittleEndian.Uint16(restart[2:4]) != 80 || restart[4] != 2 || restart[5] != 0 || restart[6] != 1 || restart[7] != 3 || restart[8] != 2 {
		t.Fatalf("restart-round payload = %x", restart)
	}
	if remove := RemoveBombPayload(); !bytes.Equal(remove, []byte{0}) {
		t.Fatalf("remove-bomb payload = %x", remove)
	}
	// ENDGAME and GAMEPOINT layouts: see game_ops_2012_test.go.
	gold, gp := MatchAward(MatchPlayerStats{Kills: 3, Plants: 1}, true)
	if gold != 155 || gp != 310 {
		t.Fatalf("match award = %d/%d", gold, gp)
	}
}

func allZero(values []byte) bool {
	for _, value := range values {
		if value != 0 {
			return false
		}
	}
	return true
}

func TestDeathMatchScoreAndRoundEnd(t *testing.T) {
	room := newRoom(1, RoomConfig{Capacity: 8, Mode: 2, Points: 1, Time: 60})
	dead := &Session{RoomNumber: -1, RoomSlot: -1}
	if _, ok := room.Add(dead); !ok {
		t.Fatal("add dead player")
	}
	dead.mu.Lock()
	dead.RoomTeam = 2
	dead.mu.Unlock()
	room.GameStarted, room.RoundStarted, room.Round = true, true, 1
	result := room.RecordDeath(dead)
	if !result.FinishRound || !result.EndGame || result.WinTeam != 1 || result.RedScore != 1 || result.BlueScore != 0 {
		t.Fatalf("death result = %+v", result)
	}
	round, red, blue, ok := room.FinishRound(result.EndGame)
	if !ok || round != 2 || red != 1 || blue != 0 || room.RoundStarted || room.GameStarted {
		t.Fatalf("end round = %d %d %d %v", round, red, blue, ok)
	}
	restartedAt := time.Unix(100, 0)
	room.CompleteRoundDelay(false, restartedAt)
	if !room.RoundStarted || room.RoundStartedAt != restartedAt {
		t.Fatal("round was not restarted")
	}
}

func TestBotKillCreditsBotTeam(t *testing.T) {
	room := newRoom(1, RoomConfig{Capacity: 8, Mode: 2, Points: 10})
	player := &Session{RoomNumber: -1, RoomSlot: -1}
	if _, ok := room.Add(player); !ok {
		t.Fatal("add player")
	}
	bot := room.ProvisionBot(2, 1, false, 40708, 0)
	if bot == nil {
		t.Fatal("add bot")
	}
	result := room.RecordKill(bot, player)
	if result.RedScore != 0 || result.BlueScore != 1 {
		t.Fatalf("bot kill credited wrong team: %+v", result)
	}
}

func TestRepeatedAIKillsEachIncreaseDeathMatchScore(t *testing.T) {
	room := newRoom(1, RoomConfig{Capacity: 8, Mode: 2, Points: 10})
	player := &Session{RoomNumber: -1, RoomSlot: -1}
	if _, ok := room.Add(player); !ok {
		t.Fatal("add player")
	}
	bot := room.ProvisionBot(2, 1, false, 40708, 0)
	if bot == nil {
		t.Fatal("add bot")
	}
	for kill := uint16(1); kill <= 2; kill++ {
		result := room.RecordKill(player, bot)
		if !result.Accepted || result.RedScore != kill || result.BlueScore != 0 {
			t.Fatalf("AI kill %d result = %+v", kill, result)
		}
	}
	players := room.MatchPlayers()
	if players[0].Stats.Kills != 2 || players[1].Stats.Deaths != 2 {
		t.Fatalf("repeated AI kill stats = %+v", players)
	}
}

func TestDeathMatchSelfKillCreditsOpposingTeam(t *testing.T) {
	tests := []struct {
		name          string
		team          byte
		wantRedScore  uint16
		wantBlueScore uint16
		wantWinTeam   byte
	}{
		{name: "red player", team: 1, wantBlueScore: 1, wantWinTeam: 2},
		{name: "blue player", team: 2, wantRedScore: 1, wantWinTeam: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			room := newRoom(1, RoomConfig{Capacity: 8, Mode: 2, Points: 1})
			player := &Session{RoomNumber: -1, RoomSlot: -1}
			slot, ok := room.Add(player)
			if !ok {
				t.Fatal("add player")
			}
			player.mu.Lock()
			player.RoomTeam = test.team
			player.mu.Unlock()

			result := room.RecordKill(player, player)
			if result.RedScore != test.wantRedScore || result.BlueScore != test.wantBlueScore ||
				!result.FinishRound || !result.EndGame || result.WinTeam != test.wantWinTeam {
				t.Fatalf("self-kill result = %+v", result)
			}
			stats := room.MatchPlayers()[0].Stats
			if slot != 0 || stats.Kills != 0 || stats.Deaths != 1 {
				t.Fatalf("self-kill stats = slot %d %+v", slot, stats)
			}
		})
	}
}

func TestRoomTimeoutAndForfeitState(t *testing.T) {
	now := time.Unix(1_000, 0)
	room := newRoom(1, RoomConfig{Capacity: 8, Mode: 2, Wins: 3, Points: 10, Time: 60})
	teamOne := &Session{RoomNumber: -1, RoomSlot: -1}
	teamTwo := &Session{RoomNumber: -1, RoomSlot: -1}
	if _, ok := room.Add(teamOne); !ok {
		t.Fatal("add team one")
	}
	if _, ok := room.Add(teamTwo); !ok {
		t.Fatal("add team two")
	}
	room.GameStarted = true
	room.RoundStarted = true
	room.RoundStartedAt = now.Add(-61 * time.Second)
	room.RedScore, room.BlueScore = 4, 3
	winTeam, endGame, triggered := room.RoundTimeout(now)
	if !triggered || !endGame || winTeam != 1 {
		t.Fatalf("round timeout = team %d end=%v triggered=%v", winTeam, endGame, triggered)
	}

	room.RoundStartedAt = now
	room.PlantTime = now.Add(-41 * time.Second)
	winTeam, endGame, triggered = room.BombTimeout(now)
	if !triggered || !endGame || winTeam != 1 || room.RedScore != 5 {
		t.Fatalf("bomb timeout = team %d end=%v triggered=%v red=%d", winTeam, endGame, triggered, room.RedScore)
	}

	teamTwo.mu.RLock()
	departingTeam := teamTwo.RoomTeam
	teamTwo.mu.RUnlock()
	_, _, empty := room.Remove(teamTwo)
	if empty {
		t.Fatal("room unexpectedly empty")
	}
	winTeam, endGame, forfeited := room.ForfeitEmptyTeam(departingTeam)
	if !forfeited || !endGame || winTeam != 1 || room.RedScore != 6 || room.BlueScore != 3 {
		t.Fatalf("forfeit = team %d end=%v triggered=%v scores=%d/%d", winTeam, endGame, forfeited, room.RedScore, room.BlueScore)
	}
}

func TestDeathMatchTimeoutCanEndInDraw(t *testing.T) {
	now := time.Unix(1_000, 0)
	room := newRoom(9, RoomConfig{Capacity: 8, Mode: 2, Points: 10, Time: 60})
	room.GameStarted = true
	room.RoundStarted = true
	room.RoundStartedAt = now.Add(-61 * time.Second)
	room.RedScore, room.BlueScore = 4, 4

	winTeam, endGame, triggered := room.RoundTimeout(now)
	if !triggered || !endGame || winTeam != 0 {
		t.Fatalf("draw timeout = team %d end=%v triggered=%v", winTeam, endGame, triggered)
	}
}

func TestObjectiveScoringUsesProtocolTeamMapping(t *testing.T) {
	room := newRoom(1, RoomConfig{Capacity: 8, Wins: 1})
	for flag := byte(0); flag < 5; flag++ {
		endRound, endGame := room.UpdateFlagControl(flag, 1)
		if flag < 4 && (endRound || endGame) {
			t.Fatalf("flag %d ended the round early", flag)
		}
		if flag == 4 && (!endRound || !endGame) {
			t.Fatal("blue conquest did not end the match")
		}
	}
	if room.RedScore != 1 || room.BlueScore != 0 {
		t.Fatalf("conquest scores = %d/%d", room.RedScore, room.BlueScore)
	}

	room = newRoom(2, RoomConfig{Capacity: 8, Mode: 4, Wins: 1, Time: 120})
	attacker := &Session{RoomNumber: -1, RoomSlot: -1}
	defender := &Session{RoomNumber: -1, RoomSlot: -1}
	attackerSlot, ok := room.Add(attacker)
	if !ok {
		t.Fatal("add attacker")
	}
	defenderSlot, ok := room.Add(defender)
	if !ok {
		t.Fatal("add defender")
	}
	room.GameStarted, room.RoundStarted = true, true
	room.RoundStartedAt = time.Unix(10, 0)
	attacker.RoomStatus, defender.RoomStatus = activeRoomStatus, activeRoomStatus
	if room.RecordBombPlant(defender, defenderSlot, time.Unix(20, 0)) {
		t.Fatal("defender planted the bomb")
	}
	if !room.RecordBombPlant(attacker, attackerSlot, time.Unix(20, 0)) {
		t.Fatal("attacker plant was rejected")
	}
	if _, _, ok := room.ResolveBombRemoval(attacker, 2, attackerSlot, time.Unix(30, 0)); ok {
		t.Fatal("attacker defused the bomb")
	}
	winTeam, endGame, ok := room.ResolveBombRemoval(defender, 2, defenderSlot, time.Unix(30, 0))
	if !ok || winTeam != 2 || !endGame || room.RedScore != 0 || room.BlueScore != 1 {
		t.Fatalf("defuse scores = %d/%d team=%d end=%v ok=%v", room.RedScore, room.BlueScore, winTeam, endGame, ok)
	}
	matchPlayers := room.MatchPlayers()
	if len(matchPlayers) != 2 || matchPlayers[0].Stats.Plants != 1 || matchPlayers[1].Stats.Defuses != 1 {
		t.Fatalf("objective stats = %+v", matchPlayers)
	}
}

func TestExplosiveRoundsAdvanceAndResetTimer(t *testing.T) {
	room := newRoom(3, RoomConfig{Capacity: 8, Mode: 4, Wins: 3, Time: 90})
	red := &Session{RoomNumber: -1, RoomSlot: -1}
	blue := &Session{RoomNumber: -1, RoomSlot: -1}
	redSlot, ok := room.Add(red)
	if !ok {
		t.Fatal("add red player")
	}
	blueSlot, ok := room.Add(blue)
	if !ok {
		t.Fatal("add blue player")
	}
	beginTestMatch(room)
	firstStart := time.Unix(1_000, 0)
	round, seconds, _ := room.BeginRound(firstStart)
	if round != 1 || seconds != 90 {
		t.Fatalf("first round = %d/%d", round, seconds)
	}
	if !room.RecordBombPlant(red, redSlot, firstStart.Add(5*time.Second)) {
		t.Fatal("red plant rejected")
	}
	winTeam, endGame, triggered := room.BombTimeout(firstStart.Add(46 * time.Second))
	if !triggered || endGame || winTeam != 1 || room.RedScore != 1 || room.BlueScore != 0 {
		t.Fatalf("explosion result = team %d end=%v triggered=%v score=%d/%d", winTeam, endGame, triggered, room.RedScore, room.BlueScore)
	}
	round, _, _, ok = room.FinishRound(false)
	if !ok || round != 2 {
		t.Fatalf("second round = %d ok=%v", round, ok)
	}
	secondStart := time.Unix(2_000, 0)
	room.CompleteRoundDelay(false, secondStart)
	if room.RoundStartedAt != secondStart {
		t.Fatalf("second-round timer started at %v", room.RoundStartedAt)
	}
	if !room.RecordBombPlant(red, redSlot, secondStart.Add(5*time.Second)) {
		t.Fatal("second red plant rejected")
	}
	winTeam, endGame, ok = room.ResolveBombRemoval(blue, 2, blueSlot, secondStart.Add(15*time.Second))
	if !ok || winTeam != 2 || endGame || room.RedScore != 1 || room.BlueScore != 1 {
		t.Fatalf("defuse result = ok=%v team=%d end=%v score=%d/%d", ok, winTeam, endGame, room.RedScore, room.BlueScore)
	}
	round, _, _, ok = room.FinishRound(false)
	if !ok || round != 3 {
		t.Fatalf("third round = %d ok=%v", round, ok)
	}
	thirdStart := time.Unix(3_000, 0)
	room.CompleteRoundDelay(false, thirdStart)
	winTeam, endGame, triggered = room.RoundTimeout(thirdStart.Add(91 * time.Second))
	if !triggered || endGame || winTeam != 2 || room.RedScore != 1 || room.BlueScore != 2 {
		t.Fatalf("round timeout = team %d end=%v triggered=%v score=%d/%d", winTeam, endGame, triggered, room.RedScore, room.BlueScore)
	}
}

func TestPlantedBombSuspendsRoundClockAndAcceptsTimedActorlessResult(t *testing.T) {
	room := newRoom(4, RoomConfig{Capacity: 8, Mode: 4, Wins: 3, Time: 60})
	red := &Session{RoomNumber: -1, RoomSlot: -1}
	blue := &Session{RoomNumber: -1, RoomSlot: -1}
	redSlot, ok := room.Add(red)
	if !ok {
		t.Fatal("add red player")
	}
	if _, ok := room.Add(blue); !ok {
		t.Fatal("add blue player")
	}
	room.GameStarted, room.RoundStarted = true, true
	start := time.Unix(5_000, 0)
	plant := start.Add(59 * time.Second)
	room.RoundStartedAt = start
	if !room.RecordBombPlant(red, redSlot, plant) {
		t.Fatal("red plant rejected")
	}
	if room.RoundStartedAt != start {
		t.Fatalf("plant changed round start to %v", room.RoundStartedAt)
	}
	if team, endGame, triggered := room.RoundTimeout(start.Add(120 * time.Second)); triggered {
		t.Fatalf("round clock resolved planted bomb: team=%d end=%v", team, endGame)
	}
	if _, _, ok := room.ResolveBombRemoval(blue, 1, 0xff, plant.Add(39*time.Second)); ok {
		t.Fatal("actorless explosion was accepted before the fuse elapsed")
	}
	winTeam, endGame, ok := room.ResolveBombRemoval(blue, 1, 0xff, plant.Add(bombFuseSeconds*time.Second))
	if !ok || winTeam != 1 || endGame || room.RedScore != 1 || room.BlueScore != 0 || !room.PlantTime.IsZero() {
		t.Fatalf("actorless explosion = ok=%v team=%d end=%v score=%d/%d plant=%v", ok, winTeam, endGame, room.RedScore, room.BlueScore, room.PlantTime)
	}
}

func TestTimedActorlessBlueBombResultHasNoDefuseCredit(t *testing.T) {
	room := newRoom(5, RoomConfig{Capacity: 8, Mode: 4, Wins: 1, Time: 60})
	red := &Session{RoomNumber: -1, RoomSlot: -1}
	blue := &Session{RoomNumber: -1, RoomSlot: -1}
	redSlot, ok := room.Add(red)
	if !ok {
		t.Fatal("add red player")
	}
	if _, ok := room.Add(blue); !ok {
		t.Fatal("add blue player")
	}
	room.GameStarted, room.RoundStarted = true, true
	plant := time.Unix(6_000, 0)
	if !room.RecordBombPlant(red, redSlot, plant) {
		t.Fatal("red plant rejected")
	}
	winTeam, endGame, ok := room.ResolveBombRemoval(blue, 2, 0xff, plant.Add(bombFuseSeconds*time.Second))
	if !ok || winTeam != 2 || !endGame || room.RedScore != 0 || room.BlueScore != 1 {
		t.Fatalf("actorless blue result = ok=%v team=%d end=%v score=%d/%d", ok, winTeam, endGame, room.RedScore, room.BlueScore)
	}
	for _, player := range room.MatchPlayers() {
		if player.Stats.Defuses != 0 {
			t.Fatalf("actorless result credited defuse: %+v", player)
		}
	}
}

func TestGameplayAuthorityCanReportBombActors(t *testing.T) {
	room := newRoom(6, RoomConfig{Capacity: 8, Mode: 4, Wins: 1, Time: 60})
	red := &Session{RoomNumber: -1, RoomSlot: -1}
	blue := &Session{RoomNumber: -1, RoomSlot: -1}
	redSlot, ok := room.Add(red)
	if !ok {
		t.Fatal("add red player")
	}
	blueSlot, ok := room.Add(blue)
	if !ok {
		t.Fatal("add blue player")
	}
	room.GameStarted, room.RoundStarted = true, true
	red.RoomStatus, blue.RoomStatus = activeRoomStatus, activeRoomStatus
	now := time.Unix(7_000, 0)

	// Gameplay events come from the authoritative peer, which is not
	// necessarily the player named by the actor byte.
	if !room.RecordBombPlant(blue, redSlot, now) {
		t.Fatal("authority-reported red plant was rejected")
	}
	winTeam, endGame, ok := room.ResolveBombRemoval(red, 2, blueSlot, now.Add(10*time.Second))
	if !ok || winTeam != 2 || !endGame || room.RedScore != 0 || room.BlueScore != 1 {
		t.Fatalf("authority-reported defuse = ok=%v team=%d end=%v score=%d/%d", ok, winTeam, endGame, room.RedScore, room.BlueScore)
	}
	players := room.MatchPlayers()
	if len(players) != 2 || players[0].Stats.Plants != 1 || players[1].Stats.Defuses != 1 {
		t.Fatalf("authority-reported objective stats = %+v", players)
	}
}
