package game

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"

	wirecrypto "wtserver-go/internal/crypto"
	"wtserver-go/internal/protocol"
)

type matchCodecResolver struct {
	server    *Server
	mu        sync.Mutex
	byAddress map[netip.Addr]*wirecrypto.MatchCodec
}

func newMatchCodecResolver(server *Server) *matchCodecResolver {
	return &matchCodecResolver{
		server:    server,
		byAddress: make(map[netip.Addr]*wirecrypto.MatchCodec),
	}
}

func (r *matchCodecResolver) candidates() ([]*wirecrypto.MatchCodec, error) {
	addresses := []netip.Addr{r.server.advertisedIP, r.server.publicIP}
	for _, session := range r.server.channelSessions() {
		addresses = append(addresses, session.AdvertisedIP)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	seen := make(map[netip.Addr]struct{}, len(addresses))
	result := make([]*wirecrypto.MatchCodec, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !address.Is4() {
			continue
		}
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}
		codec := r.byAddress[address]
		if codec == nil {
			var err error
			codec, err = wirecrypto.NewMatchCodec(address, r.server.matchPort)
			if err != nil {
				return nil, fmt.Errorf("initialize match codec for %s:%d: %w", address, r.server.matchPort, err)
			}
			r.byAddress[address] = codec
		}
		result = append(result, codec)
	}
	return result, nil
}

func (s *Server) RunUDP(ctx context.Context) error {
	mainAddress, err := net.ResolveUDPAddr("udp", s.address)
	if err != nil {
		return fmt.Errorf("resolve game UDP address: %w", err)
	}
	host, portText, err := net.SplitHostPort(s.address)
	if err != nil {
		return fmt.Errorf("split game UDP address: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port >= 65535 {
		return fmt.Errorf("invalid game UDP port %q", portText)
	}
	botPort := int(s.botPeerPortBase)
	if botPort <= 0 || botPort > 65535 {
		return fmt.Errorf("invalid bot-peer UDP port %d", botPort)
	}
	botAddress, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(botPort)))
	if err != nil {
		return fmt.Errorf("resolve bot-peer UDP address: %w", err)
	}
	mainSocket, err := net.ListenUDP("udp", mainAddress)
	if err != nil {
		return fmt.Errorf("listen for game UDP: %w", err)
	}
	defer mainSocket.Close()
	botSocket, err := net.ListenUDP("udp", botAddress)
	if err != nil {
		return fmt.Errorf("listen for bot-peer UDP: %w", err)
	}
	defer botSocket.Close()

	matchCodecs := newMatchCodecResolver(s)
	initialCodecs, err := matchCodecs.candidates()
	if err != nil {
		return err
	}
	s.logger.Info("game UDP server started", "address", mainSocket.LocalAddr(), "match_codec_endpoints", len(initialCodecs))
	s.logger.Info("bot-peer UDP server started", "address", botSocket.LocalAddr(), "match_codec_endpoints", len(initialCodecs))

	listenerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-listenerCtx.Done()
		_ = mainSocket.Close()
		_ = botSocket.Close()
	}()
	results := make(chan error, 2)
	go func() { results <- s.runUDPListener(listenerCtx, mainSocket, matchCodecs) }()
	go func() { results <- s.runUDPListener(listenerCtx, botSocket, matchCodecs) }()
	first := <-results
	cancel()
	_ = mainSocket.Close()
	_ = botSocket.Close()
	second := <-results
	if first != nil {
		return first
	}
	return second
}

func (s *Server) runUDPListener(ctx context.Context, socket *net.UDPConn, matchCodecs *matchCodecResolver) error {
	buffer := make([]byte, 2048)
	for {
		count, remote, readErr := socket.ReadFromUDP(buffer)
		if readErr != nil {
			if ctx.Err() != nil || errors.Is(readErr, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("read UDP on %s: %w", socket.LocalAddr(), readErr)
		}
		packet := append([]byte(nil), buffer[:count]...)
		candidates, resolveErr := matchCodecs.candidates()
		if resolveErr != nil {
			return resolveErr
		}
		if handleErr := s.handleUDPWithCryptos(ctx, socket, remote, packet, candidates); handleErr != nil {
			s.logger.Debug("game UDP packet failed", "remote", remote, "bytes", count, "error", handleErr)
		}
	}
}

func (s *Server) handleUDP(ctx context.Context, socket *net.UDPConn, remote *net.UDPAddr, packet []byte, matchCodec *wirecrypto.MatchCodec) error {
	var matchCodecs []*wirecrypto.MatchCodec
	if matchCodec != nil {
		matchCodecs = []*wirecrypto.MatchCodec{matchCodec}
	}
	return s.handleUDPWithCryptos(ctx, socket, remote, packet, matchCodecs)
}

func (s *Server) handleUDPWithCryptos(ctx context.Context, socket *net.UDPConn, remote *net.UDPAddr, packet []byte, matchCodecs []*wirecrypto.MatchCodec) error {
	if len(packet) == 0 {
		return nil
	}
	if s.isGameSocketSourceOnBotListener(socket, remote) {
		// The game and bot-peer listeners are endpoints of the same server, not
		// peers. Reflecting an unrecognized game-socket datagram from the bot
		// listener creates an unbounded local feedback loop that survives the
		// room that seeded it.
		return nil
	}
	if plain, ok := decodeRelayProbeFrame(packet); ok && binary.LittleEndian.Uint16(plain[0:2]) == relayProbeMessageID {
		observedIP, addressOK := netipFromUDP(remote)
		if !addressOK || !observedIP.Is4() || remote.Port < 1 || remote.Port > 65535 {
			return nil
		}
		reply, err := relayProbeEndpointReply(plain, observedIP, uint16(remote.Port))
		if err != nil {
			return err
		}
		if _, err = socket.WriteToUDP(reply, remote); err != nil {
			return err
		}
		s.logger.Info("relay discovery UDP probe", "remote", remote, "local", socket.LocalAddr(),
			"token", fmt.Sprintf("0x%08x", binary.LittleEndian.Uint32(plain[2:6])))
		return nil
	}
	if protocol.IsLTUnconnected(packet) {
		reader := protocol.NewBitReader(packet)
		if _, err := reader.Bits(32); err != nil {
			return err
		}
		submessage, err := reader.Bits(protocol.LTMessageBits)
		if err != nil {
			return err
		}
		var response []byte
		switch submessage {
		case protocol.LTMessageQuery:
			response = protocol.LTQueryResponse("Community Server", 0, false, 0)
		case protocol.LTMessageConnect:
			response = protocol.LTConnectResponse(true, false)
		case protocol.LTMessagePing:
			ping, readErr := reader.Bits(protocol.LTPingBits)
			if readErr != nil {
				return readErr
			}
			response = protocol.LTPingResponse(uint32(ping))
		default:
			return nil
		}
		_, err = socket.WriteToUDP(response, remote)
		return err
	}

	if len(packet) == 16 {
		for _, codec := range matchCodecs {
			datagram, decodeErr := codec.Unmarshal(packet)
			if decodeErr != nil || !s.isMatchFrame(datagram.Header) {
				continue
			}
			if local, ok := socket.LocalAddr().(*net.UDPAddr); ok && local.Port == int(s.matchPort) &&
				s.shouldForwardHybridMatchPacket(remote, datagram.Header) &&
				s.forwardSharedRelay(socket, remote, packet, "game-socket", s.matchPort) > 0 {
				return nil
			}
			connect := datagram.Header.IsOpening()
			if connect {
				datagram.Header = datagram.Header.AcknowledgeOpening()
			} else {
				if datagram.Header.Kind != wirecrypto.MatchKindPing {
					s.logger.Info("game match UDP non-ping control received",
						"remote", remote,
						"session_id", fmt.Sprintf("0x%04x", datagram.Header.SessionID),
						"sequence", datagram.Header.Sequence,
						"packet_type", fmt.Sprintf("0x%02x", datagram.Header.Kind))
				}
				datagram.Header.Kind = wirecrypto.MatchKindPong
			}
			response := codec.Marshal(datagram)
			_, writeErr := socket.WriteToUDP(response, remote)
			if writeErr == nil && connect {
				s.logMatchUDPActivity(socket, remote, datagram.Header)
			}
			return writeErr
		}
	}

	if packet[0] != 0xcc && packet[0] != 2 {
		if local, ok := socket.LocalAddr().(*net.UDPAddr); ok && local.Port == int(s.matchPort) {
			if s.forwardSharedRelay(socket, remote, packet, "game-socket", s.matchPort) > 0 {
				return nil
			}
		}
		if local, ok := socket.LocalAddr().(*net.UDPAddr); ok && local.Port == int(s.botPeerPortBase) {
			s.logger.Info("unrecognized bot-peer UDP packet", "remote", remote, "bytes", len(packet),
				"packet", fmt.Sprintf("% x", packet))
		}
		_, err := socket.WriteToUDP(packet, remote)
		return err
	}

	if packet[0] == 0xcc && len(packet) == 16 {
		_, err := socket.WriteToUDP(packet, remote)
		return err
	}
	if packet[0] == 2 && len(packet) == 16 {
		if _, err := socket.WriteToUDP(packet, remote); err != nil {
			return err
		}
		userID := int(binary.LittleEndian.Uint32(packet[6:10]))
		session := s.sessionByUserID(userID)
		if session == nil {
			return nil
		}
		address, ok := netipFromUDP(remote)
		if !ok {
			return nil
		}
		localAddress, localPort, hasLocalEndpoint := udpRegistrationEndpoint(packet)
		session.mu.Lock()
		oldAddress, oldPort := session.UDPIP, session.UDPPort
		changed := session.UDPIP != address || session.UDPPort != uint16(remote.Port) ||
			(hasLocalEndpoint && (session.LocalUDPIP != localAddress || session.LocalUDPPort != localPort))
		session.UDPIP = address
		session.UDPPort = uint16(remote.Port)
		if hasLocalEndpoint {
			session.LocalUDPIP = localAddress
			session.LocalUDPPort = localPort
		}
		session.mu.Unlock()
		s.mu.Lock()
		if _, tracked := s.sessions[session]; tracked {
			s.online.replaceUDPEndpoint(session, oldAddress, oldPort)
		}
		s.mu.Unlock()
		// After type-2 UDP registration, the authenticated TCP channel sends
		// 0x130d to complete the match-UDP destination handoff.
		if err := session.Write(ReplyUDPAddress, UDPAddressPayload()); err != nil {
			return fmt.Errorf("send UDP address acknowledgement: %w", err)
		}
		reportedEndpoint := "unavailable"
		if hasLocalEndpoint {
			reportedEndpoint = netip.AddrPortFrom(localAddress, localPort).String()
		}
		s.logger.Info("game UDP client registered", "remote", remote, "reported", reportedEndpoint,
			"user_id", userID, "endpoint_changed", changed, "tcp_ack", "0x130d",
			"registration", fmt.Sprintf("% x", packet))
		// Keep the learned endpoint in authoritative session state. Room entry
		// and relay routing can then use it without rebuilding the roster.
	}
	return nil
}

// isMatchFrame prevents an eight-bit fingerprint collision under one endpoint
// cipher from winning over the cipher that belongs to the actual session.
func (s *Server) isMatchFrame(header wirecrypto.MatchHeader) bool {
	// Multiple advertised endpoints can produce multiple valid ciphers. A
	// fingerprint alone is only an eight-bit check, so require the decrypted
	// session ID to identify a live session before selecting a cipher. Unknown
	// peer traffic remains opaque and follows the relay fallback below.
	return s.sessionBySessionID(header.SessionID) != nil
}

// shouldForwardHybridMatchPacket separates peer traffic sent through the
// hybrid relay from the match server's own connect and ping exchange. Peer
// accepts preserve the connect packet's destination session ID, so an accept
// sent by that destination carries the sender's own ID; its non-connect type is
// what distinguishes it from the sender's initial server bootstrap.
func (s *Server) shouldForwardHybridMatchPacket(remote *net.UDPAddr, header wirecrypto.MatchHeader) bool {
	s.relayMu.Lock()
	hybrid := s.peerEndpointMode == 2
	s.relayMu.Unlock()
	if !hybrid {
		return false
	}

	address, ok := netipFromUDP(remote)
	if !ok || remote.Port < 1 || remote.Port > 65535 {
		return false
	}
	sender := s.sessionByUDPEndpoint(address, uint16(remote.Port), false)
	if sender == nil {
		return false
	}
	room := s.sessionRoom(sender)
	if room == nil {
		return false
	}

	targetSessionID := header.SessionID
	if targetSessionID != sender.SessionID() {
		target := s.sessionBySessionID(targetSessionID)
		// Peer packets are opaque to the relay and may decrypt under the match
		// server's cipher context to a fingerprint-valid header with an unrelated
		// session ID. A server-directed frame must address the authenticated
		// sender; an unknown target from an in-room sender is therefore relay
		// traffic, not match-server control traffic.
		return target == nil || target != sender && s.sessionRoom(target) == room
	}

	// These are the two frames addressed to the game server itself. Other
	// self-ID frames are peer responses returning through the shared relay.
	return !header.IsOpening() && header.Kind != wirecrypto.MatchKindPing
}

func udpRegistrationEndpoint(packet []byte) (netip.Addr, uint16, bool) {
	if len(packet) != 16 || packet[0] != 2 {
		return netip.Addr{}, 0, false
	}
	address := netip.AddrFrom4([4]byte(packet[10:14]))
	port := binary.BigEndian.Uint16(packet[14:16])
	if address.IsUnspecified() || address.IsMulticast() || port == 0 {
		return netip.Addr{}, 0, false
	}
	return address, port, true
}

func (s *Server) logMatchUDPActivity(socket *net.UDPConn, remote *net.UDPAddr, header wirecrypto.MatchHeader) {
	sessionID := header.SessionID
	session := s.sessionBySessionID(sessionID)
	if session == nil {
		s.logger.Info("game match UDP active", "remote", remote, "local", socket.LocalAddr(),
			"session_id", fmt.Sprintf("0x%04x", sessionID), "user_id", 0)
		return
	}
	address, ok := netipFromUDP(remote)
	if !ok || !session.observeMatchUDP(netip.AddrPortFrom(address, uint16(remote.Port))) {
		return
	}
	username, _, userID, _ := session.identity()
	s.logger.Info("game match UDP active", "remote", remote, "local", socket.LocalAddr(),
		"session_id", fmt.Sprintf("0x%04x", sessionID), "user_id", userID, "username", username)
}

func (s *Server) sessionByUserID(userID int) *Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if set := s.online.byUserID[userID]; set != nil {
		return set.newest()
	}
	// Duplicate logins resolve to the most recent connection. Keep that policy
	// explicit instead of depending on Go map iteration order.
	if session := s.online.connections.newestMatching(func(session *Session) bool {
		_, _, candidate, _ := session.identity()
		return candidate == userID
	}); session != nil {
		return session
	}
	return nil
}

func (s *Server) sessionBySessionID(sessionID uint16) *Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if set := s.online.bySessionID[sessionID]; set != nil {
		return set.newest()
	}
	if session := s.online.connections.newestMatching(func(session *Session) bool {
		return session.SessionID() == sessionID
	}); session != nil {
		return session
	}
	return nil
}

func netipFromUDP(address *net.UDPAddr) (result netip.Addr, ok bool) {
	result, ok = netip.AddrFromSlice(address.IP)
	return result.Unmap(), ok
}

func (s *Server) isGameSocketSourceOnBotListener(socket *net.UDPConn, remote *net.UDPAddr) bool {
	if socket == nil || remote == nil || !remote.IP.IsLoopback() {
		return false
	}
	local, ok := socket.LocalAddr().(*net.UDPAddr)
	if !ok {
		return false
	}
	return local.Port == int(s.botPeerPortBase) && remote.Port == int(s.matchPort)
}
