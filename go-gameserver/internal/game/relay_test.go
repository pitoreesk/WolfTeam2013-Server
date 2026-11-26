package game

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRelayRecipientsStayInsideRoom(t *testing.T) {
	server := NewServer("unused", 40707, 40708, netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("203.0.113.152"), &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	room := newRoom(1, RoomConfig{Capacity: 8})
	sender := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: netip.MustParseAddr("10.0.0.1"), UDPPort: 5001}
	recipient := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: netip.MustParseAddr("10.0.0.2"), UDPPort: 5002}
	outsider := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: netip.MustParseAddr("10.0.0.3"), UDPPort: 5003}
	if _, ok := room.Add(sender); !ok {
		t.Fatal("add sender")
	}
	if _, ok := room.Add(recipient); !ok {
		t.Fatal("add recipient")
	}
	if bot := room.ProvisionBot(2, 1, false, 40708, 0); bot == nil {
		t.Fatal("add bot")
	}
	server.rooms[1] = room
	server.sessions[sender] = struct{}{}
	server.sessions[recipient] = struct{}{}
	server.sessions[outsider] = struct{}{}
	trackTestConnections(server, sender, recipient, outsider)

	targets := server.relayRecipients(&net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 5001})
	if len(targets) != 1 || targets[0].IP.String() != "10.0.0.2" || targets[0].Port != 5002 {
		t.Fatalf("relay targets = %v", targets)
	}
}

func TestPeerEndpointModes(t *testing.T) {
	server := NewServer("unused", 40707, 40708, netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("203.0.113.152"), &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	session := &Session{RoomNumber: 1, UDPIP: netip.MustParseAddr("10.0.0.1"), UDPPort: 5000}
	globalIP, globalPort, localIP, localPort := server.peerEndpoints(session)
	if globalIP.String() != "10.0.0.1" || localIP != globalIP || globalPort != 5000 || localPort != 5000 {
		t.Fatalf("direct endpoints = %s:%d %s:%d", globalIP, globalPort, localIP, localPort)
	}
	base := configureTestRelay(t, server, netip.MustParseAddr("203.0.113.3"), 2)
	globalIP, globalPort, localIP, localPort = server.peerEndpoints(session)
	if globalIP.String() != "203.0.113.3" || globalPort != base || localIP.String() != "10.0.0.1" || localPort != 5000 {
		t.Fatalf("hybrid endpoints = %s:%d %s:%d", globalIP, globalPort, localIP, localPort)
	}
	if session.RelayPort != base {
		t.Fatalf("hybrid allocated relay port %d, want %d", session.RelayPort, base)
	}
}

func TestHybridPeerEndpointUsesClientReportedLocalAddress(t *testing.T) {
	server := NewServer("unused", 40707, 40708, netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("203.0.113.152"), &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	base := configureTestRelay(t, server, netip.MustParseAddr("127.0.0.1"), 2)
	session := &Session{
		RoomNumber:   1,
		UDPIP:        netip.MustParseAddr("172.21.0.1"),
		UDPPort:      59300,
		LocalUDPIP:   netip.MustParseAddr("127.0.0.1"),
		LocalUDPPort: 28704,
	}

	globalIP, globalPort, localIP, localPort := server.peerEndpoints(session)
	if globalIP.String() != "127.0.0.1" || globalPort != base {
		t.Fatalf("global endpoint = %s:%d, want dedicated relay 127.0.0.1:%d", globalIP, globalPort, base)
	}
	if localIP.String() != "127.0.0.1" || localPort != 28704 {
		t.Fatalf("local endpoint = %s:%d, want reported 127.0.0.1:28704", localIP, localPort)
	}
}

func TestEnterMemberKeepsHybridDirectEndpoint(t *testing.T) {
	server := NewServer("unused", 40707, 40708, netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("203.0.113.152"), &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	base := configureTestRelay(t, server, netip.MustParseAddr("203.0.113.3"), 2)
	session := &Session{
		RoomNumber: 1,
		PeerIP:     netip.MustParseAddr("172.21.0.1"),
		UDPIP:      netip.MustParseAddr("10.0.0.1"),
		UDPPort:    5000,
	}

	member := server.enterMember(session, rosterState{})
	if member.GlobalIP.String() != "203.0.113.3" || member.GlobalPort != base {
		t.Fatalf("enter global endpoint = %s:%d, want dedicated relay 203.0.113.3:%d", member.GlobalIP, member.GlobalPort, base)
	}
	if member.LocalIP.String() != "10.0.0.1" || member.LocalPort != 5000 {
		t.Fatalf("enter local endpoint = %s:%d, want direct 10.0.0.1:5000", member.LocalIP, member.LocalPort)
	}
}

func TestRelayPortsAreUniqueWithinRoomAndReusableAcrossRooms(t *testing.T) {
	server := NewServer("unused", 40707, 40708, netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.1"), &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	base := configureTestRelay(t, server, netip.MustParseAddr("127.0.0.1"), 2)
	firstRoom := newRoom(1, RoomConfig{Capacity: 8})
	secondRoom := newRoom(2, RoomConfig{Capacity: 8})
	first := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: netip.MustParseAddr("10.0.0.1"), UDPPort: 5001}
	second := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: netip.MustParseAddr("10.0.0.2"), UDPPort: 5002}
	otherRoom := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: netip.MustParseAddr("10.0.0.3"), UDPPort: 5003}
	otherRoomPeer := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: netip.MustParseAddr("10.0.0.5"), UDPPort: 5005}
	for _, session := range []*Session{first, second} {
		if _, ok := firstRoom.Add(session); !ok {
			t.Fatal("add session to first room")
		}
	}
	for _, session := range []*Session{otherRoom, otherRoomPeer} {
		if _, ok := secondRoom.Add(session); !ok {
			t.Fatal("add session to second room")
		}
	}
	server.rooms[firstRoom.Number] = firstRoom
	server.rooms[secondRoom.Number] = secondRoom
	trackTestConnections(server, first, second, otherRoom, otherRoomPeer)

	_, firstPort, _, _ := server.peerEndpoints(first)
	_, secondPort, _, _ := server.peerEndpoints(second)
	_, otherRoomPort, _, _ := server.peerEndpoints(otherRoom)
	_, otherRoomPeerPort, _, _ := server.peerEndpoints(otherRoomPeer)
	if firstPort != base || secondPort != base+1 {
		t.Fatalf("first-room relay ports = %d/%d, want %d/%d", firstPort, secondPort, base, base+1)
	}
	if otherRoomPort != base || otherRoomPeerPort != base+1 {
		t.Fatalf("second-room relay ports = %d/%d, want reusable ports %d/%d", otherRoomPort, otherRoomPeerPort, base, base+1)
	}

	// A shared physical port still resolves its target through the sender's
	// room. Learning first -> second must not create a route to the peer that
	// owns the same target port in room 2.
	server.routePeerDatagram(&net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 5001}, []byte{1}, secondPort)
	server.relayMu.Lock()
	_, learnedFirstRoomRoute := server.relayRoutes[relayRouteKey{source: second, recipient: first}]
	_, leakedCrossRoomRoute := server.relayRoutes[relayRouteKey{source: otherRoomPeer, recipient: first}]
	server.relayMu.Unlock()
	if !learnedFirstRoomRoute || leakedCrossRoomRoute {
		t.Fatalf("room-scoped routing learned=%t leaked=%t", learnedFirstRoomRoute, leakedCrossRoomRoute)
	}

	replacement := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: netip.MustParseAddr("10.0.0.4"), UDPPort: 5004}
	if err := server.leaveRoom(first, false); err != nil {
		t.Fatal(err)
	}
	if first.RelayPort != 0 {
		t.Fatalf("departing session retained relay port %d", first.RelayPort)
	}
	if _, ok := firstRoom.Add(replacement); !ok {
		t.Fatal("add replacement to first room")
	}
	_, replacementPort, _, _ := server.peerEndpoints(replacement)
	if replacementPort != base {
		t.Fatalf("replacement relay port = %d, want released port %d", replacementPort, base)
	}

	server.relayMu.Lock()
	owners := server.relayOwners[base]
	firstRoomOwner, secondRoomOwner := owners[firstRoom.Number], owners[secondRoom.Number]
	server.relayMu.Unlock()
	if firstRoomOwner != replacement || secondRoomOwner != otherRoom {
		t.Fatalf("reused relay owners = room 1 %p, room 2 %p", firstRoomOwner, secondRoomOwner)
	}
}

func TestDedicatedRelayPreservesAdvertisedSenderPort(t *testing.T) {
	var logs synchronizedBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	server := NewServer("unused", 40707, 40708, netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("203.0.113.152"), &fakeUsers{}, logger)
	base := findConsecutiveUDPPorts(t)
	server.ConfigureRelay(netip.MustParseAddr("127.0.0.1"), 40710, uint16(base), 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.relayMu.Lock()
	server.relayContext = ctx
	server.relayHost = "127.0.0.1"
	server.relayMu.Unlock()
	defer server.closePerPlayerRelays()

	senderSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer senderSocket.Close()
	targetSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer targetSocket.Close()
	senderPort := uint16(senderSocket.LocalAddr().(*net.UDPAddr).Port)
	targetPort := uint16(targetSocket.LocalAddr().(*net.UDPAddr).Port)
	sender := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: netip.MustParseAddr("127.0.0.1"), UDPPort: senderPort}
	target := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: netip.MustParseAddr("127.0.0.1"), UDPPort: targetPort}
	sender.setAuthenticated("sender", 41, "Sender", 7)
	target.setAuthenticated("target", 42, "Target", 7)
	room := newRoom(1, RoomConfig{Capacity: 8})
	if _, ok := room.Add(sender); !ok {
		t.Fatal("add sender")
	}
	if _, ok := room.Add(target); !ok {
		t.Fatal("add target")
	}
	server.rooms[1] = room
	server.sessions[sender], server.sessions[target] = struct{}{}, struct{}{}
	trackTestConnections(server, sender, target)

	_, senderRelay, _, _ := server.peerEndpoints(sender)
	_, targetRelay, _, _ := server.peerEndpoints(target)
	if senderRelay == 40710 || targetRelay == 40710 || senderRelay == targetRelay {
		t.Fatalf("dedicated ports = %d/%d", senderRelay, targetRelay)
	}
	packet := []byte{1, 2, 3, 4}
	if _, err := senderSocket.WriteToUDP(packet, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(targetRelay)}); err != nil {
		t.Fatal(err)
	}
	_ = targetSocket.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, 32)
	count, remote, err := targetSocket.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if string(buffer[:count]) != string(packet) || remote.Port != int(senderRelay) {
		t.Fatalf("forwarded %x from %s, want %x from port %d", buffer[:count], remote, packet, senderRelay)
	}
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(logs.String(), "peer relay UDP active") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	output := logs.String()
	if !strings.Contains(output, "peer relay UDP probe received") ||
		!strings.Contains(output, "peer relay UDP active") || !strings.Contains(output, "path=dedicated") ||
		!strings.Contains(output, "user_id=41") || !strings.Contains(output, "target_user_id=42") {
		t.Fatalf("relay logs = %q", output)
	}
}

func TestDedicatedRelayLearnsDestinationSpecificNATRoute(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer("unused", 40707, 40708, netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("203.0.113.152"), &fakeUsers{}, logger)
	base := findConsecutiveUDPPorts(t)
	server.ConfigureRelay(netip.MustParseAddr("127.0.0.1"), 40710, uint16(base), 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.relayMu.Lock()
	server.relayContext = ctx
	server.relayHost = "127.0.0.1"
	server.relayMu.Unlock()
	defer server.closePerPlayerRelays()

	senderSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer senderSocket.Close()
	targetSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer targetSocket.Close()

	// These registered ports intentionally differ from the socket source ports,
	// reproducing Docker Desktop's destination-specific UDP NAT.
	sender := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: netip.MustParseAddr("127.0.0.1"), UDPPort: 31001}
	target := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: netip.MustParseAddr("127.0.0.1"), UDPPort: 31002}
	room := newRoom(1, RoomConfig{Capacity: 8})
	if _, ok := room.Add(sender); !ok {
		t.Fatal("add sender")
	}
	if _, ok := room.Add(target); !ok {
		t.Fatal("add target")
	}
	server.rooms[1] = room
	server.sessions[sender], server.sessions[target] = struct{}{}, struct{}{}
	trackTestConnections(server, sender, target)

	_, senderRelay, _, _ := server.peerEndpoints(sender)
	_, targetRelay, _, _ := server.peerEndpoints(target)
	if senderRelay == 40710 || targetRelay == 40710 || senderRelay == targetRelay {
		t.Fatalf("dedicated ports = %d/%d", senderRelay, targetRelay)
	}

	// The target's probe to the sender relay teaches the return mapping that is
	// valid for datagrams sourced from senderRelay.
	if _, err := targetSocket.WriteToUDP([]byte{0xaa}, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(senderRelay)}); err != nil {
		t.Fatal(err)
	}
	routeKey := relayRouteKey{source: sender, recipient: target}
	deadline := time.Now().Add(2 * time.Second)
	for {
		server.relayMu.Lock()
		_, learned := server.relayRoutes[routeKey]
		server.relayMu.Unlock()
		if learned {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("relay did not learn the target NAT route")
		}
		time.Sleep(5 * time.Millisecond)
	}

	packet := []byte{1, 2, 3, 4}
	if _, err := senderSocket.WriteToUDP(packet, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(targetRelay)}); err != nil {
		t.Fatal(err)
	}
	_ = targetSocket.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, 32)
	count, remote, err := targetSocket.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if string(buffer[:count]) != string(packet) || remote.Port != int(senderRelay) {
		t.Fatalf("forwarded %x from %s, want %x from port %d", buffer[:count], remote, packet, senderRelay)
	}
}

func TestDedicatedRelayDeliversPacketSentBeforeReverseNATRoute(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer("unused", 40707, 40708, netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("203.0.113.152"), &fakeUsers{}, logger)
	base := findConsecutiveUDPPorts(t)
	server.ConfigureRelay(netip.MustParseAddr("127.0.0.1"), 40710, uint16(base), 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.relayMu.Lock()
	server.relayContext = ctx
	server.relayHost = "127.0.0.1"
	server.relayMu.Unlock()
	defer server.closePerPlayerRelays()

	senderSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer senderSocket.Close()
	targetSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer targetSocket.Close()

	// Port 1 is deliberately not the actual socket port. This reproduces a
	// registration mapping that cannot receive packets from a dedicated relay
	// port until that destination-specific route has been learned.
	sender := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: netip.MustParseAddr("127.0.0.1"), UDPPort: 1}
	target := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: netip.MustParseAddr("127.0.0.1"), UDPPort: 1}
	room := newRoom(1, RoomConfig{Capacity: 8})
	if _, ok := room.Add(sender); !ok {
		t.Fatal("add sender")
	}
	if _, ok := room.Add(target); !ok {
		t.Fatal("add target")
	}
	server.rooms[1] = room
	server.sessions[sender], server.sessions[target] = struct{}{}, struct{}{}
	trackTestConnections(server, sender, target)

	_, senderRelay, _, _ := server.peerEndpoints(sender)
	_, targetRelay, _, _ := server.peerEndpoints(target)
	packet := []byte{1, 2, 3, 4}
	if _, err := senderSocket.WriteToUDP(packet, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(targetRelay)}); err != nil {
		t.Fatal(err)
	}

	// Wait until the first probe has been processed before the target reveals
	// its reverse destination-specific mapping.
	routeKey := relayRouteKey{source: target, recipient: sender}
	deadline := time.Now().Add(2 * time.Second)
	for {
		server.relayMu.Lock()
		_, learned := server.relayRoutes[routeKey]
		server.relayMu.Unlock()
		if learned {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("relay did not process the initial sender probe")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, err := targetSocket.WriteToUDP([]byte{0xaa}, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(senderRelay)}); err != nil {
		t.Fatal(err)
	}
	_ = targetSocket.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, 32)
	count, remote, err := targetSocket.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if string(buffer[:count]) != string(packet) || remote.Port != int(senderRelay) {
		t.Fatalf("forwarded %x from %s, want queued %x from port %d", buffer[:count], remote, packet, senderRelay)
	}
}

func TestDedicatedRelayUsesReportedPortBeforeReverseNATRoute(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer("unused", 40707, 40708, netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("203.0.113.152"), &fakeUsers{}, logger)
	base := findConsecutiveUDPPorts(t)
	server.ConfigureRelay(netip.MustParseAddr("127.0.0.1"), 40710, uint16(base), 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.relayMu.Lock()
	server.relayContext = ctx
	server.relayHost = "127.0.0.1"
	server.relayMu.Unlock()
	defer server.closePerPlayerRelays()

	senderSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer senderSocket.Close()
	targetSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer targetSocket.Close()

	targetLocalPort := uint16(targetSocket.LocalAddr().(*net.UDPAddr).Port)
	sender := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: netip.MustParseAddr("127.0.0.1"), UDPPort: 1}
	target := &Session{
		RoomNumber: -1, RoomSlot: -1, UDPIP: netip.MustParseAddr("127.0.0.1"), UDPPort: 1,
		LocalUDPIP: netip.MustParseAddr("127.0.0.1"), LocalUDPPort: targetLocalPort,
	}
	room := newRoom(1, RoomConfig{Capacity: 8})
	if _, ok := room.Add(sender); !ok {
		t.Fatal("add sender")
	}
	if _, ok := room.Add(target); !ok {
		t.Fatal("add target")
	}
	server.rooms[1] = room
	server.sessions[sender], server.sessions[target] = struct{}{}, struct{}{}
	trackTestConnections(server, sender, target)

	_, senderRelay, _, _ := server.peerEndpoints(sender)
	_, targetRelay, _, _ := server.peerEndpoints(target)
	packet := []byte{1, 2, 3, 4}
	if _, err := senderSocket.WriteToUDP(packet, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(targetRelay)}); err != nil {
		t.Fatal(err)
	}
	_ = targetSocket.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, 32)
	count, remote, err := targetSocket.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if string(buffer[:count]) != string(packet) || remote.Port != int(senderRelay) {
		t.Fatalf("forwarded %x from %s, want %x from reported source port %d", buffer[:count], remote, packet, senderRelay)
	}
}

func findConsecutiveUDPPorts(t *testing.T) int {
	t.Helper()
	for port := 50000; port < 60000; port += 2 {
		first, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
		if err != nil {
			continue
		}
		second, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port + 1})
		if err == nil {
			_ = first.Close()
			_ = second.Close()
			return port
		}
		_ = first.Close()
	}
	t.Fatal("no consecutive UDP ports available")
	return 0
}

func configureTestRelay(t *testing.T, server *Server, relayIP netip.Addr, mode int) uint16 {
	t.Helper()
	base := uint16(findConsecutiveUDPPorts(t))
	server.ConfigureRelay(relayIP, 40710, base, mode)
	ctx, cancel := context.WithCancel(context.Background())
	server.relayMu.Lock()
	server.relayContext = ctx
	server.relayHost = "127.0.0.1"
	server.relayMu.Unlock()
	t.Cleanup(func() {
		cancel()
		server.closePerPlayerRelays()
	})
	return base
}

type synchronizedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *synchronizedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(value)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}
