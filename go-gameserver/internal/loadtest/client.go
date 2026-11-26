package loadtest

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"wtserver-go/internal/game"
	"wtserver-go/internal/transport"
)

type frameMatcher func(transport.Frame) bool

type frameResult struct {
	frame transport.Frame
	err   error
}

type frameWaiter struct {
	messageID uint16
	match     frameMatcher
	result    chan frameResult
}

type loadClient struct {
	index      int
	username   string
	connection *countingConn
	wire       *transport.EncryptedConn
	metrics    *measurements

	mu            sync.Mutex
	waiters       map[*frameWaiter]struct{}
	readErr       error
	roomSlot      byte
	closed        atomic.Bool
	readDone      chan struct{}
	endGames      atomic.Uint64
	endGameSignal chan struct{}
	userID        int
	udpConnection *net.UDPConn
	udpReadDone   chan struct{}
	udpCounters   udpCounters
}

type countingConn struct {
	net.Conn
	readBytes    atomic.Uint64
	writtenBytes atomic.Uint64
	readCalls    atomic.Uint64
	writeCalls   atomic.Uint64
}

func (c *countingConn) Read(buffer []byte) (int, error) {
	n, err := c.Conn.Read(buffer)
	c.readCalls.Add(1)
	c.readBytes.Add(uint64(n))
	return n, err
}

func (c *countingConn) Write(buffer []byte) (int, error) {
	n, err := c.Conn.Write(buffer)
	c.writeCalls.Add(1)
	c.writtenBytes.Add(uint64(n))
	return n, err
}

func dialLoadClient(ctx context.Context, address string, index int, metrics *measurements) (*loadClient, error) {
	dialer := net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	counted := &countingConn{Conn: connection}
	client := &loadClient{
		index:         index,
		username:      fmt.Sprintf("load-%04d", index+1),
		connection:    counted,
		wire:          transport.NewEncryptedConn(counted, nil),
		metrics:       metrics,
		waiters:       make(map[*frameWaiter]struct{}),
		roomSlot:      0xff,
		readDone:      make(chan struct{}),
		endGameSignal: make(chan struct{}, 1),
	}
	go client.readFrames()
	return client, nil
}

func (c *loadClient) close() {
	if c.closed.CompareAndSwap(false, true) {
		if c.udpConnection != nil {
			_ = c.udpConnection.Close()
		}
		_ = c.wire.Close()
	}
	if c.udpReadDone != nil {
		<-c.udpReadDone
	}
	<-c.readDone
}

func (c *loadClient) readFrames() {
	defer close(c.readDone)
	for {
		frame, err := c.wire.ReadFrame()
		if err != nil {
			c.failWaiters(err)
			return
		}
		c.metrics.responses.Add(1)
		if frame.MessageID == game.ReplyEndGame {
			c.endGames.Add(1)
			select {
			case c.endGameSignal <- struct{}{}:
			default:
			}
		}
		c.mu.Lock()
		for waiter := range c.waiters {
			if waiter.messageID != frame.MessageID || waiter.match != nil && !waiter.match(frame) {
				continue
			}
			delete(c.waiters, waiter)
			waiter.result <- frameResult{frame: frame}
		}
		c.mu.Unlock()
	}
}

func (c *loadClient) failWaiters(err error) {
	if !c.closed.Load() {
		c.metrics.unexpectedDisconnects.Add(1)
		c.metrics.errors.Add(1)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readErr = err
	for waiter := range c.waiters {
		delete(c.waiters, waiter)
		waiter.result <- frameResult{err: err}
	}
}

func (c *loadClient) waitForEndGame(ctx context.Context, target uint64) error {
	for c.endGames.Load() < target {
		select {
		case <-c.endGameSignal:
		case <-ctx.Done():
			return fmt.Errorf("%s await end game %d: %w", c.username, target, ctx.Err())
		}
	}
	return nil
}

func (c *loadClient) pendingWaiters() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

func (c *loadClient) request(ctx context.Context, name string, requestID, replyID uint16, payload []byte, match frameMatcher) (transport.Frame, error) {
	started := time.Now()
	waiter := &frameWaiter{messageID: replyID, match: match, result: make(chan frameResult, 1)}
	c.mu.Lock()
	if c.readErr != nil {
		err := c.readErr
		c.mu.Unlock()
		c.metrics.failed()
		return transport.Frame{}, fmt.Errorf("%s reader: %w", c.username, err)
	}
	c.waiters[waiter] = struct{}{}
	c.mu.Unlock()

	if _, err := c.wire.WriteMessage(requestID, payload); err != nil {
		c.removeWaiter(waiter)
		c.metrics.failed()
		return transport.Frame{}, fmt.Errorf("%s write %#x: %w", c.username, requestID, err)
	}
	select {
	case result := <-waiter.result:
		if result.err != nil {
			c.metrics.failed()
			return transport.Frame{}, fmt.Errorf("%s await %#x: %w", c.username, replyID, result.err)
		}
		c.metrics.observe(name, time.Since(started))
		return result.frame, nil
	case <-ctx.Done():
		c.removeWaiter(waiter)
		c.metrics.failed()
		return transport.Frame{}, fmt.Errorf("%s await %#x: %w", c.username, replyID, ctx.Err())
	}
}

func (c *loadClient) removeWaiter(waiter *frameWaiter) {
	c.mu.Lock()
	delete(c.waiters, waiter)
	c.mu.Unlock()
}

func (c *loadClient) login(ctx context.Context) error {
	payload := append([]byte{0, 0, 0, byte(len(c.username))}, c.username...)
	payload = append(payload, byte(len(loadPassword)))
	payload = append(payload, loadPassword...)
	frame, err := c.request(ctx, "login", game.RequestChannelLogin, game.ReplyChannelLogin, payload, nil)
	if err != nil {
		return err
	}
	if len(frame.Payload) == 0 || frame.Payload[0] != 0 {
		c.metrics.invalidResponse()
		return fmt.Errorf("%s login status payload %x", c.username, frame.Payload)
	}
	if len(frame.Payload) < 7 {
		c.metrics.invalidResponse()
		return fmt.Errorf("%s login payload is too short for UDP identity: %x", c.username, frame.Payload)
	}
	c.userID = int(binary.LittleEndian.Uint32(frame.Payload[3:7]))
	if c.userID <= 0 {
		c.metrics.invalidResponse()
		return fmt.Errorf("%s login returned invalid UDP identity %d", c.username, c.userID)
	}
	return nil
}

func (c *loadClient) registerUDP(ctx context.Context, address string) error {
	remote, err := net.ResolveUDPAddr("udp4", address)
	if err != nil {
		return fmt.Errorf("%s resolve UDP server: %w", c.username, err)
	}
	connection, err := net.DialUDP("udp4", nil, remote)
	if err != nil {
		return fmt.Errorf("%s open UDP socket: %w", c.username, err)
	}
	local, ok := connection.LocalAddr().(*net.UDPAddr)
	if !ok || local.Port < 1 || local.Port > 65535 {
		_ = connection.Close()
		return fmt.Errorf("%s invalid local UDP endpoint %v", c.username, connection.LocalAddr())
	}
	packet := udpRegistrationPacket(c.userID, local)
	deadline := time.Now().Add(5 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	buffer := make([]byte, len(packet))
	for {
		attemptDeadline := time.Now().Add(250 * time.Millisecond)
		if deadline.Before(attemptDeadline) {
			attemptDeadline = deadline
		}
		if err := connection.SetDeadline(attemptDeadline); err != nil {
			_ = connection.Close()
			return fmt.Errorf("%s set UDP registration deadline: %w", c.username, err)
		}
		count, writeErr := connection.Write(packet)
		if count > 0 {
			c.udpCounters.registrationPacketsSent.Add(1)
		}
		if writeErr != nil {
			_ = connection.Close()
			return fmt.Errorf("%s send UDP registration: %w", c.username, writeErr)
		}
		count, readErr := connection.Read(buffer)
		if readErr == nil && bytes.Equal(buffer[:count], packet) {
			c.udpCounters.registrationPacketsReceived.Add(1)
			break
		}
		var networkErr net.Error
		if readErr != nil && (!errors.As(readErr, &networkErr) || !networkErr.Timeout()) {
			_ = connection.Close()
			return fmt.Errorf("%s receive UDP registration: %w", c.username, readErr)
		}
		if time.Now().After(deadline) {
			_ = connection.Close()
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("%s register UDP: %w", c.username, err)
			}
			return fmt.Errorf("%s UDP registration was not acknowledged within 5s", c.username)
		}
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		_ = connection.Close()
		return fmt.Errorf("%s clear UDP registration deadline: %w", c.username, err)
	}
	c.udpConnection = connection
	c.udpReadDone = make(chan struct{})
	go c.readUDP()
	return nil
}

func udpRegistrationPacket(userID int, local *net.UDPAddr) []byte {
	packet := make([]byte, 16)
	packet[0], packet[1], packet[2], packet[4], packet[5] = 2, 0x19, 0x0c, 0x61, 8
	binary.LittleEndian.PutUint32(packet[6:10], uint32(userID))
	ip := local.IP.To4()
	if ip == nil {
		ip = net.IPv4(127, 0, 0, 1).To4()
	}
	copy(packet[10:14], ip)
	binary.BigEndian.PutUint16(packet[14:16], uint16(local.Port))
	return packet
}

func (c *loadClient) readUDP() {
	defer close(c.udpReadDone)
	buffer := make([]byte, maxGameplayPacket)
	for {
		count, err := c.udpConnection.Read(buffer)
		if err != nil {
			if !c.closed.Load() && !errors.Is(err, net.ErrClosed) {
				c.udpCounters.readErrors.Add(1)
			}
			return
		}
		if count == 0 {
			continue
		}
		switch buffer[0] {
		case movementPacketMarker:
			c.udpCounters.movementPacketsReceived.Add(1)
			c.udpCounters.movementBytesReceived.Add(uint64(count))
		case shootingPacketMarker:
			c.udpCounters.shootingPacketsReceived.Add(1)
			c.udpCounters.shootingBytesReceived.Add(uint64(count))
		case 2:
			c.udpCounters.registrationPacketsReceived.Add(1)
		}
	}
}

func (c *loadClient) sendGameplayPacket(packet []byte, marker byte, recipients int) error {
	if c.udpConnection == nil {
		c.udpCounters.sendErrors.Add(1)
		return fmt.Errorf("%s has no registered UDP socket", c.username)
	}
	count, err := c.udpConnection.Write(packet)
	if err != nil || count != len(packet) {
		c.udpCounters.sendErrors.Add(1)
		if err == nil {
			err = fmt.Errorf("short UDP write: wrote %d of %d bytes", count, len(packet))
		}
		return fmt.Errorf("%s send gameplay UDP: %w", c.username, err)
	}
	switch marker {
	case movementPacketMarker:
		c.udpCounters.movementPacketsSent.Add(1)
		c.udpCounters.movementBytesSent.Add(uint64(count))
	case shootingPacketMarker:
		c.udpCounters.shootingPacketsSent.Add(1)
		c.udpCounters.shootingBytesSent.Add(uint64(count))
	}
	if recipients > 0 {
		c.udpCounters.expectedRelayPackets.Add(uint64(recipients))
		c.udpCounters.expectedRelayBytes.Add(uint64(count * recipients))
	}
	return nil
}

func (c *loadClient) shopAndEquip(ctx context.Context) error {
	requests := []struct {
		name    string
		request uint16
		reply   uint16
		payload []byte
	}{
		{name: "user_info", request: game.RequestUserInfo, reply: game.ReplyUserInfo},
		{name: "currency", request: game.RequestGetGoldCash, reply: game.ReplyGetGoldCash},
		{name: "item_list", request: game.RequestItemList, reply: game.ReplyItemList},
		{name: "equipment_list", request: game.RequestEquipList, reply: game.ReplyEquipList},
		{name: "buy_item", request: game.RequestBuyItem, reply: game.ReplyBuyItem, payload: buyPayload()},
		{name: "save_equipment", request: game.RequestEquip, reply: game.ReplyEquip, payload: equipmentPayload()},
	}
	for _, request := range requests {
		frame, err := c.request(ctx, request.name, request.request, request.reply, request.payload, nil)
		if err != nil {
			return err
		}
		if (request.name == "buy_item" || request.name == "save_equipment") && (len(frame.Payload) == 0 || frame.Payload[0] != 0) {
			c.metrics.invalidResponse()
			return fmt.Errorf("%s %s returned status payload %x", c.username, request.name, frame.Payload)
		}
	}
	return nil
}

func (c *loadClient) allocateRoom(ctx context.Context, roomSize int) (int16, error) {
	frame, err := c.request(ctx, "create_room", game.RequestCreateRoom, game.ReplyCreateRoom, roomPayload(c.index, roomSize), nil)
	if err != nil {
		return -1, err
	}
	if len(frame.Payload) < 7 || frame.Payload[0] != 0 {
		c.metrics.invalidResponse()
		return -1, fmt.Errorf("%s invalid create-room payload %x", c.username, frame.Payload)
	}
	c.roomSlot = 0
	return int16(binary.LittleEndian.Uint16(frame.Payload[5:7])), nil
}

func (c *loadClient) enterRoom(ctx context.Context, roomNumber int16, slot byte) error {
	payload := make([]byte, 2)
	binary.LittleEndian.PutUint16(payload, uint16(roomNumber))
	frame, err := c.request(ctx, "enter_room", game.RequestEnterRoom, game.ReplyEnterRoom, payload, func(frame transport.Frame) bool {
		return len(frame.Payload) > 0
	})
	if err != nil {
		return err
	}
	if len(frame.Payload) == 0 || frame.Payload[0] != 0 {
		c.metrics.invalidResponse()
		return fmt.Errorf("%s enter-room status payload %x", c.username, frame.Payload)
	}
	c.roomSlot = slot
	return nil
}

func (c *loadClient) ready(ctx context.Context) error {
	slot := c.roomSlot
	_, err := c.request(ctx, "ready", game.RequestUDPReady, game.ReplyUDPReady, []byte{1}, func(frame transport.Frame) bool {
		return len(frame.Payload) > 2 && frame.Payload[1] == slot && frame.Payload[2] == 1
	})
	return err
}

func (c *loadClient) beginMatch(ctx context.Context) error {
	frame, err := c.request(ctx, "start_game", game.RequestStartGame, game.ReplyStartGame, nil, func(frame transport.Frame) bool {
		return len(frame.Payload) > 0
	})
	if err != nil {
		return err
	}
	if len(frame.Payload) == 0 || frame.Payload[0] != 0 {
		c.metrics.invalidResponse()
		return fmt.Errorf("%s start-game status payload %x", c.username, frame.Payload)
	}
	return nil
}

func (c *loadClient) enterGame(ctx context.Context) error {
	slot := c.roomSlot
	_, err := c.request(ctx, "enter_game", game.RequestEnterGame, game.ReplyEnterGame, nil, func(frame transport.Frame) bool {
		return len(frame.Payload) > 1 && frame.Payload[1] == slot
	})
	return err
}

func (c *loadClient) beginRound(ctx context.Context) error {
	_, err := c.request(ctx, "start_round", game.RequestStartRound, game.ReplyStartRound, nil, nil)
	return err
}

func (c *loadClient) recordKill(ctx context.Context, killer, victim byte) error {
	payload := make([]byte, 8)
	payload[0], payload[1] = killer, victim
	_, err := c.request(ctx, "player_dead", game.RequestPlayerDead, game.ReplyPlayerDead, payload, func(frame transport.Frame) bool {
		return len(frame.Payload) > 6 && frame.Payload[5] == killer && frame.Payload[6] == victim
	})
	return err
}

func (c *loadClient) gamePoints(ctx context.Context) error {
	_, err := c.request(ctx, "game_points", game.RequestGamePoints, game.ReplyGamePoints, nil, nil)
	return err
}

func (c *loadClient) exitRoom(ctx context.Context) error {
	slot := c.roomSlot
	_, err := c.request(ctx, "exit_room", game.RequestExitRoom, game.ReplyExitRoom, nil, func(frame transport.Frame) bool {
		return len(frame.Payload) > 1 && frame.Payload[1] == slot
	})
	if err == nil {
		c.roomSlot = 0xff
	}
	return err
}

func buyPayload() []byte {
	payload := make([]byte, 10)
	binary.LittleEndian.PutUint16(payload[0:2], 1001)
	payload[2] = 30
	payload[5] = 4
	binary.LittleEndian.PutUint32(payload[6:10], 100)
	return payload
}

func equipmentPayload() []byte {
	payload := make([]byte, 90)
	payload[0] = 1
	payload[1] = 0
	values := map[int]uint32{
		2:  2_000_002_001,
		7:  2_000_001_001,
		12: 2_000_001_001,
		17: 2_000_001_001,
		21: 2_000_001_001,
		85: 2_000_001_001,
	}
	for offset, value := range values {
		binary.LittleEndian.PutUint32(payload[offset:offset+4], value)
	}
	return payload
}

func roomPayload(index, roomSize int) []byte {
	title := fmt.Sprintf("Load %03d", index+1)
	titleBytes := make([]byte, 0, len(title)*2)
	for _, value := range []byte(title) {
		titleBytes = append(titleBytes, value, 0)
	}
	payload := []byte{byte(len(titleBytes))}
	payload = append(payload, titleBytes...)
	payload = append(payload, 0) // empty password
	fixed := make([]byte, 16)
	fixed[0] = 3
	binary.LittleEndian.PutUint32(fixed[1:5], 2)
	fixed[5] = 5
	fixed[6] = 0
	binary.LittleEndian.PutUint16(fixed[7:9], 300)
	// A one-point deathmatch completes from the scripted kill, allowing the
	// harness to verify end-game packets and persisted match results.
	binary.LittleEndian.PutUint16(fixed[9:11], 1)
	fixed[11] = 2
	fixed[12] = 0
	binary.LittleEndian.PutUint16(fixed[13:15], 0x00c0)
	fixed[15] = byte(roomSize)
	payload = append(payload, fixed...)
	// After Capacity the wire layout contains MapSetting, RuleSetting, two
	// ladder words, two mode bytes, class minimum/maximum, then Points
	// repeated. The full shape keeps encrypted zero padding distinct from an
	// explicit 0..0 class limit.
	trailing := make([]byte, 12)
	trailing[9] = 0x3d
	binary.LittleEndian.PutUint16(trailing[10:12], 1)
	return append(payload, trailing...)
}
