package game

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"wtserver-go/internal/transport"
)

const lobbyUpdateWindow = 10 * time.Millisecond

type LobbyUpdateStats struct {
	Flushes                uint64
	Events                 uint64
	RecipientBatches       uint64
	DeliveredFrames        uint64
	AudienceFilteredFrames uint64
	FailedBatches          uint64
	FailedFrames           uint64
	MaxEventsPerFlush      uint64
	MaxFramesPerBatch      uint64
}

type lobbyUpdateMetrics struct {
	flushes                atomic.Uint64
	events                 atomic.Uint64
	recipientBatches       atomic.Uint64
	deliveredFrames        atomic.Uint64
	audienceFilteredFrames atomic.Uint64
	failedBatches          atomic.Uint64
	failedFrames           atomic.Uint64
	maxEventsPerFlush      atomic.Uint64
	maxFramesPerBatch      atomic.Uint64
}

type lobbyUpdateKind byte

const (
	lobbyUpdateRoster lobbyUpdateKind = iota
	lobbyUpdateJoinAllow
	lobbyUpdateFieldCreate
	lobbyUpdateFieldUsers
	lobbyUpdateFieldState
	lobbyUpdateGameState
	lobbyUpdateFieldExit
)

type pendingLobbyFlag struct {
	dirty bool
	order uint64
}

type pendingLobbyRoom struct {
	created       *Room
	createOrder   uint64
	users         *Room
	usersOrder    uint64
	fieldState    *Room
	fieldOrder    uint64
	fieldAudience uint64
	gameStates    []pendingLobbyGameState
	exited        *Room
	exitOrder     uint64
}

type pendingLobbyGameState struct {
	room     *Room
	started  bool
	order    uint64
	audience uint64
}

func (pending pendingLobbyRoom) empty() bool {
	return pending.created == nil && pending.users == nil && pending.fieldState == nil &&
		len(pending.gameStates) == 0 && pending.exited == nil
}

type lobbyUpdateState struct {
	mu        sync.Mutex
	timer     *time.Timer
	flushing  bool
	delay     time.Duration
	nextOrder uint64
	roster    pendingLobbyFlag
	joinAllow pendingLobbyFlag
	rooms     map[int16]pendingLobbyRoom
}

type lobbyUpdateEvent struct {
	kind            lobbyUpdateKind
	order           uint64
	room            *Room
	started         bool
	audienceVersion uint64
}

func newLobbyUpdateState() lobbyUpdateState {
	return lobbyUpdateState{
		delay: lobbyUpdateWindow,
		rooms: make(map[int16]pendingLobbyRoom),
	}
}

func (s *Server) LobbyUpdateStats() LobbyUpdateStats {
	metrics := s.lobbyUpdateMetrics
	if metrics == nil {
		return LobbyUpdateStats{}
	}
	return LobbyUpdateStats{
		Flushes:                metrics.flushes.Load(),
		Events:                 metrics.events.Load(),
		RecipientBatches:       metrics.recipientBatches.Load(),
		DeliveredFrames:        metrics.deliveredFrames.Load(),
		AudienceFilteredFrames: metrics.audienceFilteredFrames.Load(),
		FailedBatches:          metrics.failedBatches.Load(),
		FailedFrames:           metrics.failedFrames.Load(),
		MaxEventsPerFlush:      metrics.maxEventsPerFlush.Load(),
		MaxFramesPerBatch:      metrics.maxFramesPerBatch.Load(),
	}
}

// EnableLobbyUpdateStats turns on diagnostic counters. Production servers do
// not enable them, keeping the lobby hot path free of counter atomics.
func (s *Server) EnableLobbyUpdateStats() {
	s.lobbyUpdateMetrics = &lobbyUpdateMetrics{}
}

func (s *Server) scheduleChannelRoster() {
	updates := &s.lobbyUpdates
	updates.mu.Lock()
	updates.roster = pendingLobbyFlag{dirty: true, order: updates.nextOrderLocked()}
	s.armLobbyUpdateTimerLocked()
	updates.mu.Unlock()
}

func (s *Server) scheduleJoinAllow() {
	updates := &s.lobbyUpdates
	updates.mu.Lock()
	updates.joinAllow = pendingLobbyFlag{dirty: true, order: updates.nextOrderLocked()}
	s.armLobbyUpdateTimerLocked()
	updates.mu.Unlock()
}

func (s *Server) scheduleFieldCreate(room *Room) {
	if room == nil {
		return
	}
	updates := &s.lobbyUpdates
	updates.mu.Lock()
	pending := updates.rooms[room.Number]
	if pending.created != room {
		pending.created = room
		pending.createOrder = updates.nextOrderLocked()
	}
	// The create payload is built at flush time and already contains the latest
	// user count, so a separate pending user-count packet would be redundant.
	if pending.users == room {
		pending.users = nil
		pending.usersOrder = 0
	}
	if pending.fieldState == room {
		pending.fieldState = nil
		pending.fieldOrder = 0
		pending.fieldAudience = 0
	}
	updates.rooms[room.Number] = pending
	s.armLobbyUpdateTimerLocked()
	updates.mu.Unlock()
}

func (s *Server) scheduleFieldUsers(room *Room) {
	if room == nil {
		return
	}
	updates := &s.lobbyUpdates
	updates.mu.Lock()
	pending := updates.rooms[room.Number]
	if pending.created != room && pending.exited != room {
		pending.users = room
		pending.usersOrder = updates.nextOrderLocked()
		updates.rooms[room.Number] = pending
	}
	s.armLobbyUpdateTimerLocked()
	updates.mu.Unlock()
}

func (s *Server) scheduleFieldState(room *Room) {
	if room == nil {
		return
	}
	updates := &s.lobbyUpdates
	updates.mu.Lock()
	pending := updates.rooms[room.Number]
	// A queued create is serialized from the latest room snapshot and already
	// contains the same configuration, so a separate state frame is redundant.
	if pending.created != room && pending.exited != room {
		pending.fieldState = room
		pending.fieldOrder = updates.nextOrderLocked()
		pending.fieldAudience = roomMembershipVersion.Load()
		updates.rooms[room.Number] = pending
	}
	s.armLobbyUpdateTimerLocked()
	updates.mu.Unlock()
}

func (s *Server) scheduleGameState(room *Room, started bool) {
	if room == nil {
		return
	}
	updates := &s.lobbyUpdates
	updates.mu.Lock()
	pending := updates.rooms[room.Number]
	if pending.exited != room {
		pending.gameStates = append(pending.gameStates, pendingLobbyGameState{
			room:     room,
			started:  started,
			order:    updates.nextOrderLocked(),
			audience: roomMembershipVersion.Load(),
		})
		updates.rooms[room.Number] = pending
	}
	s.armLobbyUpdateTimerLocked()
	updates.mu.Unlock()
}

func (s *Server) scheduleFieldExit(room *Room) {
	if room == nil {
		return
	}
	updates := &s.lobbyUpdates
	updates.mu.Lock()
	pending := updates.rooms[room.Number]
	if pending.created == room {
		// Existing lobby users did not see the queued create, but the creator was
		// sent a targeted field list and may return to the lobby in this window.
		// Suppress the obsolete create while retaining the final exit delta.
		pending.created = nil
		pending.createOrder = 0
	}
	if pending.users == room {
		pending.users = nil
		pending.usersOrder = 0
	}
	if pending.fieldState == room {
		pending.fieldState = nil
		pending.fieldOrder = 0
		pending.fieldAudience = 0
	}
	if len(pending.gameStates) != 0 {
		retained := pending.gameStates[:0]
		for _, gameState := range pending.gameStates {
			if gameState.room != room {
				retained = append(retained, gameState)
			}
		}
		pending.gameStates = retained
	}
	if pending.exited != room {
		pending.exited = room
		pending.exitOrder = updates.nextOrderLocked()
	}
	if pending.empty() {
		delete(updates.rooms, room.Number)
	} else {
		updates.rooms[room.Number] = pending
	}
	s.armLobbyUpdateTimerLocked()
	updates.mu.Unlock()
}

func (updates *lobbyUpdateState) nextOrderLocked() uint64 {
	updates.nextOrder++
	return updates.nextOrder
}

func (s *Server) armLobbyUpdateTimerLocked() {
	updates := &s.lobbyUpdates
	if updates.timer != nil || updates.flushing || !updates.pendingLocked() {
		return
	}
	delay := updates.delay
	if delay <= 0 {
		delay = lobbyUpdateWindow
	}
	updates.timer = time.AfterFunc(delay, s.flushLobbyUpdates)
}

func (updates *lobbyUpdateState) pendingLocked() bool {
	return updates.roster.dirty || updates.joinAllow.dirty || len(updates.rooms) != 0
}

func (s *Server) flushLobbyUpdates() {
	updates := &s.lobbyUpdates
	updates.mu.Lock()
	if updates.flushing {
		updates.mu.Unlock()
		return
	}
	if updates.timer != nil {
		updates.timer.Stop()
		updates.timer = nil
	}
	if !updates.pendingLocked() {
		updates.mu.Unlock()
		return
	}
	updates.flushing = true
	events := updates.takeEventsLocked()
	updates.mu.Unlock()

	s.flushLobbyUpdateEvents(events)

	updates.mu.Lock()
	updates.flushing = false
	s.armLobbyUpdateTimerLocked()
	updates.mu.Unlock()
}

func (updates *lobbyUpdateState) takeEventsLocked() []lobbyUpdateEvent {
	eventCapacity := 2
	for _, pending := range updates.rooms {
		eventCapacity += 4 + len(pending.gameStates)
	}
	events := make([]lobbyUpdateEvent, 0, eventCapacity)
	if updates.roster.dirty {
		events = append(events, lobbyUpdateEvent{kind: lobbyUpdateRoster, order: updates.roster.order})
	}
	if updates.joinAllow.dirty {
		events = append(events, lobbyUpdateEvent{kind: lobbyUpdateJoinAllow, order: updates.joinAllow.order})
	}
	for _, pending := range updates.rooms {
		if pending.exited != nil {
			events = append(events, lobbyUpdateEvent{kind: lobbyUpdateFieldExit, order: pending.exitOrder, room: pending.exited})
		}
		if pending.created != nil {
			events = append(events, lobbyUpdateEvent{kind: lobbyUpdateFieldCreate, order: pending.createOrder, room: pending.created})
		}
		if pending.users != nil {
			events = append(events, lobbyUpdateEvent{kind: lobbyUpdateFieldUsers, order: pending.usersOrder, room: pending.users})
		}
		if pending.fieldState != nil {
			events = append(events, lobbyUpdateEvent{
				kind:            lobbyUpdateFieldState,
				order:           pending.fieldOrder,
				room:            pending.fieldState,
				audienceVersion: pending.fieldAudience,
			})
		}
		for _, gameState := range pending.gameStates {
			events = append(events, lobbyUpdateEvent{
				kind:            lobbyUpdateGameState,
				order:           gameState.order,
				room:            gameState.room,
				started:         gameState.started,
				audienceVersion: gameState.audience,
			})
		}
	}
	updates.roster = pendingLobbyFlag{}
	updates.joinAllow = pendingLobbyFlag{}
	clear(updates.rooms)
	sort.Slice(events, func(i, j int) bool { return events[i].order < events[j].order })
	return events
}

func (s *Server) flushLobbyUpdateEvents(events []lobbyUpdateEvent) {
	metrics := s.lobbyUpdateMetrics
	if metrics != nil {
		metrics.flushes.Add(1)
		metrics.events.Add(uint64(len(events)))
		storeLobbyMax(&metrics.maxEventsPerFlush, uint64(len(events)))
	}
	targets := s.channelSessions()
	if len(targets) == 0 {
		return
	}
	if hasLobbyUpdateKind(events, lobbyUpdateRoster) {
		s.channelRosterMu.Lock()
		defer s.channelRosterMu.Unlock()
	}

	messages := make([]transport.OutboundMessage, 0, len(events))
	audienceVersions := make([]uint64, 0, len(events))
	rosterMessageIndexes := make(map[int]struct{})
	joinAllowMessageIndexes := make(map[int]struct{})
	var rosterMessages map[*Session][]transport.OutboundMessage
	var joinAllowMessages map[*Session]transport.OutboundMessage
	appendMessage := func(message transport.OutboundMessage, audienceVersion uint64) {
		messages = append(messages, message)
		audienceVersions = append(audienceVersions, audienceVersion)
	}
	for _, event := range events {
		switch event.kind {
		case lobbyUpdateRoster:
			messagesBySession, err := s.channelRosterMessages(targets)
			if err != nil {
				s.logger.Warn("build coalesced channel roster", "error", err)
				continue
			}
			rosterMessages = messagesBySession
			rosterMessageIndexes[len(messages)] = struct{}{}
			appendMessage(transport.OutboundMessage{MessageID: ReplyChannelChars}, 0)
		case lobbyUpdateJoinAllow:
			rooms := s.roomSnapshots()
			payloadsByLevel := make(map[byte][]byte)
			messagesBySession := make(map[*Session]transport.OutboundMessage, len(targets))
			buildFailed := false
			for _, target := range targets {
				_, _, _, level := target.identity()
				payload, ok := payloadsByLevel[level]
				if !ok {
					var err error
					payload, err = JoinAllowPayload(rooms, level)
					if err != nil {
						s.logger.Warn("build coalesced join-allow broadcast", "error", err)
						buildFailed = true
						break
					}
					payloadsByLevel[level] = payload
				}
				messagesBySession[target] = transport.OutboundMessage{MessageID: ReplyJoinAllow, Payload: payload}
			}
			if buildFailed {
				continue
			}
			joinAllowMessages = messagesBySession
			joinAllowMessageIndexes[len(messages)] = struct{}{}
			appendMessage(transport.OutboundMessage{MessageID: ReplyJoinAllow}, 0)
		case lobbyUpdateFieldCreate:
			payload, err := FieldCreatePayload(event.room.Snapshot())
			if err != nil {
				s.logger.Warn("build coalesced field-create broadcast", "room", event.room.Number, "error", err)
				continue
			}
			appendMessage(transport.OutboundMessage{MessageID: ReplyFieldCreate, Payload: payload}, 0)
		case lobbyUpdateFieldUsers:
			snapshot := event.room.Snapshot()
			appendMessage(transport.OutboundMessage{
				MessageID: ReplyFieldUsers,
				Payload:   FieldUsersPayload(snapshot.Number, snapshot.Users),
			}, 0)
		case lobbyUpdateFieldState:
			payload, err := FieldStatePayload(event.room.Snapshot())
			if err != nil {
				s.logger.Warn("build coalesced field-state broadcast", "room", event.room.Number, "error", err)
				continue
			}
			appendMessage(transport.OutboundMessage{MessageID: ReplyFieldState, Payload: payload}, event.audienceVersion)
		case lobbyUpdateGameState:
			appendMessage(transport.OutboundMessage{
				MessageID: ReplyGameState,
				Payload:   GameStatePayload(event.room.Number, event.started),
			}, event.audienceVersion)
		case lobbyUpdateFieldExit:
			appendMessage(transport.OutboundMessage{
				MessageID: ReplyFieldExit,
				Payload:   FieldExitPayload(event.room.Number),
			}, 0)
		}
	}
	batch := make([]transport.OutboundMessage, 0, len(messages))
	var recipientBatches uint64
	var deliveredFrames uint64
	var audienceFilteredFrames uint64
	var failedBatches uint64
	var failedFrames uint64
	var maxFramesPerBatch uint64
	for _, session := range targets {
		lobbySince := session.lobbyAudienceVersion()
		batch = batch[:0]
		for index, message := range messages {
			audienceVersion := audienceVersions[index]
			if audienceVersion == 0 || lobbySince == 0 || lobbySince <= audienceVersion {
				if _, isRoster := rosterMessageIndexes[index]; isRoster {
					replacement, ok := rosterMessages[session]
					if !ok {
						continue
					}
					batch = append(batch, replacement...)
					continue
				}
				if _, isJoinAllow := joinAllowMessageIndexes[index]; isJoinAllow {
					replacement, ok := joinAllowMessages[session]
					if !ok {
						continue
					}
					batch = append(batch, replacement)
					continue
				}
				batch = append(batch, message)
			} else if metrics != nil {
				audienceFilteredFrames++
			}
		}
		if len(batch) == 0 {
			continue
		}
		if err := session.WriteMessages(batch); err != nil {
			if metrics != nil {
				failedBatches++
				failedFrames += uint64(len(batch))
			}
			s.logger.Debug("coalesced lobby broadcast failed", "remote", session.wire.RemoteAddr(), "error", err)
			continue
		}
		if metrics != nil {
			recipientBatches++
			deliveredFrames += uint64(len(batch))
			if uint64(len(batch)) > maxFramesPerBatch {
				maxFramesPerBatch = uint64(len(batch))
			}
		}
	}
	if metrics != nil {
		metrics.recipientBatches.Add(recipientBatches)
		metrics.deliveredFrames.Add(deliveredFrames)
		metrics.audienceFilteredFrames.Add(audienceFilteredFrames)
		metrics.failedBatches.Add(failedBatches)
		metrics.failedFrames.Add(failedFrames)
		storeLobbyMax(&metrics.maxFramesPerBatch, maxFramesPerBatch)
	}
}

func storeLobbyMax(target *atomic.Uint64, value uint64) {
	for current := target.Load(); value > current; current = target.Load() {
		if target.CompareAndSwap(current, value) {
			return
		}
	}
}

func hasLobbyUpdateKind(events []lobbyUpdateEvent, kind lobbyUpdateKind) bool {
	for _, event := range events {
		if event.kind == kind {
			return true
		}
	}
	return false
}
