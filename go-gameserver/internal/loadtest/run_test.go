package loadtest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGroupIndexesAvoidsSingletonRooms(t *testing.T) {
	tests := []struct {
		players  int
		roomSize int
		want     [][2]int
	}{
		{players: 2, roomSize: 8, want: [][2]int{{0, 2}}},
		{players: 9, roomSize: 8, want: [][2]int{{0, 7}, {7, 9}}},
		{players: 17, roomSize: 8, want: [][2]int{{0, 8}, {8, 15}, {15, 17}}},
		{players: 250, roomSize: 8, want: append(makeFullGroups(31, 8), [2]int{248, 250})},
		{players: 10_000, roomSize: 8, want: makeFullGroups(1_250, 8)},
	}
	for _, test := range tests {
		t.Run(testName(test.players, test.roomSize), func(t *testing.T) {
			got := groupIndexes(test.players, test.roomSize)
			if len(got) != len(test.want) {
				t.Fatalf("groups = %v, want %v", got, test.want)
			}
			for index := range got {
				if got[index] != test.want[index] {
					t.Fatalf("groups = %v, want %v", got, test.want)
				}
				if size := got[index][1] - got[index][0]; size < 2 || size > test.roomSize {
					t.Fatalf("group %d has invalid size %d", index, size)
				}
			}
		})
	}
}

func TestTenThousandPlayersUseFiveProtocolSafeRoomWaves(t *testing.T) {
	config := Config{Players: 10_000, Cycles: 1, RoomSize: 8}
	if err := validateConfig(config); err != nil {
		t.Fatal(err)
	}
	groups := make([]*clientGroup, len(groupIndexes(config.Players, config.RoomSize)))
	waves := groupWaves(groups, maxRoomsPerWave)
	if len(groups) != 1_250 || len(waves) != 5 {
		t.Fatalf("groups/waves = %d/%d, want 1250/5", len(groups), len(waves))
	}
	for index, wave := range waves {
		if len(wave) > maxRoomsPerWave {
			t.Fatalf("wave %d has %d rooms, protocol maximum is %d", index, len(wave), maxRoomsPerWave)
		}
	}
	if len(waves[len(waves)-1]) != 230 {
		t.Fatalf("last wave has %d rooms, want 230", len(waves[len(waves)-1]))
	}
}

func TestRunCompletesEncryptedPlayerLifecycleWithoutDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	report, err := Run(ctx, Config{
		Players: 4, Cycles: 1, RoomSize: 4, Drain: 100 * time.Millisecond,
		Gameplay: GameplayConfig{
			Duration:                 250 * time.Millisecond,
			MovementPacketsPerSecond: 20,
			MovementPayloadBytes:     32,
			ShootingPacketsPerSecond: 10,
			ShootingPayloadBytes:     48,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Persistence != "in-memory only (zero database connections)" || report.Errors != 0 {
		t.Fatalf("isolation/errors = %q/%d", report.Persistence, report.Errors)
	}
	if report.RoomWavesPerCycle != 1 || report.MaxRoomsPerWave != maxRoomsPerWave {
		t.Fatalf("room wave report = %+v", report)
	}
	if report.Backend.Purchases != 4 || report.Backend.EquipmentWrites != 64 || report.Backend.MatchResults != 4 {
		t.Fatalf("mutations = %+v", report.Backend)
	}
	for _, action := range []string{
		"login", "buy_item", "save_equipment", "create_room", "enter_room",
		"ready", "start_game", "enter_game", "start_round", "player_dead", "exit_room",
	} {
		if report.Latency[action].Count == 0 {
			t.Fatalf("action %q did not complete: %+v", action, report.Latency)
		}
	}
	if report.Requests == 0 || report.Responses < report.Requests || report.BytesSent == 0 || report.BytesReceived == 0 {
		t.Fatalf("traffic report = %+v", report)
	}
	if report.ConnectionFailures != 0 || report.ProtocolFailures != 0 || report.UnexpectedDisconnects != 0 || report.PendingWaiters != 0 {
		t.Fatalf("harness integrity = connection %d protocol %d disconnects %d waiters %d",
			report.ConnectionFailures, report.ProtocolFailures, report.UnexpectedDisconnects, report.PendingWaiters)
	}
	if report.Network.ServerAcceptedConnections != report.Network.ServerClosedConnections ||
		report.Network.ServerWriteCalls == 0 || report.Network.ResponsesPerServerWrite < 1 {
		t.Fatalf("network report = %+v", report.Network)
	}
	if report.Lobby.Flushes == 0 || report.Lobby.DeliveredFrames == 0 || report.Lobby.FramesPerBatch < 1 {
		t.Fatalf("lobby report = %+v", report.Lobby)
	}
	if !report.Gameplay.Enabled || report.Gameplay.MovementPacketsSent == 0 || report.Gameplay.ShootingPacketsSent == 0 ||
		report.Gameplay.ExpectedRelayPackets == 0 || report.Gameplay.ReceivedRelayPackets == 0 ||
		report.Gameplay.ExpectedRelayIPv4Bytes <= report.Gameplay.ExpectedRelayPayloadBytes ||
		report.Gameplay.ExpectedPerPlayerKilobits <= 0 || report.Gameplay.ProjectedMonthlyEgressTB <= 0 {
		t.Fatalf("gameplay UDP report = %+v", report.Gameplay)
	}
	if report.Gameplay.DeliveryRatio < 0.95 || report.Gameplay.DeliveryRatio > 1 {
		t.Fatalf("gameplay UDP delivery ratio = %.6f, want 0.95..1", report.Gameplay.DeliveryRatio)
	}
}

func TestJoinErrorsBoundsDetails(t *testing.T) {
	values := make([]error, 12)
	for index := range values {
		values[index] = errors.New("failure " + testName(index, 1))
	}
	message := joinErrors(values).Error()
	if !strings.Contains(message, "... and 2 more errors") || strings.Contains(message, "failure 11") {
		t.Fatalf("bounded error summary = %q", message)
	}
}

func TestValidateConfigRejectsNegativeDrain(t *testing.T) {
	if err := validateConfig(Config{Players: 2, Cycles: 1, RoomSize: 2, Drain: -time.Millisecond}); err == nil {
		t.Fatal("negative drain was accepted")
	}
}

func TestValidateConfigRejectsInvalidGameplayTraffic(t *testing.T) {
	base := Config{Players: 2, Cycles: 1, RoomSize: 2}
	tests := []GameplayConfig{
		{Duration: -time.Millisecond},
		{Duration: time.Second},
		{Duration: time.Second, MovementPacketsPerSecond: 10, MovementPayloadBytes: 9},
		{Duration: time.Second, ShootingPacketsPerSecond: 1001, ShootingPayloadBytes: 48},
	}
	for _, gameplay := range tests {
		config := base
		config.Gameplay = gameplay
		if err := validateConfig(config); err == nil {
			t.Fatalf("invalid gameplay config was accepted: %+v", gameplay)
		}
	}
}

func TestGameplayPacketCarriesKindSenderAndSequence(t *testing.T) {
	packet := gameplayPacket(shootingPacketMarker, 48, 23, 99)
	if len(packet) != 48 || packet[0] != shootingPacketMarker || packet[1] != 1 {
		t.Fatalf("gameplay packet header = %x", packet[:10])
	}
	if got := uint32(packet[2]) | uint32(packet[3])<<8 | uint32(packet[4])<<16 | uint32(packet[5])<<24; got != 23 {
		t.Fatalf("gameplay packet sender = %d, want 23", got)
	}
	if got := uint32(packet[6]) | uint32(packet[7])<<8 | uint32(packet[8])<<16 | uint32(packet[9])<<24; got != 99 {
		t.Fatalf("gameplay packet sequence = %d, want 99", got)
	}
}

func TestValidateCompletedReportChecksExactActionCounts(t *testing.T) {
	report := Report{
		Players:       4,
		Cycles:        1,
		RoomsPerCycle: 1,
		Requests:      54,
		Latency:       make(map[string]LatencyReport),
		Network:       NetworkReport{ServerAcceptedConnections: 5, ServerClosedConnections: 5},
	}
	for name, count := range map[string]int{
		"login": 4, "user_info": 4, "currency": 4, "item_list": 4, "equipment_list": 4,
		"buy_item": 4, "save_equipment": 4, "create_room": 1, "enter_room": 3,
		"ready": 4, "start_game": 1, "enter_game": 4, "start_round": 4,
		"player_dead": 1, "game_points": 4, "exit_room": 4,
	} {
		report.Latency[name] = LatencyReport{Count: count}
	}
	if err := validateCompletedReport(report); err != nil {
		t.Fatal(err)
	}
	report.Latency["enter_game"] = LatencyReport{Count: 3}
	if err := validateCompletedReport(report); err == nil {
		t.Fatal("missing completed action was accepted")
	}
	report.Latency["enter_game"] = LatencyReport{Count: 4}
	report.Lobby.FailedBatches = 2
	report.Lobby.FailedFrames = 17
	if err := validateCompletedReport(report); err == nil || !strings.Contains(err.Error(), "failed lobby delivery batches = 2 (17 frames)") {
		t.Fatalf("failed lobby delivery was accepted: %v", err)
	}
}

func TestMemoryEquipmentBatchValidatesBeforeMutation(t *testing.T) {
	users := newMemoryUsers()
	userID, err := users.Login(context.Background(), "batch-user", loadPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := users.SetEquipmentBatch(context.Background(), userID, map[int]int{0: 101, 24: 202}); err != nil {
		t.Fatal(err)
	}
	before, err := users.Equipment(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	operationsBefore := users.operations.Load()
	if err := users.SetEquipmentBatch(context.Background(), userID, map[int]int{0: 303, 34: 404}); err == nil {
		t.Fatal("invalid batch was accepted")
	}
	after, err := users.Equipment(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if after != before || after[0] != 101 || after[24] != 202 {
		t.Fatalf("invalid batch partially mutated equipment: before=%v after=%v", before, after)
	}
	// The rejected batch is validated before it is counted as a backend call.
	if got := users.operations.Load(); got != operationsBefore+1 { // Equipment above is one successful operation.
		t.Fatalf("backend operations after rejected batch = %d, want %d", got, operationsBefore+1)
	}
}

func makeFullGroups(count, roomSize int) [][2]int {
	groups := make([][2]int, 0, count)
	for index := 0; index < count; index++ {
		groups = append(groups, [2]int{index * roomSize, (index + 1) * roomSize})
	}
	return groups
}

func testName(players, roomSize int) string {
	return time.Duration(players).String() + "_players_" + time.Duration(roomSize).String() + "_room"
}
