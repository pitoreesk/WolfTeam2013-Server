package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"sort"
	"time"

	"wtserver-go/internal/loadtest"
)

func main() {
	os.Exit(run())
}

func run() int {
	players := flag.Int("players", 10_000, "number of concurrent simulated players")
	cycles := flag.Int("cycles", 1, "complete shop/room/match lifecycles per player")
	roomSize := flag.Int("room-size", 8, "maximum simulated players per room (2-16)")
	ramp := flag.Duration("ramp", 30*time.Second, "time over which client connections are opened")
	hold := flag.Duration("hold", 5*time.Second, "time to hold all connections after the workload")
	drain := flag.Duration("drain", 250*time.Millisecond, "time to drain unsolicited responses before closing clients")
	gameplayDuration := flag.Duration("gameplay-duration", 10*time.Second, "continuous movement/shooting time per active room wave (zero disables UDP gameplay)")
	movementRate := flag.Int("movement-pps", 10, "movement UDP packets per second per player")
	movementBytes := flag.Int("movement-bytes", 32, "movement UDP payload bytes per packet")
	shootingRate := flag.Int("shooting-pps", 1, "shooting UDP packets per second per player")
	shootingBytes := flag.Int("shooting-bytes", 48, "shooting UDP payload bytes per packet")
	timeout := flag.Duration("timeout", 30*time.Minute, "overall load-test timeout")
	jsonPath := flag.String("json", "", "optional path for the machine-readable JSON report")
	cpuProfile := flag.String("cpuprofile", "", "optional CPU profile output path")
	heapProfile := flag.String("heapprofile", "", "optional heap profile output path")
	verbose := flag.Bool("verbose", false, "show game server logs during the run")
	flag.Parse()

	stopCPU, err := startCPUProfile(*cpuProfile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start CPU profile:", err)
		return 2
	}
	defer stopCPU()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	report, runErr := loadtest.Run(ctx, loadtest.Config{
		Players:  *players,
		Cycles:   *cycles,
		RoomSize: *roomSize,
		Ramp:     *ramp,
		Hold:     *hold,
		Drain:    *drain,
		Gameplay: loadtest.GameplayConfig{
			Duration:                 *gameplayDuration,
			MovementPacketsPerSecond: *movementRate,
			MovementPayloadBytes:     *movementBytes,
			ShootingPacketsPerSecond: *shootingRate,
			ShootingPayloadBytes:     *shootingBytes,
		},
		Verbose:   *verbose,
		LogWriter: os.Stderr,
	})
	printReport(report)

	if *jsonPath != "" {
		if err := writeJSON(*jsonPath, report); err != nil {
			fmt.Fprintln(os.Stderr, "write JSON report:", err)
			return 2
		}
		fmt.Printf("JSON report: %s\n", *jsonPath)
	}
	if *heapProfile != "" {
		if err := writeHeapProfile(*heapProfile); err != nil {
			fmt.Fprintln(os.Stderr, "write heap profile:", err)
			return 2
		}
		fmt.Printf("Heap profile: %s\n", *heapProfile)
	}
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "load test failed:", runErr)
		return 1
	}
	return 0
}

func printReport(report loadtest.Report) {
	peakDelta := uint64(0)
	if report.Resources.PeakHeapBytes > report.Resources.BaselineHeapBytes {
		peakDelta = report.Resources.PeakHeapBytes - report.Resources.BaselineHeapBytes
	}
	fmt.Println("WT isolated player load test")
	fmt.Printf("Persistence: %s\n", report.Persistence)
	fmt.Printf("Players: %d  Cycles: %d  Rooms/cycle: %d  Room waves/cycle: %d (max %d concurrent)  Room size: %d\n",
		report.Players, report.Cycles, report.RoomsPerCycle, report.RoomWavesPerCycle, report.MaxRoomsPerWave, report.RoomSize)
	fmt.Printf("Duration: %s  Requests: %d  Responses: %d  Errors: %d  Rate: %.1f req/s\n",
		report.Duration.Round(time.Millisecond), report.Requests, report.Responses, report.Errors, report.RequestsPerSecond)
	fmt.Printf("TCP wire traffic: sent %.2f MiB  received %.2f MiB\n", mib(report.BytesSent), mib(report.BytesReceived))
	if report.Gameplay.Enabled {
		fmt.Printf("Gameplay UDP: %s/room wave  movement %d pps x %d B  shooting %d pps x %d B\n",
			report.Gameplay.DurationPerRoom, report.Gameplay.MovementPacketsPerSecond, report.Gameplay.MovementPayloadBytes,
			report.Gameplay.ShootingPacketsPerSecond, report.Gameplay.ShootingPayloadBytes)
		fmt.Printf("UDP relay: client sent %.2f MiB  expected/delivered egress %.2f/%.2f MiB  packets %d/%d  delivery %.2f%%\n",
			mib(report.Gameplay.ClientPayloadBytesSent), mib(report.Gameplay.ExpectedRelayPayloadBytes),
			mib(report.Gameplay.ReceivedRelayPayloadBytes), report.Gameplay.ExpectedRelayPackets,
			report.Gameplay.ReceivedRelayPackets, report.Gameplay.DeliveryRatio*100)
		fmt.Printf("UDP bandwidth (IPv4 + UDP headers): expected/delivered %.2f/%.2f Mbit/s  %.2f Kbit/s/player  projected %.2f TB/month at %d players\n",
			report.Gameplay.ExpectedAggregateMegabits, report.Gameplay.DeliveredAggregateMegabits,
			report.Gameplay.ExpectedPerPlayerKilobits, report.Gameplay.ProjectedMonthlyEgressTB, report.Players)
		fmt.Printf("UDP harness: registrations %d/%d  send/read errors %d/%d\n",
			report.Gameplay.RegistrationPacketsReceived, report.Gameplay.RegistrationPacketsSent,
			report.Gameplay.SendErrors, report.Gameplay.ReadErrors)
	}
	fmt.Printf("Server TCP: accepted %d  closed %d  peak %d  reads %d  writes %d  responses/write %.2f\n",
		report.Network.ServerAcceptedConnections, report.Network.ServerClosedConnections, report.Network.ServerPeakConnections,
		report.Network.ServerReadCalls, report.Network.ServerWriteCalls, report.Network.ResponsesPerServerWrite)
	fmt.Printf("Server wire: read %.2f MiB  wrote %.2f MiB  local client calls: reads %d  writes %d\n",
		mib(report.Network.ServerBytesRead), mib(report.Network.ServerBytesWritten),
		report.Network.ClientReadCalls, report.Network.ClientWriteCalls)
	fmt.Printf("Lobby batching: flushes %d  events %d  recipient batches %d  frames %d  average %.2f frames/batch  max %d  audience-filtered %d  failed batches/frames %d/%d\n",
		report.Lobby.Flushes, report.Lobby.Events, report.Lobby.RecipientBatches, report.Lobby.DeliveredFrames,
		report.Lobby.FramesPerBatch, report.Lobby.MaxFramesPerBatch, report.Lobby.AudienceFilteredFrames,
		report.Lobby.FailedBatches, report.Lobby.FailedFrames)
	fmt.Printf("Resources (server + local clients): heap baseline %.2f MiB  peak %.2f MiB (+%.2f MiB)  heap in-use %.2f MiB  system %.2f MiB\n",
		mib(report.Resources.BaselineHeapBytes), mib(report.Resources.PeakHeapBytes), mib(peakDelta),
		mib(report.Resources.PeakHeapInUse), mib(report.Resources.PeakSystemBytes))
	fmt.Printf("Peak goroutines: %d  GC cycles: %d\n", report.Resources.PeakGoroutines, report.Resources.GarbageCollections)
	fmt.Printf("Harness integrity: connection failures %d  protocol failures %d  unexpected disconnects %d  pending waiters %d\n",
		report.ConnectionFailures, report.ProtocolFailures, report.UnexpectedDisconnects, report.PendingWaiters)
	fmt.Printf("Estimated Go CPU: %.2f CPU-s  average %.2f logical cores (server + local clients)\n",
		report.Resources.EstimatedCPUSeconds, report.Resources.AverageCPUCores)
	fmt.Printf("In-memory mutations: purchases %d  equipment writes %d  match results %d  total backend operations %d\n",
		report.Backend.Purchases, report.Backend.EquipmentWrites, report.Backend.MatchResults, report.Backend.Operations)

	names := make([]string, 0, len(report.Latency))
	for name := range report.Latency {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Println("Latency by completed action:")
	for _, name := range names {
		latency := report.Latency[name]
		fmt.Printf("  %-18s n=%-5d p50=%-9s p95=%-9s p99=%-9s max=%s\n",
			name, latency.Count, compactDuration(latency.P50), compactDuration(latency.P95), compactDuration(latency.P99), compactDuration(latency.Max))
	}
}

func startCPUProfile(path string) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	if err := pprof.StartCPUProfile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		pprof.StopCPUProfile()
		_ = file.Close()
	}, nil
}

func writeHeapProfile(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	runtime.GC()
	return pprof.WriteHeapProfile(file)
}

func writeJSON(path string, report loadtest.Report) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func mib(value uint64) float64 { return float64(value) / (1024 * 1024) }

func compactDuration(value time.Duration) time.Duration {
	if value >= time.Second {
		return value.Round(time.Millisecond)
	}
	if value >= time.Millisecond {
		return value.Round(10 * time.Microsecond)
	}
	return value.Round(time.Microsecond)
}
