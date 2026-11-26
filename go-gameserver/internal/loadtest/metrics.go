package loadtest

import (
	"runtime"
	runtimemetrics "runtime/metrics"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type LatencyReport struct {
	Count int           `json:"count"`
	P50   time.Duration `json:"p50"`
	P95   time.Duration `json:"p95"`
	P99   time.Duration `json:"p99"`
	Max   time.Duration `json:"max"`
}

type ResourceReport struct {
	BaselineHeapBytes   uint64  `json:"baseline_heap_bytes"`
	PeakHeapBytes       uint64  `json:"peak_heap_bytes"`
	PeakHeapInUse       uint64  `json:"peak_heap_in_use_bytes"`
	PeakSystemBytes     uint64  `json:"peak_system_bytes"`
	PeakGoroutines      int     `json:"peak_goroutines"`
	GarbageCollections  uint32  `json:"garbage_collections"`
	EstimatedCPUSeconds float64 `json:"estimated_cpu_seconds"`
	AverageCPUCores     float64 `json:"average_cpu_cores"`
}

type NetworkReport struct {
	ServerAcceptedConnections uint64  `json:"server_accepted_connections"`
	ServerClosedConnections   uint64  `json:"server_closed_connections"`
	ServerPeakConnections     int     `json:"server_peak_connections"`
	ServerReadCalls           uint64  `json:"server_read_calls"`
	ServerWriteCalls          uint64  `json:"server_write_calls"`
	ServerBytesRead           uint64  `json:"server_bytes_read"`
	ServerBytesWritten        uint64  `json:"server_bytes_written"`
	ClientReadCalls           uint64  `json:"client_read_calls"`
	ClientWriteCalls          uint64  `json:"client_write_calls"`
	ResponsesPerServerWrite   float64 `json:"responses_per_server_write"`
}

type LobbyReport struct {
	Flushes                uint64  `json:"flushes"`
	Events                 uint64  `json:"events"`
	RecipientBatches       uint64  `json:"recipient_batches"`
	DeliveredFrames        uint64  `json:"delivered_frames"`
	AudienceFilteredFrames uint64  `json:"audience_filtered_frames"`
	FailedBatches          uint64  `json:"failed_batches"`
	FailedFrames           uint64  `json:"failed_frames"`
	MaxEventsPerFlush      uint64  `json:"max_events_per_flush"`
	MaxFramesPerBatch      uint64  `json:"max_frames_per_batch"`
	FramesPerBatch         float64 `json:"frames_per_batch"`
}

// GameplayReport describes the UDP peer traffic generated while every room is
// actively playing. Expected relay figures are derived from successful writes
// and the number of other players in each room. Received figures are counted
// at the simulated peers, making packet loss visible independently
// from the server's expected egress volume.
type GameplayReport struct {
	Enabled                     bool          `json:"enabled"`
	DurationPerRoom             time.Duration `json:"duration_per_room"`
	ActiveDuration              time.Duration `json:"active_duration"`
	MovementPacketsPerSecond    int           `json:"movement_packets_per_second"`
	MovementPayloadBytes        int           `json:"movement_payload_bytes"`
	ShootingPacketsPerSecond    int           `json:"shooting_packets_per_second"`
	ShootingPayloadBytes        int           `json:"shooting_payload_bytes"`
	RegistrationPacketsSent     uint64        `json:"registration_packets_sent"`
	RegistrationPacketsReceived uint64        `json:"registration_packets_received"`
	MovementPacketsSent         uint64        `json:"movement_packets_sent"`
	MovementPacketsReceived     uint64        `json:"movement_packets_received"`
	ShootingPacketsSent         uint64        `json:"shooting_packets_sent"`
	ShootingPacketsReceived     uint64        `json:"shooting_packets_received"`
	ClientPayloadBytesSent      uint64        `json:"client_payload_bytes_sent"`
	ExpectedRelayPackets        uint64        `json:"expected_relay_packets"`
	ExpectedRelayPayloadBytes   uint64        `json:"expected_relay_payload_bytes"`
	ReceivedRelayPackets        uint64        `json:"received_relay_packets"`
	ReceivedRelayPayloadBytes   uint64        `json:"received_relay_payload_bytes"`
	ExpectedRelayIPv4Bytes      uint64        `json:"expected_relay_ipv4_bytes"`
	ReceivedRelayIPv4Bytes      uint64        `json:"received_relay_ipv4_bytes"`
	DeliveryRatio               float64       `json:"delivery_ratio"`
	SendErrors                  uint64        `json:"send_errors"`
	ReadErrors                  uint64        `json:"read_errors"`
	ExpectedAggregateMegabits   float64       `json:"expected_aggregate_megabits_per_second"`
	DeliveredAggregateMegabits  float64       `json:"delivered_aggregate_megabits_per_second"`
	ExpectedPerPlayerKilobits   float64       `json:"expected_per_player_kilobits_per_second"`
	ProjectedMonthlyEgressTB    float64       `json:"projected_monthly_egress_tb"`
}

type Report struct {
	Players               int                      `json:"players"`
	Cycles                int                      `json:"cycles"`
	RoomSize              int                      `json:"room_size"`
	RoomsPerCycle         int                      `json:"rooms_per_cycle"`
	RoomWavesPerCycle     int                      `json:"room_waves_per_cycle"`
	MaxRoomsPerWave       int                      `json:"max_rooms_per_wave"`
	Persistence           string                   `json:"persistence"`
	Duration              time.Duration            `json:"duration"`
	Requests              uint64                   `json:"requests"`
	Responses             uint64                   `json:"responses"`
	Errors                uint64                   `json:"errors"`
	UnexpectedDisconnects uint64                   `json:"unexpected_disconnects"`
	ConnectionFailures    uint64                   `json:"connection_failures"`
	ProtocolFailures      uint64                   `json:"protocol_failures"`
	PendingWaiters        int                      `json:"pending_waiters"`
	RequestsPerSecond     float64                  `json:"requests_per_second"`
	BytesSent             uint64                   `json:"bytes_sent"`
	BytesReceived         uint64                   `json:"bytes_received"`
	Latency               map[string]LatencyReport `json:"latency"`
	Resources             ResourceReport           `json:"resources"`
	Network               NetworkReport            `json:"network"`
	Lobby                 LobbyReport              `json:"lobby_updates"`
	Gameplay              GameplayReport           `json:"gameplay_udp"`
	Backend               backendStats             `json:"in_memory_backend"`
}

type measurements struct {
	requests              atomic.Uint64
	responses             atomic.Uint64
	errors                atomic.Uint64
	unexpectedDisconnects atomic.Uint64
	connectionFailures    atomic.Uint64
	protocolFailures      atomic.Uint64

	mu        sync.Mutex
	latencies map[string][]time.Duration
}

func newMeasurements() *measurements {
	return &measurements{latencies: make(map[string][]time.Duration)}
}

func (m *measurements) observe(name string, duration time.Duration) {
	m.requests.Add(1)
	m.mu.Lock()
	m.latencies[name] = append(m.latencies[name], duration)
	m.mu.Unlock()
}

func (m *measurements) failed() {
	m.requests.Add(1)
	m.errors.Add(1)
}

func (m *measurements) invalidResponse() {
	m.errors.Add(1)
	m.protocolFailures.Add(1)
}

func (m *measurements) connectionFailed() {
	m.errors.Add(1)
	m.connectionFailures.Add(1)
}

func (m *measurements) reports() map[string]LatencyReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]LatencyReport, len(m.latencies))
	for name, values := range m.latencies {
		copyOfValues := append([]time.Duration(nil), values...)
		sort.Slice(copyOfValues, func(i, j int) bool { return copyOfValues[i] < copyOfValues[j] })
		result[name] = LatencyReport{
			Count: len(copyOfValues),
			P50:   percentile(copyOfValues, 0.50),
			P95:   percentile(copyOfValues, 0.95),
			P99:   percentile(copyOfValues, 0.99),
			Max:   percentile(copyOfValues, 1),
		}
	}
	return result
}

func percentile(values []time.Duration, fraction float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1) * fraction)
	return values[index]
}

type resourceSampler struct {
	baseline       runtime.MemStats
	baselineCPU    float64
	peakHeap       atomic.Uint64
	peakInUse      atomic.Uint64
	peakSystem     atomic.Uint64
	peakGoroutines atomic.Int64
	done           chan struct{}
	stopped        chan struct{}
}

func startResourceSampler() *resourceSampler {
	r := &resourceSampler{done: make(chan struct{}), stopped: make(chan struct{})}
	runtime.ReadMemStats(&r.baseline)
	r.baselineCPU = consumedCPUSeconds()
	r.sample()
	go func() {
		defer close(r.stopped)
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				r.sample()
			case <-r.done:
				r.sample()
				return
			}
		}
	}()
	return r
}

func (r *resourceSampler) stop(elapsed time.Duration) ResourceReport {
	close(r.done)
	<-r.stopped
	var final runtime.MemStats
	runtime.ReadMemStats(&final)
	cpuSeconds := consumedCPUSeconds() - r.baselineCPU
	if cpuSeconds < 0 {
		cpuSeconds = 0
	}
	averageCores := float64(0)
	if elapsed > 0 {
		averageCores = cpuSeconds / elapsed.Seconds()
	}
	return ResourceReport{
		BaselineHeapBytes:   r.baseline.HeapAlloc,
		PeakHeapBytes:       r.peakHeap.Load(),
		PeakHeapInUse:       r.peakInUse.Load(),
		PeakSystemBytes:     r.peakSystem.Load(),
		PeakGoroutines:      int(r.peakGoroutines.Load()),
		GarbageCollections:  final.NumGC - r.baseline.NumGC,
		EstimatedCPUSeconds: cpuSeconds,
		AverageCPUCores:     averageCores,
	}
}

func (r *resourceSampler) sample() {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	storeMax(&r.peakHeap, stats.HeapAlloc)
	storeMax(&r.peakInUse, stats.HeapInuse)
	storeMax(&r.peakSystem, stats.Sys)
	goroutines := int64(runtime.NumGoroutine())
	for current := r.peakGoroutines.Load(); goroutines > current; current = r.peakGoroutines.Load() {
		if r.peakGoroutines.CompareAndSwap(current, goroutines) {
			break
		}
	}
}

func storeMax(target *atomic.Uint64, value uint64) {
	for current := target.Load(); value > current; current = target.Load() {
		if target.CompareAndSwap(current, value) {
			break
		}
	}
}

func consumedCPUSeconds() float64 {
	samples := []runtimemetrics.Sample{
		{Name: "/cpu/classes/total:cpu-seconds"},
		{Name: "/cpu/classes/idle:cpu-seconds"},
	}
	runtimemetrics.Read(samples)
	if samples[0].Value.Kind() != runtimemetrics.KindFloat64 || samples[1].Value.Kind() != runtimemetrics.KindFloat64 {
		return 0
	}
	return samples[0].Value.Float64() - samples[1].Value.Float64()
}
