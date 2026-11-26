package game

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

type privateLadderChallenge struct {
	Token      uint16
	Challenger *Session
	Target     *Session
}

type privateLadderQueueEntry struct {
	Session *Session
	Request PrivateLadderSearchRequest
}

func (s *Server) handlePrivateLadderMessage(ctx context.Context, session *Session, frame transport.Frame) error {
	ladder, backed := s.users.(store.PrivateLadderStore)
	_, _, userID, _ := session.identity()
	switch frame.MessageID {
	case RequestPrivateInfo:
		info := store.PrivateLadderInfo{}
		if backed {
			loaded, err := ladder.PrivateLadderInfo(ctx, userID)
			if err != nil {
				s.logBackendFallback("load private-ladder info", userID, err)
			} else {
				info = loaded
			}
		}
		if err := session.Write(ReplyPrivateInfo, PrivateLadderInfoPayload(info)); err != nil {
			return err
		}
		// INFO is the first request of the ladder screen; the dialog behind
		// "EŞLEŞME BAŞLASIN" stays disabled until the option list arrives.
		switch session.channelType() {
		case store.ChannelTypeLadder, store.ChannelTypePrivateLadder:
			options, err := LadderOptionsPayload(ladderOptionModes())
			if err != nil {
				return err
			}
			return session.Write(ReplyLadderOptions, options)
		}
		return nil
	case RequestPrivateNotice:
		notice := store.PrivateLadderNotice{}
		if backed {
			loaded, err := ladder.PrivateLadderNotice(ctx, time.Now())
			if err != nil {
				s.logBackendFallback("load private-ladder notice", userID, err)
			} else {
				notice = loaded
			}
		}
		payload, err := PrivateLadderNoticePayload(notice)
		if err != nil {
			return err
		}
		return session.Write(ReplyPrivateNotice, payload)
	case RequestPrivateRankList:
		request, err := ParsePrivateLadderRankRequest(frame.Payload)
		if err != nil {
			return err
		}
		top, page := []store.PrivateLadderRank(nil), []store.PrivateLadderRank(nil)
		if backed {
			topSize, pageSize := min(int(request.TopSize), 32), min(int(request.PageSize), 64)
			top, err = ladder.PrivateLadderRanks(ctx, 0, topSize)
			if err == nil {
				page, err = ladder.PrivateLadderRanks(ctx, int(request.Page)*pageSize, pageSize)
			}
			if err != nil {
				s.logBackendFallback("load private-ladder ranks", userID, err)
				top, page = nil, nil
			}
		}
		payload, err := PrivateLadderRanksPayload(top, page)
		if err != nil {
			return err
		}
		return session.Write(ReplyPrivateRankList, payload)
	case RequestPrivateFindStart:
		request, err := ParsePrivateLadderSearchRequest(frame.Payload)
		if err != nil {
			return err
		}
		return s.startPrivateLadderSearch(ctx, session, request)
	case RequestPrivateFindEnd:
		status, err := ParsePrivateLadderSearchEndRequest(frame.Payload)
		if err != nil {
			return err
		}
		s.removePrivateLadderSearch(session)
		s.clearLadderSearch(session)
		if err := session.Write(ReplyPrivateFindEnd, PrivateLadderSearchEndPayload(status)); err != nil {
			return err
		}
		return session.Write(ReplyPrivateNotify, PrivateLadderNotifyPayload(false))
	case RequestPrivateChallenge:
		targetName, err := ParsePrivateChallengeRequest(frame.Payload)
		if err != nil {
			return err
		}
		return s.createPrivateChallenge(ctx, session, targetName)
	case RequestPrivateReceive:
		response, err := ParsePrivateChallengeResponse(frame.Payload)
		if err != nil {
			return err
		}
		return s.resolvePrivateChallenge(ctx, session, response.Token, response.Decision, response.Rule)
	case RequestPrivateResult:
		request, err := ParsePrivateChallengeResultRequest(frame.Payload)
		if err != nil {
			return err
		}
		return s.resolvePrivateChallenge(ctx, session, request.Token, request.Decision, 0)
	case RequestPrivateLevel:
		return session.Write(ReplyPrivateLevel, PrivateLadderLevelPayload())
	case RequestLoginRestrict:
		return session.Write(ReplyLoginRestrict, LoginRestrictPayload(0))
	default:
		return fmt.Errorf("unsupported private-ladder message 0x%04x", frame.MessageID)
	}
}

// ladderOptionModes is the 0x1B16 list: the four ladder modes on every map
// id (the client keeps only the maps valid for each mode), 1 vs 1 only.
func ladderOptionModes() []LadderOptionMode {
	maps := make([]LadderOptionMap, 0, 64)
	for id := 0; id < 64; id++ {
		maps = append(maps, LadderOptionMap{Map: byte(id), Capacities: ladderCapacityOneVsOne})
	}
	modes := make([]LadderOptionMode, 0, len(ladderModes))
	for _, mode := range ladderModes {
		modes = append(modes, LadderOptionMode{ModeWord: mode, Maps: maps})
	}
	return modes
}

func (s *Server) startPrivateLadderSearch(ctx context.Context, session *Session, request PrivateLadderSearchRequest) error {
	if session.channelType() == store.ChannelTypePrivateLadder {
		return s.startPrivateLadderQueueSearch(ctx, session, request)
	}
	return s.startLadderRoomSearch(session, request)
}

// startLadderRoomSearch follows the ladder (type 3) client model: the server
// answers with open rooms whose creator is searching compatibly and the client
// enters them; with none, the client creates its own room (sub_34214A00 ->
// sub_34184270 CREATE_REQ) and waits there for the next searcher.
func (s *Server) startLadderRoomSearch(session *Session, request PrivateLadderSearchRequest) error {
	s.privateLadderMu.Lock()
	s.ladderSearches[session] = request
	s.privateLadderMu.Unlock()
	if s.sessionRoom(session) == nil {
		if room, ok := s.findLadderRoom(session, request); ok {
			s.adaptLadderRoom(room, request)
			s.clearLadderSearch(session)
			s.logger.Info("ladder search matched", "room", room.Number,
				"mode", fmt.Sprintf("0x%x", request.ModeWord), "map", request.Map)
			if err := session.Write(ReplyPrivateFindStart, LadderRoomListPayload([]int16{room.Number})); err != nil {
				return err
			}
			return session.Write(ReplyPrivateNotify, PrivateLadderNotifyPayload(false))
		}
	}
	s.logger.Info("ladder search waiting", "mode", fmt.Sprintf("0x%x", request.ModeWord), "map", request.Map)
	if err := session.Write(ReplyPrivateFindStart, LadderRoomListPayload(nil)); err != nil {
		return err
	}
	return session.Write(ReplyPrivateNotify, PrivateLadderNotifyPayload(true))
}

// findLadderRoom returns an open room created by another player who is still
// searching compatibly with request.
func (s *Server) findLadderRoom(searcher *Session, request PrivateLadderSearchRequest) (*Room, bool) {
	for _, snapshot := range s.roomSnapshots() {
		if snapshot.GameStarted || snapshot.Users != 1 || snapshot.Users >= snapshot.Config.Capacity || snapshot.Config.Password != "" {
			continue
		}
		room := s.roomByNumber(snapshot.Number)
		if room == nil {
			continue
		}
		creatorSearch, creator, ok := room.LadderSearch()
		if !ok || creator == searcher {
			continue
		}
		active, searching := s.ladderSearchOf(creator)
		if !searching || active != creatorSearch || !creatorSearch.Compatible(request) {
			continue
		}
		return room, true
	}
	return nil, false
}

// adaptLadderRoom switches a room created by a "random" searcher to the
// joiner's concrete mode/map with the room-rule update every member applies
// (0x1615 CHANGERULE_ACK).
func (s *Server) adaptLadderRoom(room *Room, request PrivateLadderSearchRequest) {
	creatorSearch, _, _ := room.LadderSearch()
	config := room.Snapshot().Config
	changed := false
	if creatorSearch.AnyMode() && !request.AnyMode() && config.Mode != request.ModeWord {
		config.Mode, changed = request.ModeWord, true
	}
	if creatorSearch.Map == ladderAnyByte && request.Map != ladderAnyByte && config.Map != request.Map {
		config.Map, changed = request.Map, true
	}
	if !changed {
		return
	}
	room.UpdateConfig(config)
	room.Broadcast(ReplyChangeRule, ChangeRuleConfigPayload(config))
}

// clearLadderSearch forgets a player's ladder search (FIND_END, match).
func (s *Server) clearLadderSearch(session *Session) {
	s.privateLadderMu.Lock()
	delete(s.ladderSearches, session)
	s.privateLadderMu.Unlock()
}

// ladderSearchOf returns the active ladder search of a player.
func (s *Server) ladderSearchOf(session *Session) (PrivateLadderSearchRequest, bool) {
	s.privateLadderMu.Lock()
	defer s.privateLadderMu.Unlock()
	request, ok := s.ladderSearches[session]
	return request, ok
}

// startPrivateLadderQueueSearch pairs private-ladder (type 4) players into a
// server-created room; two searches meet when Compatible.
func (s *Server) startPrivateLadderQueueSearch(ctx context.Context, session *Session, request PrivateLadderSearchRequest) error {
	if s.sessionRoom(session) != nil {
		return session.Write(ReplyPrivateFindStart, LadderRoomListPayload(nil))
	}
	var opponent *Session
	var opponentRequest PrivateLadderSearchRequest
	s.privateLadderMu.Lock()
	s.removePrivateLadderSearchLocked(session)
	for element := s.privateLadderQueue.Front(); element != nil; {
		next := element.Next()
		entry := element.Value.(privateLadderQueueEntry)
		entry.Session.mu.RLock()
		available := entry.Session.RoomNumber == -1
		entry.Session.mu.RUnlock()
		if !available {
			s.privateLadderQueue.Remove(element)
			delete(s.privateLadderSearches, entry.Session)
		} else if entry.Request.Compatible(request) {
			s.privateLadderQueue.Remove(element)
			delete(s.privateLadderSearches, entry.Session)
			opponent, opponentRequest = entry.Session, entry.Request
			break
		}
		element = next
	}
	if opponent == nil {
		s.privateLadderSearches[session] = s.privateLadderQueue.PushBack(privateLadderQueueEntry{Session: session, Request: request})
	}
	s.privateLadderMu.Unlock()
	if opponent == nil {
		if err := session.Write(ReplyPrivateFindStart, LadderRoomListPayload(nil)); err != nil {
			return err
		}
		return session.Write(ReplyPrivateNotify, PrivateLadderNotifyPayload(true))
	}
	return s.createPrivateLadderMatch(ctx, opponent, session, opponentRequest, request)
}

// ladderRoomConfig resolves two searches into a concrete ladder room.
func ladderRoomConfig(first, second PrivateLadderSearchRequest) RoomConfig {
	mode := first.ModeWord
	if mode == ladderAnyMode {
		mode = second.ModeWord
	}
	if mode == ladderAnyMode || mode == ladderAllModesSet {
		mode = ladderModes[rand.IntN(len(ladderModes))]
	}
	gameMap := first.Map
	if gameMap == ladderAnyByte {
		gameMap = second.Map
	}
	if gameMap == ladderAnyByte {
		gameMap = 0
	}
	return RoomConfig{Title: "Private Ladder", Capacity: 2, Map: gameMap, Mode: mode, Wins: 5, Points: 20, Time: 600}
}

func (s *Server) createPrivateLadderMatch(ctx context.Context, first, second *Session, firstRequest, secondRequest PrivateLadderSearchRequest) error {
	room, err := s.allocateRoom(ladderRoomConfig(firstRequest, secondRequest))
	if err != nil {
		return err
	}
	room.EnablePrivateLadder()
	if _, ok := room.Add(first); !ok {
		s.discardLadderRoom(room)
		return fmt.Errorf("private-ladder room rejected first player")
	}
	if _, ok := room.Add(second); !ok {
		_, _, _ = room.Remove(first)
		s.discardLadderRoom(room)
		return fmt.Errorf("private-ladder room rejected second player")
	}
	// Announce the room only once both players are in it.
	s.broadcastFieldCreate(room)
	s.refreshSessionLobby(first)
	s.refreshSessionLobby(second)
	s.removePrivateLadderSearch(first)
	s.removePrivateLadderSearch(second)
	peers := s.privateLadderPeers(room)
	number := room.Number
	payload, err := PrivateLadderSearchPayload(&number, peers, true)
	if err != nil {
		return err
	}
	if err := first.Write(ReplyPrivateFindStart, payload); err != nil {
		return err
	}
	if err := second.Write(ReplyPrivateFindStart, payload); err != nil {
		return err
	}
	_ = first.Write(ReplyPrivateNotify, PrivateLadderNotifyPayload(false))
	_ = second.Write(ReplyPrivateNotify, PrivateLadderNotifyPayload(false))
	if err := s.broadcastRoster(ctx, room); err != nil {
		return err
	}
	s.broadcastFieldUsers(room)
	s.broadcastJoinAllow()
	return nil
}

func (s *Server) privateLadderPeers(room *Room) []PrivateLadderPeer {
	snapshot := room.Snapshot()
	peers := make([]PrivateLadderPeer, 0, len(snapshot.Members))
	for _, member := range snapshot.Members {
		globalIP, globalPort, localIP, localPort := s.peerEndpoints(member)
		peers = append(peers, PrivateLadderPeer{
			GlobalIP: globalIP, GlobalPort: globalPort, LocalIP: localIP, LocalPort: localPort,
		})
	}
	return peers
}

func (s *Server) createPrivateChallenge(ctx context.Context, challenger *Session, targetName string) error {
	target := s.sessionForCallsign(targetName)
	if target == nil || target == challenger || s.sessionRoom(challenger) != nil || s.sessionRoom(target) != nil {
		return challenger.Write(ReplyPrivateChallenge, PrivateChallengeAckPayload(0xfa))
	}
	s.privateLadderMu.Lock()
	if s.privateLadderChallenges == nil {
		s.privateLadderChallenges = make(map[uint16]privateLadderChallenge)
	}
	if s.privateChallengeByUser == nil {
		s.privateChallengeByUser = make(map[*Session]uint16)
	}
	if s.privateChallengeByUser[challenger] != 0 || s.privateChallengeByUser[target] != 0 {
		s.privateLadderMu.Unlock()
		return challenger.Write(ReplyPrivateChallenge, PrivateChallengeAckPayload(0xfa))
	}
	for {
		s.privateLadderSequence++
		if s.privateLadderSequence != 0 {
			if _, exists := s.privateLadderChallenges[s.privateLadderSequence]; !exists {
				break
			}
		}
	}
	token := s.privateLadderSequence
	challenge := privateLadderChallenge{Token: token, Challenger: challenger, Target: target}
	s.privateLadderChallenges[token] = challenge
	s.privateChallengeByUser[challenger] = token
	s.privateChallengeByUser[target] = token
	s.privateLadderMu.Unlock()
	if err := challenger.Write(ReplyPrivateChallenge, PrivateChallengeAckPayload(0)); err != nil {
		return err
	}
	username, callsign, challengerID, level := challenger.identity()
	info := store.AccountSnapshot{}
	points := uint32(0)
	if loaded, err := s.users.AccountSnapshot(ctx, challengerID); err == nil {
		info = loaded
	}
	if ladder, ok := s.users.(store.PrivateLadderStore); ok {
		if loaded, err := ladder.PrivateLadderInfo(ctx, challengerID); err == nil {
			points = loaded.Points
		}
	}
	payload, err := PrivateChallengeReceivePayload(0, token, username, callsign, level, points, uint32(max(info.Wins, 0)), uint32(max(info.Losses, 0)))
	if err != nil {
		return err
	}
	return target.Write(ReplyPrivateReceive, payload)
}

func (s *Server) resolvePrivateChallenge(ctx context.Context, actor *Session, token uint16, decision byte, detail uint16) error {
	s.privateLadderMu.Lock()
	var challenge privateLadderChallenge
	if token != 0 {
		challenge = s.privateLadderChallenges[token]
	} else {
		if actorToken := s.privateChallengeByUser[actor]; actorToken != 0 {
			challenge = s.privateLadderChallenges[actorToken]
		}
	}
	if challenge.Token != 0 && (challenge.Target == actor || challenge.Challenger == actor) {
		s.removePrivateChallengeLocked(challenge)
	} else {
		challenge = privateLadderChallenge{}
	}
	s.privateLadderMu.Unlock()
	if challenge.Token == 0 {
		return actor.Write(ReplyPrivateResult, PrivateChallengeResultPayload(0xfa, 2, 0))
	}
	if decision == 0 {
		payload := PrivateChallengeResultPayload(0, 0, 0)
		_ = challenge.Challenger.Write(ReplyPrivateResult, payload)
		if challenge.Target != challenge.Challenger {
			_ = challenge.Target.Write(ReplyPrivateResult, payload)
		}
		return nil
	}
	// The RESULT_ACK room (result 1) is entered by both clients themselves
	// (sub_342368C0 -> ENTER_REQ). The challenge carries no verified mode, so
	// the room gets a ladder mode.
	any := PrivateLadderSearchRequest{ModeWord: ladderAnyMode, Map: ladderAnyByte, Capacity: ladderAnyByte}
	room, err := s.allocateRoom(ladderRoomConfig(any, any))
	if err != nil {
		_ = challenge.Challenger.Write(ReplyPrivateResult, PrivateChallengeResultPayload(0xfa, 2, 0))
		_ = challenge.Target.Write(ReplyPrivateResult, PrivateChallengeResultPayload(0xfa, 2, 0))
		return err
	}
	room.EnablePrivateLadder()
	s.broadcastFieldCreate(room)
	s.refreshSessionLobby(challenge.Challenger)
	s.refreshSessionLobby(challenge.Target)
	s.removePrivateLadderSearch(challenge.Challenger)
	s.removePrivateLadderSearch(challenge.Target)

	payload := PrivateChallengeResultPayload(0, 1, uint16(room.Number))
	if err := challenge.Challenger.Write(ReplyPrivateResult, payload); err != nil {
		return err
	}
	if err := challenge.Target.Write(ReplyPrivateResult, payload); err != nil {
		return err
	}
	return nil
}

func (s *Server) sessionForCallsign(callsign string) *Session {
	key := normalizedCallsign(callsign)
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := s.online.byCallsign[key]
	if set == nil {
		return nil
	}
	return set.oldestMatching(func(session *Session) bool {
		session.mu.RLock()
		matches := session.RoomNumber == -1 && normalizedCallsign(session.Callsign) == key
		session.mu.RUnlock()
		return matches
	})
}

func (s *Server) removePrivateLadderSession(session *Session) {
	s.privateLadderMu.Lock()
	s.removePrivateLadderSearchLocked(session)
	delete(s.ladderSearches, session)
	if token := s.privateChallengeByUser[session]; token != 0 {
		s.removePrivateChallengeLocked(s.privateLadderChallenges[token])
	}
	s.privateLadderMu.Unlock()
}

func (s *Server) removePrivateLadderSearch(session *Session) {
	s.privateLadderMu.Lock()
	s.removePrivateLadderSearchLocked(session)
	s.privateLadderMu.Unlock()
}

func (s *Server) removePrivateLadderSearchLocked(session *Session) {
	element := s.privateLadderSearches[session]
	if element == nil {
		return
	}
	s.privateLadderQueue.Remove(element)
	delete(s.privateLadderSearches, session)
}

func (s *Server) removePrivateChallengeLocked(challenge privateLadderChallenge) {
	if challenge.Token == 0 {
		return
	}
	delete(s.privateLadderChallenges, challenge.Token)
	if s.privateChallengeByUser[challenge.Challenger] == challenge.Token {
		delete(s.privateChallengeByUser, challenge.Challenger)
	}
	if s.privateChallengeByUser[challenge.Target] == challenge.Token {
		delete(s.privateChallengeByUser, challenge.Target)
	}
}

// discardLadderRoom releases a server-created ladder room that never got its
// players.
func (s *Server) discardLadderRoom(room *Room) {
	s.deleteRoom(room)
}
