package game

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	wirecrypto "wtserver-go/internal/crypto"
	"wtserver-go/internal/protocol"
	"wtserver-go/internal/transport"
)

func openingDatagram(codec *wirecrypto.MatchCodec, sessionID uint16, nonce byte) []byte {
	return openingDatagramWithFlags(codec, sessionID, 1, nonce)
}

func openingDatagramWithFlags(codec *wirecrypto.MatchCodec, sessionID uint16, flags, nonce byte) []byte {
	return codec.Marshal(wirecrypto.MatchDatagram{
		Header: wirecrypto.MatchHeader{
			Flags:     flags,
			SessionID: sessionID,
			Kind:      wirecrypto.MatchKindOpening,
			Tail:      nonce,
		},
	})
}

func acknowledgedDatagram(t *testing.T, codec *wirecrypto.MatchCodec, wire []byte) []byte {
	t.Helper()
	datagram, err := codec.Unmarshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	datagram.Header = datagram.Header.AcknowledgeOpening()
	return codec.Marshal(datagram)
}

func TestUDPUnconnectedQuery(t *testing.T) {
	serverSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer serverSocket.Close()
	clientSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer clientSocket.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer("unused", 40707, 40708, netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.1"), &fakeUsers{}, logger)

	var query protocol.BitWriter
	query.U32(protocol.LTToken)
	query.Bits(protocol.LTMessageQuery, protocol.LTMessageBits)
	remote := clientSocket.LocalAddr().(*net.UDPAddr)
	if err := server.handleUDP(context.Background(), serverSocket, remote, query.Clone(), nil); err != nil {
		t.Fatal(err)
	}
	_ = clientSocket.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, 128)
	count, _, err := clientSocket.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	reader := protocol.NewBitReader(buffer[:count])
	_, _ = reader.Bits(32)
	message, err := reader.Bits(protocol.LTMessageBits)
	if err != nil || message != protocol.LTMessageQueryResponse {
		t.Fatalf("response submessage = %d, err = %v, packet = %x", message, err, buffer[:count])
	}
}

func TestBotUDPSocketDoesNotReflectGameSocketPackets(t *testing.T) {
	mainSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer mainSocket.Close()
	botSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer botSocket.Close()

	mainAddress := mainSocket.LocalAddr().(*net.UDPAddr)
	botAddress := botSocket.LocalAddr().(*net.UDPAddr)
	loopback := netip.MustParseAddr("127.0.0.1")
	server := NewServer("unused", uint16(mainAddress.Port), uint16(botAddress.Port), loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cases := []struct {
		name   string
		packet []byte
	}{
		{name: "vector one", packet: bytes.Repeat([]byte{0xa5}, 16)},
		{name: "vector two", packet: bytes.Repeat([]byte{0x5a}, 16)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := server.handleUDP(context.Background(), botSocket, mainAddress, testCase.packet, nil); err != nil {
				t.Fatal(err)
			}
			assertUDPTimeout(t, mainSocket)
		})
	}
}

func TestUDPRelayDiscoveryReturnsObservedEndpoint(t *testing.T) {
	serverSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer serverSocket.Close()
	clientSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer clientSocket.Close()
	var logs bytes.Buffer
	server := NewServer("unused", 40707, 40708, netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.1"), &fakeUsers{}, slog.New(slog.NewTextHandler(&logs, nil)))
	plain := make([]byte, relayProbePlainSize)
	binary.LittleEndian.PutUint16(plain[0:2], relayProbeMessageID)
	binary.LittleEndian.PutUint32(plain[2:6], 0x12345678)
	frame, err := encodeRelayProbeFrame(plain, 0x2468)
	if err != nil {
		t.Fatal(err)
	}
	remote := clientSocket.LocalAddr().(*net.UDPAddr)
	if err := server.handleUDP(context.Background(), serverSocket, remote, frame, nil); err != nil {
		t.Fatal(err)
	}
	_ = clientSocket.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, relayProbeFrameSize)
	count, _, err := clientSocket.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	reply, ok := decodeRelayProbeFrame(buffer[:count])
	if !ok || binary.LittleEndian.Uint32(reply[2:6]) != 0x12345678 ||
		!bytes.Equal(reply[6:10], []byte{127, 0, 0, 1}) ||
		binary.LittleEndian.Uint16(reply[10:12]) != uint16(remote.Port) {
		t.Fatalf("relay discovery reply = %x, decoded=%v", reply, ok)
	}
	if !strings.Contains(logs.String(), "relay discovery UDP probe") {
		t.Fatalf("relay discovery log = %q", logs.String())
	}
}

func TestMatchUDPHandshakeLogsAuthenticatedClientOnce(t *testing.T) {
	serverSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer serverSocket.Close()
	clientSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer clientSocket.Close()

	loopback := netip.MustParseAddr("127.0.0.1")
	matchCodec, err := wirecrypto.NewMatchCodec(loopback, 40707)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	server := NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, logger)
	session := &Session{RoomNumber: -1, RoomSlot: -1}
	session.sessionID = 0x0801
	session.setAuthenticated("player", 42, "Player", 7)
	server.addSession(session)
	packet := openingDatagram(matchCodec, session.SessionID(), 0xaa)
	remote := clientSocket.LocalAddr().(*net.UDPAddr)
	for range 2 {
		if err := server.handleUDP(context.Background(), serverSocket, remote, packet, matchCodec); err != nil {
			t.Fatal(err)
		}
		_ = clientSocket.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, err := clientSocket.ReadFromUDP(make([]byte, 32)); err != nil {
			t.Fatal(err)
		}
	}
	output := logs.String()
	if strings.Count(output, "game match UDP active") != 1 || !strings.Contains(output, "user_id=42") || !strings.Contains(output, "username=player") {
		t.Fatalf("match UDP logs = %q", output)
	}
}

func TestMatchUDPSelectsCipherForAdvertisedEndpoint(t *testing.T) {
	serverSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer serverSocket.Close()
	clientSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer clientSocket.Close()

	loopback := netip.MustParseAddr("127.0.0.1")
	public := netip.MustParseAddr("203.0.113.152")
	serverPort := uint16(serverSocket.LocalAddr().(*net.UDPAddr).Port)
	localCodec, err := wirecrypto.NewMatchCodec(loopback, serverPort)
	if err != nil {
		t.Fatal(err)
	}
	publicCodec, err := wirecrypto.NewMatchCodec(public, serverPort)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer("unused", serverPort, serverPort+1, loopback, public, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	session := &Session{RoomNumber: -1, RoomSlot: -1}
	session.sessionID = 0x0801
	session.setAuthenticated("remote", 42, "Remote", 7)
	server.addSession(session)

	connect := openingDatagram(publicCodec, session.SessionID(), 0xaa)
	want := acknowledgedDatagram(t, publicCodec, connect)
	remote := clientSocket.LocalAddr().(*net.UDPAddr)
	if err := server.handleUDPWithCryptos(
		context.Background(), serverSocket, remote, connect,
		[]*wirecrypto.MatchCodec{localCodec, publicCodec},
	); err != nil {
		t.Fatal(err)
	}

	_ = clientSocket.SetReadDeadline(time.Now().Add(2 * time.Second))
	got := make([]byte, len(want))
	count, _, err := clientSocket.ReadFromUDP(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[:count], want) {
		t.Fatalf("match accept = %x, want public-endpoint cipher %x", got[:count], want)
	}
}

func TestSessionByUserIDPrefersMostRecentConnection(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer("unused", 40707, 40708, netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.1"), &fakeUsers{}, logger)

	oldest := &Session{RoomNumber: -1, RoomSlot: -1}
	oldest.setAuthenticated("first", 42, "Player", 7)
	newest := &Session{RoomNumber: -1, RoomSlot: -1}
	newest.setAuthenticated("second", 42, "Player", 7)
	server.addSession(oldest)
	server.addSession(newest)

	if got := server.sessionByUserID(42); got != newest {
		t.Fatalf("duplicate user lookup = %p, want most recent session %p", got, newest)
	}

	server.removeSession(newest)
	if got := server.sessionByUserID(42); got != oldest {
		t.Fatalf("lookup after latest disconnect = %p, want remaining session %p", got, oldest)
	}
}

func TestUDPRegistrationEndpoint(t *testing.T) {
	packet := []byte{2, 0x19, 0x0c, 0, 0x61, 8, 1, 0, 0, 0, 127, 0, 0, 1, 0x70, 0x1f}
	address, port, ok := udpRegistrationEndpoint(packet)
	if !ok || address.String() != "127.0.0.1" || port != 28703 {
		t.Fatalf("registration endpoint = %s:%d, ok=%v", address, port, ok)
	}

	packet[14], packet[15] = 0, 0
	if _, _, ok := udpRegistrationEndpoint(packet); ok {
		t.Fatal("zero-port registration endpoint accepted")
	}
}

func TestUDPRegistrationInRoomDoesNotRebroadcastRoster(t *testing.T) {
	loopback := netip.MustParseAddr("127.0.0.1")
	serverSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer serverSocket.Close()
	udpClient, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer udpClient.Close()

	firstServer, firstClient := net.Pipe()
	defer firstServer.Close()
	defer firstClient.Close()
	secondServer, secondClient := net.Pipe()
	defer secondServer.Close()
	defer secondClient.Close()
	first, err := newSession(transport.NewEncryptedConn(firstServer, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newSession(transport.NewEncryptedConn(secondServer, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	first.setAuthenticated("first", 41, "First", 1)
	second.setAuthenticated("second", 42, "Second", 1)

	server := NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.addSession(first)
	server.addSession(second)
	room := newRoom(1, RoomConfig{Capacity: 8})
	if _, ok := room.Add(first); !ok {
		t.Fatal("add first room member")
	}
	if _, ok := room.Add(second); !ok {
		t.Fatal("add second room member")
	}
	server.rooms[room.Number] = room

	packet := []byte{2, 0x19, 0x0c, 0, 0x61, 8, 42, 0, 0, 0, 10, 0, 0, 42, 0x70, 0x1f}
	handled := make(chan error, 1)
	go func() {
		handled <- server.handleUDP(context.Background(), serverSocket, udpClient.LocalAddr().(*net.UDPAddr), packet, nil)
	}()

	_ = udpClient.SetReadDeadline(time.Now().Add(2 * time.Second))
	echo := make([]byte, len(packet))
	count, _, err := udpClient.ReadFromUDP(echo)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(echo[:count], packet) {
		t.Fatalf("registration echo = %x, want %x", echo[:count], packet)
	}
	firstWire := transport.NewEncryptedConn(firstClient, nil)
	secondWire := transport.NewEncryptedConn(secondClient, nil)
	ack := assertFrameID(t, secondWire, ReplyUDPAddress)
	if len(ack.Payload) == 0 || ack.Payload[0] != 0 {
		t.Fatalf("UDP address acknowledgement = %x", ack.Payload)
	}
	checks := []struct {
		name       string
		connection net.Conn
		wire       *transport.EncryptedConn
	}{
		{name: "room peer", connection: firstClient, wire: firstWire},
		{name: "registered client", connection: secondClient, wire: secondWire},
	}
	for _, check := range checks {
		name, connection, wire := check.name, check.connection, check.wire
		if err := connection.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		frame, readErr := wire.ReadFrame()
		if readErr == nil {
			t.Fatalf("%s received unsolicited message %#x after UDP registration", name, frame.MessageID)
		}
		var networkErr net.Error
		if !errors.As(readErr, &networkErr) || !networkErr.Timeout() {
			t.Fatalf("%s post-registration read error = %v, want timeout", name, readErr)
		}
	}
	select {
	case err := <-handled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("UDP registration handler did not finish")
	}

	second.mu.RLock()
	observedIP, observedPort := second.UDPIP, second.UDPPort
	localIP, localPort := second.LocalUDPIP, second.LocalUDPPort
	second.mu.RUnlock()
	remote := udpClient.LocalAddr().(*net.UDPAddr)
	if observedIP.String() != "127.0.0.1" || observedPort != uint16(remote.Port) ||
		localIP.String() != "10.0.0.42" || localPort != 28703 {
		t.Fatalf("registered endpoints = observed %s:%d local %s:%d", observedIP, observedPort, localIP, localPort)
	}
}

func TestGameUDPSocketRelaysUnknownPeerPacketWithinRoom(t *testing.T) {
	serverSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer serverSocket.Close()
	senderSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer senderSocket.Close()
	recipientSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer recipientSocket.Close()

	serverPort := uint16(serverSocket.LocalAddr().(*net.UDPAddr).Port)
	server := NewServer("unused", serverPort, serverPort+1, netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.1"), &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	senderPort := uint16(senderSocket.LocalAddr().(*net.UDPAddr).Port)
	recipientPort := uint16(recipientSocket.LocalAddr().(*net.UDPAddr).Port)
	sender := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: netip.MustParseAddr("127.0.0.1"), UDPPort: senderPort}
	recipient := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: netip.MustParseAddr("127.0.0.1"), UDPPort: recipientPort}
	room := newRoom(1, RoomConfig{Capacity: 8})
	if _, ok := room.Add(sender); !ok {
		t.Fatal("add sender")
	}
	if _, ok := room.Add(recipient); !ok {
		t.Fatal("add recipient")
	}
	server.rooms[1] = room
	server.sessions[sender], server.sessions[recipient] = struct{}{}, struct{}{}
	trackTestConnections(server, sender, recipient)

	packet := []byte{0x38, 0x9b, 0x8b, 0x5f, 0x13, 0x10, 0x31, 0x2e}
	if err := server.handleUDP(context.Background(), serverSocket, senderSocket.LocalAddr().(*net.UDPAddr), packet, nil); err != nil {
		t.Fatal(err)
	}
	_ = recipientSocket.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, 32)
	count, remote, err := recipientSocket.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buffer[:count], packet) || remote.Port != int(serverPort) {
		t.Fatalf("relayed packet = %x from %s, want %x from port %d", buffer[:count], remote, packet, serverPort)
	}
}

func TestGameUDPSocketRelaysEncryptedPeerHandshakeWithinRoom(t *testing.T) {
	serverSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer serverSocket.Close()
	senderSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer senderSocket.Close()
	recipientSocket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer recipientSocket.Close()

	loopback := netip.MustParseAddr("127.0.0.1")
	serverPort := uint16(serverSocket.LocalAddr().(*net.UDPAddr).Port)
	matchCodec, err := wirecrypto.NewMatchCodec(loopback, serverPort)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer("unused", serverPort, serverPort+1, loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureRelay(loopback, serverPort+2, serverPort+3, 2)

	sender := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: loopback, UDPPort: uint16(senderSocket.LocalAddr().(*net.UDPAddr).Port)}
	sender.sessionID = 0x0801
	recipient := &Session{RoomNumber: -1, RoomSlot: -1, UDPIP: loopback, UDPPort: uint16(recipientSocket.LocalAddr().(*net.UDPAddr).Port)}
	recipient.sessionID = 0x0802
	server.addSession(sender)
	server.addSession(recipient)
	room := newRoom(1, RoomConfig{Capacity: 8})
	if _, ok := room.Add(sender); !ok {
		t.Fatal("add sender")
	}
	if _, ok := room.Add(recipient); !ok {
		t.Fatal("add recipient")
	}
	server.rooms[room.Number] = room

	var connect []byte
findMarker:
	for flags := 0; flags <= 0xff; flags++ {
		for nonce := 0; nonce <= 0xff; nonce++ {
			candidate := openingDatagramWithFlags(matchCodec, recipient.SessionID(), byte(flags), byte(nonce))
			if candidate[0] == 2 || candidate[0] == 0xcc {
				connect = candidate
				break findMarker
			}
		}
	}
	if connect == nil {
		t.Fatal("could not construct an encrypted connect with a legacy marker prefix")
	}
	if err := server.handleUDP(context.Background(), serverSocket, senderSocket.LocalAddr().(*net.UDPAddr), connect, matchCodec); err != nil {
		t.Fatal(err)
	}
	assertServerUDP := func(socket *net.UDPConn, want []byte) {
		t.Helper()
		if err := socket.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, 32)
		count, remote, err := socket.ReadFromUDP(buffer)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(buffer[:count], want) || remote.Port != int(serverPort) {
			t.Fatalf("relayed packet = %x from %s, want %x from port %d", buffer[:count], remote, want, serverPort)
		}
	}
	assertServerUDP(recipientSocket, connect)
	assertUDPTimeout(t, senderSocket)

	accept := acknowledgedDatagram(t, matchCodec, connect)
	if err := server.handleUDP(context.Background(), serverSocket, recipientSocket.LocalAddr().(*net.UDPAddr), accept, matchCodec); err != nil {
		t.Fatal(err)
	}
	assertServerUDP(senderSocket, accept)
	assertUDPTimeout(t, recipientSocket)

	bootstrap := openingDatagram(matchCodec, sender.SessionID(), 0xbb)
	wantBootstrapAccept := acknowledgedDatagram(t, matchCodec, bootstrap)
	if err := server.handleUDP(context.Background(), serverSocket, senderSocket.LocalAddr().(*net.UDPAddr), bootstrap, matchCodec); err != nil {
		t.Fatal(err)
	}
	assertServerUDP(senderSocket, wantBootstrapAccept)
	assertUDPTimeout(t, recipientSocket)

	// A relay header can pass the one-byte fingerprint without identifying an
	// active server session. It must remain opaque relay traffic.
	collisionHeader, err := wirecrypto.ParseMatchHeader([]byte{0x33, 0x52, 0x1a, 0xff, 0xc3, 0x50, 0x74, 0x2f})
	if err != nil {
		t.Fatal(err)
	}
	collision := matchCodec.Marshal(wirecrypto.MatchDatagram{Header: collisionHeader})
	if collision[0] == 2 || collision[0] == 0xcc {
		t.Fatalf("collision prefix = %#x, want normal ciphertext path", collision[0])
	}
	if err := server.handleUDP(context.Background(), serverSocket, senderSocket.LocalAddr().(*net.UDPAddr), collision, matchCodec); err != nil {
		t.Fatal(err)
	}
	assertServerUDP(recipientSocket, collision)
	assertUDPTimeout(t, senderSocket)
}

func assertUDPTimeout(t *testing.T, socket *net.UDPConn) {
	t.Helper()
	if err := socket.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := socket.ReadFromUDP(make([]byte, 32)); err == nil {
		t.Fatal("received unexpected UDP response")
	} else if networkErr, ok := err.(net.Error); !ok || !networkErr.Timeout() {
		t.Fatalf("UDP read error = %v, want timeout", err)
	}
}
