package game

import (
	"fmt"
	"strings"

	"wtserver-go/internal/transport"
)

// Five channel sheets are selectable using values 0 through 4.
// Channel member IDs and roster counts are both one byte. Keeping one value
// unused avoids relying on a special meaning for 0xff in other channel
// messages. Large sheets use continuation chunks because one encrypted frame
// cannot hold 255 variable-length user records.
const (
	channelSheetCount    = 5
	channelSheetCapacity = 255
)

func (s *Server) assignChannelMemberLocked(session *Session, sheet byte) error {
	if sheet >= channelSheetCount {
		return fmt.Errorf("channel sheet %d is out of range", sheet)
	}
	if session.channelMemberAssigned {
		return nil
	}
	for memberID := 0; memberID < channelSheetCapacity; memberID++ {
		if s.channelMembers[sheet][memberID] != nil {
			continue
		}
		s.channelMembers[sheet][memberID] = session
		session.channelSheet = sheet
		session.channelMemberID = byte(memberID)
		session.channelMemberAssigned = true
		return nil
	}
	return fmt.Errorf("channel sheet %d is full", sheet)
}

func (s *Server) assignAnyChannelMemberLocked(session *Session) error {
	if session.channelMemberAssigned {
		return nil
	}
	preferred := session.channelSheet
	if preferred < channelSheetCount {
		if err := s.assignChannelMemberLocked(session, preferred); err == nil {
			return nil
		}
	}
	for sheet := byte(0); sheet < channelSheetCount; sheet++ {
		if sheet == preferred {
			continue
		}
		if err := s.assignChannelMemberLocked(session, sheet); err == nil {
			return nil
		}
	}
	return fmt.Errorf("all channel sheets are full")
}

func channelMemberEligible(session *Session) bool {
	loginName, callsign, userID, _ := session.identity()
	session.mu.RLock()
	inLobby := session.RoomNumber == -1 && userID > 0 && loginName != "" && strings.TrimSpace(callsign) != ""
	session.mu.RUnlock()
	return inLobby
}

func (s *Server) syncChannelMemberLocked(session *Session) error {
	if channelMemberEligible(session) {
		return s.assignAnyChannelMemberLocked(session)
	}
	s.releaseChannelMemberLocked(session)
	return nil
}

func (s *Server) releaseChannelMemberLocked(session *Session) {
	if !session.channelMemberAssigned {
		return
	}
	sheet := session.channelSheet
	memberID := session.channelMemberID
	if sheet < channelSheetCount && memberID < channelSheetCapacity &&
		s.channelMembers[sheet][memberID] == session {
		s.channelMembers[sheet][memberID] = nil
	}
	session.channelMemberAssigned = false
}

func (s *Server) moveChannelMember(session *Session, requested byte) (byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.moveChannelMemberLocked(session, requested)
}

func (s *Server) moveChannelMemberLocked(session *Session, requested byte) (byte, bool, error) {
	if requested >= channelSheetCount {
		return session.channelSheet, false, fmt.Errorf("channel sheet %d is out of range", requested)
	}
	if _, tracked := s.sessions[session]; !tracked {
		return session.channelSheet, false, fmt.Errorf("channel session is not tracked")
	}
	if !channelMemberEligible(session) {
		return session.channelSheet, false, fmt.Errorf("channel session is not in the lobby")
	}
	if !session.channelMemberAssigned {
		if err := s.assignChannelMemberLocked(session, requested); err != nil {
			return session.channelSheet, false, err
		}
		return requested, true, nil
	}
	if session.channelSheet == requested {
		return requested, false, nil
	}

	memberID := -1
	for candidate := 0; candidate < channelSheetCapacity; candidate++ {
		if s.channelMembers[requested][candidate] == nil {
			memberID = candidate
			break
		}
	}
	if memberID < 0 {
		return session.channelSheet, false, fmt.Errorf("channel sheet %d is full", requested)
	}
	s.releaseChannelMemberLocked(session)
	s.channelMembers[requested][memberID] = session
	session.channelSheet = requested
	session.channelMemberID = byte(memberID)
	session.channelMemberAssigned = true
	return requested, true, nil
}

type channelEnterTransition struct {
	user             ChannelUser
	roster           []ChannelUser
	destinationPeers []*Session
	previousPeers    []*Session
	previousMemberID byte
	hadPrevious      bool
	changed          bool
}

// prepareChannelEnter moves the member and captures every recipient and roster
// record under the same server lock. Network writes happen after the lock is
// released so a slow connection cannot block unrelated membership changes.
func (s *Server) prepareChannelEnter(session *Session, requested byte) (channelEnterTransition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	transition := channelEnterTransition{
		previousMemberID: session.channelMemberID,
		hadPrevious:      session.channelMemberAssigned,
	}
	previousSheet := session.channelSheet
	selected, changed, err := s.moveChannelMemberLocked(session, requested)
	if err != nil {
		return channelEnterTransition{}, err
	}
	transition.changed = changed

	for _, candidate := range s.online.lobby.sessions() {
		if !candidate.channelMemberAssigned || candidate.channelSheet >= channelSheetCount {
			continue
		}
		if transition.hadPrevious && changed && candidate != session && candidate.channelSheet == previousSheet {
			transition.previousPeers = append(transition.previousPeers, candidate)
		}
		if candidate.channelSheet != selected {
			continue
		}

		user := channelUserRecord(candidate)
		transition.roster = append(transition.roster, user)
		if candidate == session {
			transition.user = user
		} else {
			transition.destinationPeers = append(transition.destinationPeers, candidate)
		}
	}
	return transition, nil
}

func (s *Server) channelUser(session *Session) (ChannelUser, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, tracked := s.sessions[session]; !tracked || !session.channelMemberAssigned {
		return ChannelUser{}, false
	}
	return channelUserRecord(session), true
}

func channelUserRecord(session *Session) ChannelUser {
	session.mu.RLock()
	defer session.mu.RUnlock()
	return ChannelUser{
		MemberID:    session.channelMemberID,
		SessionID:   session.SessionID(),
		LoginName:   session.LoginName,
		Callsign:    session.Callsign,
		Authority:   session.UserAuthority,
		Level:       session.UserLevel,
		PrideMarkID: session.PrideMarkID,
		PrideName:   session.PrideName,
	}
}

func requestedChannelSheet(payload []byte) (byte, error) {
	if len(payload) == 0 {
		return 0, fmt.Errorf("channel sheet request is empty")
	}
	if payload[0] >= channelSheetCount {
		return 0, fmt.Errorf("channel sheet %d is out of range", payload[0])
	}
	return payload[0], nil
}

func (s *Server) handleChannelEnter(session *Session, payload []byte) error {
	requested, err := requestedChannelSheet(payload)
	if err != nil {
		return err
	}

	// Serialize replacement snapshots and membership deltas so a scheduled
	// roster refresh cannot interleave them on a connection.
	s.channelRosterMu.Lock()
	defer s.channelRosterMu.Unlock()

	transition, moveErr := s.prepareChannelEnter(session, requested)
	if moveErr != nil {
		if s.logger != nil {
			s.logger.Warn("enter channel sheet", "requested_sheet", requested, "error", moveErr)
		}
		// Preserve the recovery behavior, but limit the replacement roster to
		// the requester.
		messagesBySession, buildErr := s.channelRosterMessages([]*Session{session})
		if buildErr != nil {
			return buildErr
		}
		if messages := messagesBySession[session]; len(messages) != 0 {
			return session.WriteMessages(messages)
		}
		return nil
	}

	rosterMessages, err := channelRosterOutboundMessages(requested, transition.roster)
	if err != nil {
		return err
	}
	enterPayload, err := ChannelEnterPayload(transition.user)
	if err != nil {
		return err
	}

	// The entrant receives a replacement roster, while members already
	// displaying the destination sheet append only this user.
	if writeErr := session.WriteMessages(rosterMessages); writeErr != nil {
		return writeErr
	}
	if transition.hadPrevious && transition.changed {
		exitPayload := ChannelExitPayload(transition.previousMemberID)
		for _, peer := range transition.previousPeers {
			if writeErr := peer.Write(ReplyChannelExit, exitPayload); writeErr != nil && s.logger != nil {
				s.logger.Debug("channel exit delta failed", "session_id", peer.SessionID(), "error", writeErr)
			}
		}
	}
	for _, peer := range transition.destinationPeers {
		if writeErr := peer.Write(ReplyChannelEnter, enterPayload); writeErr != nil && s.logger != nil {
			s.logger.Debug("channel enter delta failed", "session_id", peer.SessionID(), "error", writeErr)
		}
	}
	return nil
}

func (s *Server) handleChangeChannelSheet(session *Session, payload []byte) error {
	// Message 0x151e selects general interface sheets; channel membership is
	// changed by 0x1506.
	//
	// Replay a deferred room exit immediately before the Return Lobby reply so
	// cached room state is cleared before event 201 chooses between the field
	// list and a room lobby.
	if len(payload) > 0 && payload[0] == 1 && s.sessionRoom(session) == nil {
		if slot, pending := session.pendingClientRoomDeparture(); pending {
			messages := []transport.OutboundMessage{
				{MessageID: ReplyExitRoom, Payload: ExitRoomPayload(slot)},
				{MessageID: ReplyChangeSheet, Payload: ChangeSheetPayload(payload)},
			}
			if err := session.WriteMessages(messages); err != nil {
				return err
			}
			session.clearPendingClientRoomExit(slot)
			return nil
		}
	}
	return session.Write(ReplyChangeSheet, ChangeSheetPayload(payload))
}

// wpackListIDs returns the W-Packs that have server-defined contents.
func (s *Server) wpackListIDs() []uint16 {
	var ids []uint16
	for _, override := range s.shopCatalog.WPackPriceOverrides() {
		ids = append(ids, uint16(override.ItemID))
	}
	return ids
}
