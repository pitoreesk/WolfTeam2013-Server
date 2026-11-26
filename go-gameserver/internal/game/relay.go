package game

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
)

// relayRouteKey identifies the peer-side NAT mapping that is reachable when
// packets originate from one dedicated relay port. NATs (including Docker
// Desktop's) may use a different mapping for every destination port.
type relayRouteKey struct {
	source    *Session
	recipient *Session
}

type relayProbeKey struct {
	targetPort uint16
	remote     netip.AddrPort
}

const maxPendingRelayPackets = 8

// RunRelay hosts the shared transparent peer relay. Direct endpoint mode still
// binds this socket so deployments can switch routing modes without changing
// the exposed service surface.
func (s *Server) RunRelay(ctx context.Context, address string) error {
	udpAddress, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return fmt.Errorf("resolve relay address: %w", err)
	}
	socket, err := net.ListenUDP("udp", udpAddress)
	if err != nil {
		return fmt.Errorf("listen for relay UDP: %w", err)
	}
	defer socket.Close()
	relayCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	host, _, splitErr := net.SplitHostPort(address)
	if splitErr != nil {
		return fmt.Errorf("split relay address: %w", splitErr)
	}
	s.relayMu.Lock()
	s.relayContext = relayCtx
	s.relayHost = host
	if s.relayPort == 0 {
		s.relayPort = uint16(udpAddress.Port)
	}
	s.relayMu.Unlock()
	defer s.closePerPlayerRelays()
	go func() {
		<-relayCtx.Done()
		_ = socket.Close()
	}()
	s.logger.Info("peer relay started", "address", socket.LocalAddr(), "endpoint_mode", s.peerEndpointMode)

	buffer := make([]byte, 2048)
	for {
		count, remote, readErr := socket.ReadFromUDP(buffer)
		if readErr != nil {
			if relayCtx.Err() != nil || errors.Is(readErr, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("read relay UDP: %w", readErr)
		}
		packet := append([]byte(nil), buffer[:count]...)
		s.forwardSharedRelay(socket, remote, packet, "shared", uint16(udpAddress.Port))
	}
}

func (s *Server) forwardSharedRelay(socket *net.UDPConn, remote *net.UDPAddr, packet []byte, path string, relayPort uint16) int {
	sender, recipients := s.relayRecipientsWithSender(remote)
	forwarded := 0
	for _, recipient := range recipients {
		if _, writeErr := socket.WriteToUDP(packet, recipient); writeErr != nil {
			s.logger.Debug("relay forward failed", "source", remote, "recipient", recipient, "path", path, "error", writeErr)
		} else {
			forwarded++
		}
	}
	if forwarded > 0 {
		s.logRelayUDPActivity(sender, nil, remote, path, relayPort, forwarded)
	}
	return forwarded
}

func (s *Server) peerEndpoints(session *Session) (globalIP netip.Addr, globalPort uint16, localIP netip.Addr, localPort uint16) {
	unspecified := netip.IPv4Unspecified()
	session.mu.RLock()
	directIP, directPort := session.UDPIP, session.UDPPort
	reportedIP, reportedPort, bot := session.LocalUDPIP, session.LocalUDPPort, session.IsBot
	session.mu.RUnlock()
	if !directIP.Is4() {
		directIP = unspecified
	}
	localIP, localPort = directIP, directPort
	if reportedIP.Is4() && reportedPort != 0 {
		localIP, localPort = reportedIP, reportedPort
		// Docker Desktop hides host peers behind its bridge gateway. When a peer
		// explicitly reports a loopback socket, that socket is the only endpoint
		// another peer on the same Windows host can reach;
		// the bridge gateway and translated port are internal to Docker.
		if reportedIP.IsLoopback() {
			directIP, directPort = reportedIP, reportedPort
		}
	}
	if bot {
		return directIP, directPort, directIP, directPort
	}
	s.relayMu.Lock()
	mode, relayIP, sharedPort := s.peerEndpointMode, s.relayIP, s.relayPort
	s.relayMu.Unlock()
	if !relayIP.Is4() {
		relayIP = unspecified
	}
	switch mode {
	case 0:
		port := s.assignedRelayPort(session)
		if port == 0 {
			port = sharedPort
		}
		return relayIP, port, relayIP, port
	case 2:
		// Hybrid mode keeps the peer-reported endpoint as its direct/LAN route,
		// while the global route must uniquely identify a peer within its room.
		// Dedicated ports may be reused by sessions in different rooms because
		// relay ownership and learned NAT routes are room/session scoped.
		if port := s.assignedRelayPort(session); port != 0 {
			return relayIP, port, localIP, localPort
		}
		return directIP, directPort, localIP, localPort
	default:
		return directIP, directPort, localIP, localPort
	}
}

func (s *Server) assignedRelayPort(session *Session) uint16 {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.RelayPort != 0 {
		return session.RelayPort
	}
	roomNumber := session.RoomNumber
	if roomNumber < 0 {
		return 0
	}

	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	if s.relayContext == nil || s.relayPortBase == 0 {
		return 0
	}
	for attempt := 0; attempt < 500; attempt++ {
		portNumber := int(s.relayPortBase) + attempt
		if portNumber > 65535 {
			break
		}
		port := uint16(portNumber)
		owners := s.relayOwners[port]
		if owners != nil && owners[roomNumber] != nil {
			continue
		}
		if s.relaySockets[port] == nil {
			address, err := net.ResolveUDPAddr("udp", net.JoinHostPort(s.relayHost, fmt.Sprint(port)))
			if err != nil {
				continue
			}
			socket, err := net.ListenUDP("udp", address)
			if err != nil {
				continue
			}
			s.relaySockets[port] = socket
			go s.runPerPlayerRelay(s.relayContext, socket, port)
			s.logger.Info("dedicated peer relay started", "port", port)
		}
		if owners == nil {
			owners = make(map[int16]*Session)
			s.relayOwners[port] = owners
		}
		owners[roomNumber] = session
		session.RelayPort = port
		return port
	}
	return 0
}

func (s *Server) runPerPlayerRelay(ctx context.Context, socket *net.UDPConn, port uint16) {
	buffer := make([]byte, 2048)
	for {
		count, remote, err := socket.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		packet := append([]byte(nil), buffer[:count]...)
		s.routePeerDatagram(remote, packet, port)
		if ctx.Err() != nil {
			return
		}
	}
}

func (s *Server) routePeerDatagram(remote *net.UDPAddr, packet []byte, targetPort uint16) {
	sourceIP, ok := netipFromUDP(remote)
	if !ok {
		return
	}
	remoteEndpoint := netip.AddrPortFrom(sourceIP, uint16(remote.Port))
	sender := s.sessionByUDPEndpoint(sourceIP, uint16(remote.Port), false)
	s.relayMu.Lock()
	owners := make(map[int16]*Session, len(s.relayOwners[targetPort]))
	for roomNumber, owner := range s.relayOwners[targetPort] {
		owners[roomNumber] = owner
	}
	s.relayMu.Unlock()

	var target *Session
	if sender != nil {
		sender.mu.RLock()
		roomNumber := sender.RoomNumber
		sender.mu.RUnlock()
		target = owners[roomNumber]
	} else {
		// A destination-dependent NAT can change the source port used for every
		// relay destination. First check if this remote socket was previously learned
		// as a known sender route.
		s.relayMu.Lock()
		for key, learnedAddr := range s.relayRoutes {
			if learnedAddr != nil && learnedAddr.IP.Equal(remote.IP) && learnedAddr.Port == remote.Port && key.source != nil {
				sender = key.source
				sender.mu.RLock()
				target = owners[sender.RoomNumber]
				sender.mu.RUnlock()
				break
			}
		}
		s.relayMu.Unlock()

		if sender == nil {
			for roomNumber, candidateTarget := range owners {
				room := s.roomByNumber(roomNumber)
				if room == nil {
					continue
				}
				target = candidateTarget
				candidates := make([]*Session, 0, len(room.Snapshot().Members))
				for _, candidate := range room.Snapshot().Members {
					if candidate == candidateTarget || candidate.IsBot {
						continue
					}
					candidate.mu.RLock()
					sameIP := candidate.UDPIP == sourceIP
					candidate.mu.RUnlock()
					if sameIP {
						candidates = append(candidates, candidate)
					}
				}
				if len(candidates) == 1 {
					sender = candidates[0]
					break
				} else if len(candidates) > 1 {
					s.relayMu.Lock()
					for _, candidate := range candidates {
						forwardKey := relayRouteKey{source: candidate, recipient: target}
						if s.relayRoutes[forwardKey] == nil {
							sender = candidate
							break
						}
					}
					s.relayMu.Unlock()
					if sender == nil {
						sender = candidates[0]
					}
					break
				}
			}
		}
	}

	s.relayMu.Lock()
	probeKey := relayProbeKey{targetPort: targetPort, remote: remoteEndpoint}
	_, observed := s.relayProbes[probeKey]
	if !observed {
		s.relayProbes[probeKey] = struct{}{}
	}
	s.relayMu.Unlock()
	if !observed {
		targetUsername, targetUserID := "", 0
		if target != nil {
			targetUsername, _, targetUserID, _ = target.identity()
		}
		s.logger.Info("peer relay UDP probe received", "remote", remoteEndpoint, "relay_port", targetPort,
			"target_user_id", targetUserID, "target_username", targetUsername, "bytes", len(packet))
	}
	if target == nil || sender == nil || sender == target {
		return
	}
	sender.mu.RLock()
	senderPort := sender.RelayPort
	sender.mu.RUnlock()
	target.mu.RLock()
	targetIP, targetUDPPort, targetLocalUDPPort := target.UDPIP, target.UDPPort, target.LocalUDPPort
	target.mu.RUnlock()
	if senderPort == 0 || !targetIP.Is4() || targetUDPPort == 0 {
		return
	}
	fallbacks := relayFallbackAddresses(targetIP, targetUDPPort, targetLocalUDPPort)
	s.relayMu.Lock()
	// A packet received on targetPort reveals where the sender can be reached
	// by a reply sourced from that same port. Remember it for the reverse flow.
	learnedKey := relayRouteKey{source: target, recipient: sender}
	forwardKey := relayRouteKey{source: sender, recipient: target}
	s.relayRoutes[learnedKey] = cloneUDPAddr(remote)
	senderSocket := s.relaySockets[senderPort]
	address := cloneUDPAddr(s.relayRoutes[forwardKey])
	pendingSocket := s.relaySockets[targetPort]
	pending := s.relayPending[learnedKey]
	delete(s.relayPending, learnedKey)
	if address == nil {
		queue := s.relayPending[forwardKey]
		if len(queue) < maxPendingRelayPackets {
			s.relayPending[forwardKey] = append(queue, append([]byte(nil), packet...))
		}
	}
	s.relayMu.Unlock()
	flushed := 0
	if pendingSocket != nil {
		for _, queued := range pending {
			if _, err := pendingSocket.WriteToUDP(queued, remote); err != nil {
				s.logger.Debug("pending dedicated relay forward failed", "recipient", remote, "error", err)
				continue
			}
			flushed++
		}
	}
	if flushed > 0 {
		s.logger.Info("pending peer relay packets flushed", "remote", remote, "relay_port", targetPort,
			"packets", flushed)
	}
	if senderSocket == nil {
		return
	}
	addresses := []*net.UDPAddr{address}
	if address == nil {
		addresses = fallbacks
	}
	forwarded := 0
	for _, recipient := range addresses {
		if _, err := senderSocket.WriteToUDP(packet, recipient); err != nil {
			s.logger.Debug("dedicated relay forward failed", "source", remote, "recipient", recipient, "error", err)
			continue
		}
		forwarded++
	}
	if forwarded > 0 {
		s.logRelayUDPActivity(sender, target, remote, "dedicated", targetPort, forwarded)
	}
}

func relayFallbackAddresses(address netip.Addr, observedPort, reportedPort uint16) []*net.UDPAddr {
	if !address.Is4() || observedPort == 0 {
		return nil
	}
	ip := address.AsSlice()
	result := []*net.UDPAddr{{IP: append(net.IP(nil), ip...), Port: int(observedPort)}}
	if reportedPort != 0 && reportedPort != observedPort {
		result = append(result, &net.UDPAddr{IP: append(net.IP(nil), ip...), Port: int(reportedPort)})
	}
	return result
}

func (s *Server) logRelayUDPActivity(sender, target *Session, remote *net.UDPAddr, path string, relayPort uint16, recipients int) {
	if sender == nil {
		return
	}
	address, ok := netipFromUDP(remote)
	if !ok || !sender.observeRelayUDP(netip.AddrPortFrom(address, uint16(remote.Port))) {
		return
	}
	username, _, userID, _ := sender.identity()
	attributes := []any{
		"path", path,
		"remote", remote,
		"relay_port", relayPort,
		"user_id", userID,
		"username", username,
		"recipients", recipients,
	}
	if target != nil {
		targetUsername, _, targetUserID, _ := target.identity()
		attributes = append(attributes, "target_user_id", targetUserID, "target_username", targetUsername)
	}
	s.logger.Info("peer relay UDP active", attributes...)
}

func cloneUDPAddr(address *net.UDPAddr) *net.UDPAddr {
	if address == nil {
		return nil
	}
	return &net.UDPAddr{IP: append(net.IP(nil), address.IP...), Port: address.Port, Zone: address.Zone}
}

func (s *Server) reclaimRelayPort(session *Session) {
	session.mu.Lock()
	port := session.RelayPort
	session.RelayPort = 0
	session.mu.Unlock()
	if port == 0 {
		return
	}
	s.relayMu.Lock()
	owners := s.relayOwners[port]
	for roomNumber, owner := range owners {
		if owner == session {
			delete(owners, roomNumber)
		}
	}
	var socket *net.UDPConn
	if len(owners) == 0 {
		socket = s.relaySockets[port]
		delete(s.relaySockets, port)
		delete(s.relayOwners, port)
	}
	for key := range s.relayRoutes {
		if key.source == session || key.recipient == session {
			delete(s.relayRoutes, key)
		}
	}
	for key := range s.relayPending {
		if key.source == session || key.recipient == session {
			delete(s.relayPending, key)
		}
	}
	if len(owners) == 0 {
		for key := range s.relayProbes {
			if key.targetPort == port {
				delete(s.relayProbes, key)
			}
		}
	}
	s.relayMu.Unlock()
	if socket != nil {
		_ = socket.Close()
	}
}

func (s *Server) closePerPlayerRelays() {
	s.relayMu.Lock()
	sockets := make([]*net.UDPConn, 0, len(s.relaySockets))
	for _, socket := range s.relaySockets {
		sockets = append(sockets, socket)
	}
	s.relaySockets = make(map[uint16]*net.UDPConn)
	s.relayOwners = make(map[uint16]map[int16]*Session)
	s.relayRoutes = make(map[relayRouteKey]*net.UDPAddr)
	s.relayPending = make(map[relayRouteKey][][]byte)
	s.relayProbes = make(map[relayProbeKey]struct{})
	s.relayContext = nil
	s.relayMu.Unlock()
	for _, socket := range sockets {
		_ = socket.Close()
	}
}

func (s *Server) relayRecipients(remote *net.UDPAddr) []*net.UDPAddr {
	_, recipients := s.relayRecipientsWithSender(remote)
	return recipients
}

func (s *Server) relayRecipientsWithSender(remote *net.UDPAddr) (*Session, []*net.UDPAddr) {
	sourceIP, ok := netipFromUDP(remote)
	if !ok {
		return nil, nil
	}
	sender := s.sessionByUDPEndpoint(sourceIP, uint16(remote.Port), true)
	if sender == nil {
		return nil, nil
	}
	room := s.sessionRoom(sender)
	if room == nil {
		return sender, nil
	}
	result := make([]*net.UDPAddr, 0, room.Users)
	for _, session := range room.Snapshot().Members {
		if session == sender {
			continue
		}
		session.mu.RLock()
		address, port, bot := session.UDPIP, session.UDPPort, session.IsBot
		session.mu.RUnlock()
		// Bots expose their own peer listener. Sending opaque shared-relay
		// traffic to that listener can make the game and bot sockets reflect the
		// same packet forever, and is unnecessary because peers address bots
		// directly through the roster endpoint.
		if bot || !address.Is4() || port == 0 {
			continue
		}
		ip := address.AsSlice()
		result = append(result, &net.UDPAddr{IP: append(net.IP(nil), ip...), Port: int(port)})
	}
	return sender, result
}
