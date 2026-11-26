package loadtest

import (
	"net"
	"sync"
	"sync/atomic"
)

type serverNetworkMetrics struct {
	accepted    atomic.Uint64
	closed      atomic.Uint64
	active      atomic.Int64
	peak        atomic.Int64
	mu          sync.Mutex
	connections []*serverCountingConn
}

func (metrics *serverNetworkMetrics) wrap(connection net.Conn) net.Conn {
	metrics.accepted.Add(1)
	active := metrics.active.Add(1)
	for peak := metrics.peak.Load(); active > peak; peak = metrics.peak.Load() {
		if metrics.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	counted := &serverCountingConn{Conn: connection, metrics: metrics}
	metrics.mu.Lock()
	metrics.connections = append(metrics.connections, counted)
	metrics.mu.Unlock()
	return counted
}

func (metrics *serverNetworkMetrics) snapshot() NetworkReport {
	report := NetworkReport{
		ServerAcceptedConnections: metrics.accepted.Load(),
		ServerClosedConnections:   metrics.closed.Load(),
		ServerPeakConnections:     int(metrics.peak.Load()),
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	for _, connection := range metrics.connections {
		report.ServerReadCalls += connection.readCalls.Load()
		report.ServerBytesRead += connection.readBytes.Load()
		report.ServerWriteCalls += connection.writeCalls.Load()
		report.ServerBytesWritten += connection.writeBytes.Load()
	}
	return report
}

type serverCountingConn struct {
	net.Conn
	metrics    *serverNetworkMetrics
	close      sync.Once
	err        error
	readCalls  atomic.Uint64
	readBytes  atomic.Uint64
	writeCalls atomic.Uint64
	writeBytes atomic.Uint64
}

func (connection *serverCountingConn) Read(buffer []byte) (int, error) {
	count, err := connection.Conn.Read(buffer)
	connection.readCalls.Add(1)
	connection.readBytes.Add(uint64(count))
	return count, err
}

func (connection *serverCountingConn) Write(buffer []byte) (int, error) {
	count, err := connection.Conn.Write(buffer)
	connection.writeCalls.Add(1)
	connection.writeBytes.Add(uint64(count))
	return count, err
}

func (connection *serverCountingConn) Close() error {
	connection.close.Do(func() {
		connection.err = connection.Conn.Close()
		connection.metrics.closed.Add(1)
		connection.metrics.active.Add(-1)
	})
	return connection.err
}
