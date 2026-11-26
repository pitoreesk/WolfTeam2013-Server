package loadtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"wtserver-go/internal/game"
)

const (
	loadPassword      = "load-only"
	maxRoomsPerWave   = 255
	loadRoundEndDelay = 10 * time.Millisecond
)

type Config struct {
	Players   int
	Cycles    int
	RoomSize  int
	Ramp      time.Duration
	Hold      time.Duration
	Drain     time.Duration
	Gameplay  GameplayConfig
	Verbose   bool
	LogWriter io.Writer
}

type clientGroup struct {
	clients    []*loadClient
	roomNumber int16
}

func Run(ctx context.Context, config Config) (Report, error) {
	if err := validateConfig(config); err != nil {
		return Report{}, err
	}
	groupsCount := len(groupIndexes(config.Players, config.RoomSize))
	wavesCount := (groupsCount + maxRoomsPerWave - 1) / maxRoomsPerWave
	report := Report{
		Players:           config.Players,
		Cycles:            config.Cycles,
		RoomSize:          config.RoomSize,
		RoomsPerCycle:     groupsCount,
		RoomWavesPerCycle: wavesCount,
		MaxRoomsPerWave:   maxRoomsPerWave,
		Persistence:       "in-memory only (zero database connections)",
	}
	started := time.Now()
	metrics := newMeasurements()
	backend := newMemoryUsers()
	network := &serverNetworkMetrics{}
	gameplayPhases := &gameplayPhaseMetrics{}
	resources := startResourceSampler()

	address, err := availableAddress()
	if err != nil {
		report.Resources = resources.stop(time.Since(started))
		return report, err
	}
	logger := loadLogger(config)
	loopback := netip.MustParseAddr("127.0.0.1")
	_, portText, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portText)
	server := game.NewServer(address, uint16(port), uint16(port+1), loopback, loopback, backend, logger)
	server.ConfigureConnectionWrapper(network.wrap)
	server.ConfigureRoundEndDelay(loadRoundEndDelay)
	server.EnableLobbyUpdateStats()
	serverContext, stopServer := context.WithCancel(ctx)
	type serviceResult struct {
		name string
		err  error
	}
	serviceCount := 1
	serverDone := make(chan serviceResult, 2)
	go func() { serverDone <- serviceResult{name: "TCP", err: server.Run(serverContext)} }()
	if config.Gameplay.enabled() {
		serviceCount++
		go func() { serverDone <- serviceResult{name: "UDP", err: server.RunUDP(serverContext)} }()
	}

	clients := make([]*loadClient, config.Players)
	runErr := waitForServer(ctx, address)
	if runErr == nil {
		runErr = connectClients(ctx, address, clients, config.Ramp, metrics)
	}
	if runErr == nil && config.Gameplay.enabled() {
		runErr = connectUDPClients(ctx, address, clients)
	}
	for cycle := 0; cycle < config.Cycles && runErr == nil; cycle++ {
		if err := runCycle(ctx, clients, config.RoomSize, uint64(cycle+1), config.Gameplay, gameplayPhases); err != nil {
			runErr = fmt.Errorf("cycle %d: %w", cycle+1, err)
		}
	}
	if runErr == nil && config.Hold > 0 {
		select {
		case <-time.After(config.Hold):
		case <-ctx.Done():
			runErr = ctx.Err()
		}
	}
	if runErr == nil && config.Drain > 0 {
		select {
		case <-time.After(config.Drain):
		case <-ctx.Done():
			runErr = ctx.Err()
		}
	}

	workloadResponses := metrics.responses.Load()
	workloadNetwork := network.snapshot()
	workloadLobby := server.LobbyUpdateStats()
	report.Gameplay = buildGameplayReport(config.Gameplay, clients, gameplayPhases, config.Cycles)
	report.Errors += report.Gameplay.SendErrors + report.Gameplay.ReadErrors
	for _, client := range clients {
		if client != nil {
			report.PendingWaiters += client.pendingWaiters()
			report.BytesSent += client.connection.writtenBytes.Load()
			report.BytesReceived += client.connection.readBytes.Load()
			report.Network.ClientReadCalls += client.connection.readCalls.Load()
			report.Network.ClientWriteCalls += client.connection.writeCalls.Load()
			client.close()
		}
	}
	stopServer()
	for range serviceCount {
		select {
		case result := <-serverDone:
			if result.err != nil && runErr == nil {
				runErr = fmt.Errorf("game %s server: %w", result.name, result.err)
			}
		case <-time.After(5 * time.Second):
			if runErr == nil {
				runErr = errors.New("game server did not stop within 5s")
			}
		}
	}

	report.Duration = time.Since(started)
	report.Requests = metrics.requests.Load()
	report.Responses = workloadResponses
	report.Errors += metrics.errors.Load()
	report.UnexpectedDisconnects = metrics.unexpectedDisconnects.Load()
	report.ConnectionFailures = metrics.connectionFailures.Load()
	report.ProtocolFailures = metrics.protocolFailures.Load()
	if report.Duration > 0 {
		report.RequestsPerSecond = float64(report.Requests) / report.Duration.Seconds()
	}
	report.Latency = metrics.reports()
	report.Backend = backend.stats()
	report.Resources = resources.stop(report.Duration)
	finalNetwork := network.snapshot()
	workloadNetwork.ServerAcceptedConnections = finalNetwork.ServerAcceptedConnections
	workloadNetwork.ServerClosedConnections = finalNetwork.ServerClosedConnections
	workloadNetwork.ServerPeakConnections = finalNetwork.ServerPeakConnections
	workloadNetwork.ClientReadCalls = report.Network.ClientReadCalls
	workloadNetwork.ClientWriteCalls = report.Network.ClientWriteCalls
	if workloadNetwork.ServerWriteCalls > 0 {
		workloadNetwork.ResponsesPerServerWrite = float64(report.Responses) / float64(workloadNetwork.ServerWriteCalls)
	}
	report.Network = workloadNetwork
	lobby := workloadLobby
	report.Lobby = LobbyReport{
		Flushes:                lobby.Flushes,
		Events:                 lobby.Events,
		RecipientBatches:       lobby.RecipientBatches,
		DeliveredFrames:        lobby.DeliveredFrames,
		AudienceFilteredFrames: lobby.AudienceFilteredFrames,
		FailedBatches:          lobby.FailedBatches,
		FailedFrames:           lobby.FailedFrames,
		MaxEventsPerFlush:      lobby.MaxEventsPerFlush,
		MaxFramesPerBatch:      lobby.MaxFramesPerBatch,
	}
	// A failed unsolicited lobby batch is still a load-test error even when all
	// request/reply actions completed. Without this, oversized or broken server
	// pushes can silently disappear while the harness reports zero errors.
	report.Errors += report.Lobby.FailedBatches
	if report.Lobby.RecipientBatches > 0 {
		report.Lobby.FramesPerBatch = float64(report.Lobby.DeliveredFrames) / float64(report.Lobby.RecipientBatches)
	}

	if runErr == nil {
		expectedPlayers := uint64(config.Players * config.Cycles)
		expectedPurchases := expectedPlayers
		expectedEquipmentWrites := expectedPurchases * 16
		if report.Backend.Purchases != expectedPurchases {
			runErr = fmt.Errorf("in-memory purchase mutations = %d, want %d", report.Backend.Purchases, expectedPurchases)
		}
		if report.Backend.EquipmentWrites != expectedEquipmentWrites {
			runErr = errors.Join(runErr, fmt.Errorf("in-memory equipment mutations = %d, want %d", report.Backend.EquipmentWrites, expectedEquipmentWrites))
		}
		if report.Backend.MatchResults != expectedPlayers {
			runErr = errors.Join(runErr, fmt.Errorf("in-memory match results = %d, want %d", report.Backend.MatchResults, expectedPlayers))
		}
		runErr = errors.Join(runErr, validateCompletedReport(report))
	}
	return report, runErr
}

func validateConfig(config Config) error {
	switch {
	case config.Players < 2:
		return errors.New("players must be at least 2 so every room can play")
	case config.Players > 10_000:
		return errors.New("players must not exceed 10000")
	case config.Cycles < 1:
		return errors.New("cycles must be at least 1")
	case config.RoomSize < 2 || config.RoomSize > 16:
		return errors.New("room-size must be between 2 and 16")
	case config.Ramp < 0:
		return errors.New("ramp must not be negative")
	case config.Hold < 0:
		return errors.New("hold must not be negative")
	case config.Drain < 0:
		return errors.New("drain must not be negative")
	default:
		return validateGameplayConfig(config.Gameplay)
	}
}

func loadLogger(config Config) *slog.Logger {
	writer := config.LogWriter
	if writer == nil || !config.Verbose {
		writer = io.Discard
	}
	level := slog.LevelInfo
	if config.Verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{Level: level}))
}

func availableAddress() (string, error) {
	for attempt := 0; attempt < 100; attempt++ {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return "", fmt.Errorf("reserve load-test TCP address: %w", err)
		}
		address := listener.Addr().String()
		_, portText, _ := net.SplitHostPort(address)
		port, _ := strconv.Atoi(portText)
		if port >= 65535 {
			_ = listener.Close()
			continue
		}
		mainUDP, mainErr := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
		botUDP, botErr := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port + 1})
		_ = listener.Close()
		if mainUDP != nil {
			_ = mainUDP.Close()
		}
		if botUDP != nil {
			_ = botUDP.Close()
		}
		if mainErr == nil && botErr == nil {
			return address, nil
		}
	}
	return "", errors.New("could not reserve adjacent TCP and UDP load-test ports")
}

func waitForServer(ctx context.Context, address string) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("game server at %s was not ready within 5s", address)
		case <-ticker.C:
		}
	}
}

func connectClients(ctx context.Context, address string, clients []*loadClient, ramp time.Duration, metrics *measurements) error {
	var wait sync.WaitGroup
	errorsByClient := make([]error, len(clients))
	started := time.Now()
	var launchErr error

launchLoop:
	for index := range clients {
		if ramp > 0 {
			target := started.Add(time.Duration(index) * ramp / time.Duration(len(clients)))
			if delay := time.Until(target); delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					launchErr = ctx.Err()
					break launchLoop
				}
			}
		}
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			client, err := dialLoadClient(ctx, address, index, metrics)
			if err != nil {
				metrics.connectionFailed()
			}
			if err == nil {
				err = client.login(ctx)
			}
			if err != nil {
				if client != nil {
					client.close()
				}
				errorsByClient[index] = err
				return
			}
			clients[index] = client
		}(index)
	}
	wait.Wait()
	return errors.Join(launchErr, joinErrors(errorsByClient))
}

func connectUDPClients(ctx context.Context, address string, clients []*loadClient) error {
	return runClients(clients, func(client *loadClient) error {
		return client.registerUDP(ctx, address)
	})
}

func runCycle(ctx context.Context, clients []*loadClient, roomSize int, endGameTarget uint64, gameplay GameplayConfig, phases *gameplayPhaseMetrics) error {
	if err := runClients(clients, func(client *loadClient) error { return client.shopAndEquip(ctx) }); err != nil {
		return fmt.Errorf("shop and equipment: %w", err)
	}
	for waveIndex, groups := range groupWaves(makeGroups(clients, roomSize), maxRoomsPerWave) {
		if err := runRoomWave(ctx, groups, endGameTarget, gameplay, phases); err != nil {
			return fmt.Errorf("room wave %d: %w", waveIndex+1, err)
		}
	}
	return nil
}

func runRoomWave(ctx context.Context, groups []*clientGroup, endGameTarget uint64, gameplay GameplayConfig, phases *gameplayPhaseMetrics) error {
	if err := runGroups(groups, func(group *clientGroup) error {
		roomNumber, err := group.clients[0].allocateRoom(ctx, len(group.clients))
		if err != nil {
			return err
		}
		group.roomNumber = roomNumber
		for index, client := range group.clients[1:] {
			if err := client.enterRoom(ctx, roomNumber, byte(index+1)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("room creation and entry: %w", err)
	}
	clients := waveClients(groups)
	if err := runClients(clients, func(client *loadClient) error { return client.ready(ctx) }); err != nil {
		return fmt.Errorf("room ready: %w", err)
	}
	if err := runGroups(groups, func(group *clientGroup) error { return group.clients[0].beginMatch(ctx) }); err != nil {
		return fmt.Errorf("start game: %w", err)
	}
	if err := runClients(clients, func(client *loadClient) error { return client.enterGame(ctx) }); err != nil {
		return fmt.Errorf("enter game: %w", err)
	}
	if err := runClients(clients, func(client *loadClient) error { return client.beginRound(ctx) }); err != nil {
		return fmt.Errorf("start round: %w", err)
	}
	if err := runGameplayTraffic(ctx, groups, gameplay, phases); err != nil {
		return fmt.Errorf("continuous gameplay UDP: %w", err)
	}
	if err := runGroups(groups, func(group *clientGroup) error { return group.clients[0].recordKill(ctx, 0, 1) }); err != nil {
		return fmt.Errorf("gameplay: %w", err)
	}
	if err := runClients(clients, func(client *loadClient) error { return client.waitForEndGame(ctx, endGameTarget) }); err != nil {
		return fmt.Errorf("end game: %w", err)
	}
	if err := runClients(clients, func(client *loadClient) error { return client.gamePoints(ctx) }); err != nil {
		return fmt.Errorf("game points: %w", err)
	}
	if err := runGroups(groups, func(group *clientGroup) error {
		for index := len(group.clients) - 1; index >= 0; index-- {
			if err := group.clients[index].exitRoom(ctx); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("exit room: %w", err)
	}
	return nil
}

func validateCompletedReport(report Report) error {
	groups := report.RoomsPerCycle
	players := report.Players
	cycles := report.Cycles
	expected := map[string]int{
		"login":          players,
		"user_info":      players * cycles,
		"currency":       players * cycles,
		"item_list":      players * cycles,
		"equipment_list": players * cycles,
		"buy_item":       players * cycles,
		"save_equipment": players * cycles,
		"create_room":    groups * cycles,
		"enter_room":     (players - groups) * cycles,
		"ready":          players * cycles,
		"start_game":     groups * cycles,
		"enter_game":     players * cycles,
		"start_round":    players * cycles,
		"player_dead":    groups * cycles,
		"game_points":    players * cycles,
		"exit_room":      players * cycles,
	}
	var result error
	for name, count := range expected {
		if report.Latency[name].Count != count {
			result = errors.Join(result, fmt.Errorf("completed %s actions = %d, want %d", name, report.Latency[name].Count, count))
		}
	}
	expectedRequests := uint64(players + cycles*(12*players+2*groups))
	if report.Requests != expectedRequests {
		result = errors.Join(result, fmt.Errorf("requests = %d, want %d", report.Requests, expectedRequests))
	}
	if report.PendingWaiters != 0 {
		result = errors.Join(result, fmt.Errorf("pending response waiters = %d, want 0", report.PendingWaiters))
	}
	if report.UnexpectedDisconnects != 0 {
		result = errors.Join(result, fmt.Errorf("unexpected disconnects = %d, want 0", report.UnexpectedDisconnects))
	}
	if report.Network.ServerAcceptedConnections != report.Network.ServerClosedConnections {
		result = errors.Join(result, fmt.Errorf("server connections accepted/closed = %d/%d", report.Network.ServerAcceptedConnections, report.Network.ServerClosedConnections))
	}
	if report.Lobby.FailedBatches != 0 {
		result = errors.Join(result, fmt.Errorf("failed lobby delivery batches = %d (%d frames), want 0", report.Lobby.FailedBatches, report.Lobby.FailedFrames))
	}
	if report.Gameplay.Enabled {
		if report.Gameplay.RegistrationPacketsReceived < uint64(report.Players) {
			result = errors.Join(result, fmt.Errorf("UDP registrations acknowledged = %d, want at least %d", report.Gameplay.RegistrationPacketsReceived, report.Players))
		}
		if report.Gameplay.ExpectedRelayPackets == 0 || report.Gameplay.ReceivedRelayPackets == 0 {
			result = errors.Join(result, fmt.Errorf("gameplay relay packets expected/received = %d/%d, want non-zero traffic", report.Gameplay.ExpectedRelayPackets, report.Gameplay.ReceivedRelayPackets))
		}
		if report.Gameplay.SendErrors != 0 || report.Gameplay.ReadErrors != 0 {
			result = errors.Join(result, fmt.Errorf("gameplay UDP send/read errors = %d/%d, want 0", report.Gameplay.SendErrors, report.Gameplay.ReadErrors))
		}
	}
	return result
}

func waveClients(groups []*clientGroup) []*loadClient {
	count := 0
	for _, group := range groups {
		count += len(group.clients)
	}
	clients := make([]*loadClient, 0, count)
	for _, group := range groups {
		clients = append(clients, group.clients...)
	}
	return clients
}

func groupWaves(groups []*clientGroup, roomsPerWave int) [][]*clientGroup {
	if roomsPerWave < 1 {
		return nil
	}
	waves := make([][]*clientGroup, 0, (len(groups)+roomsPerWave-1)/roomsPerWave)
	for start := 0; start < len(groups); start += roomsPerWave {
		end := min(start+roomsPerWave, len(groups))
		waves = append(waves, groups[start:end])
	}
	return waves
}

func runClients(clients []*loadClient, action func(*loadClient) error) error {
	errorsByClient := make([]error, len(clients))
	var wait sync.WaitGroup
	for index, client := range clients {
		wait.Add(1)
		go func(index int, client *loadClient) {
			defer wait.Done()
			errorsByClient[index] = action(client)
		}(index, client)
	}
	wait.Wait()
	return joinErrors(errorsByClient)
}

func runGroups(groups []*clientGroup, action func(*clientGroup) error) error {
	errorsByGroup := make([]error, len(groups))
	var wait sync.WaitGroup
	for index, group := range groups {
		wait.Add(1)
		go func(index int, group *clientGroup) {
			defer wait.Done()
			errorsByGroup[index] = action(group)
		}(index, group)
	}
	wait.Wait()
	return joinErrors(errorsByGroup)
}

func makeGroups(clients []*loadClient, roomSize int) []*clientGroup {
	indexes := groupIndexes(len(clients), roomSize)
	groups := make([]*clientGroup, 0, len(indexes))
	for _, bounds := range indexes {
		groups = append(groups, &clientGroup{clients: clients[bounds[0]:bounds[1]], roomNumber: -1})
	}
	return groups
}

// groupIndexes avoids a one-player final room by borrowing one player from
// the preceding room. Every generated room can therefore form both teams.
func groupIndexes(players, roomSize int) [][2]int {
	groups := make([][2]int, 0, (players+roomSize-1)/roomSize)
	for start := 0; start < players; {
		end := min(start+roomSize, players)
		groups = append(groups, [2]int{start, end})
		start = end
	}
	if len(groups) > 1 {
		last := len(groups) - 1
		if groups[last][1]-groups[last][0] == 1 {
			groups[last-1][1]--
			groups[last][0]--
		}
	}
	return groups
}

func joinErrors(values []error) error {
	const maximumDetails = 10
	filtered := make([]error, 0, maximumDetails)
	total := 0
	for _, err := range values {
		if err != nil {
			total++
			if len(filtered) < maximumDetails {
				filtered = append(filtered, err)
			}
		}
	}
	result := errors.Join(filtered...)
	if total > len(filtered) {
		result = fmt.Errorf("%w\n... and %d more errors", result, total-len(filtered))
	}
	return result
}
