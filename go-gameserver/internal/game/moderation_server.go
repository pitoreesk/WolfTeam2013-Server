package game

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wtserver-go/internal/store"
)

func (s *Server) handleGMCommand(ctx context.Context, actor *Session, request GMCommandRequest) error {
	if actor == nil || !actor.hasStaffAuthority() {
		return nil
	}

	_, actorCallsign, actorUserID, _ := actor.identity()
	switch request.Command {
	case GMCommandSilenceOn, GMCommandSilenceOff:
		enabled := request.Command == GMCommandSilenceOn
		s.setChannelSilenced(enabled)
		payload, err := GMCommandAckPayload(request.Command, request.Reason, actorCallsign)
		if err != nil {
			return err
		}
		for _, target := range s.authenticatedSessions() {
			if writeErr := target.Write(ReplyGMCommand, payload); writeErr != nil && target == actor {
				return writeErr
			}
		}
		if s.logger != nil {
			s.logger.Info("GM changed global channel silence",
				"user_id", actorUserID, "callsign", actorCallsign, "enabled", enabled)
		}
		return nil
	case GMCommandMuteBan, GMCommandKick, GMCommandBan:
		return s.handleTargetedGMCommand(ctx, actor, actorCallsign, actorUserID, request)
	case GMCommandSearch:
		return s.handleGMSearch(actor, request.Target)
	default:
		return fmt.Errorf("unknown GM command %d", request.Command)
	}
}

func (s *Server) handleTargetedGMCommand(
	ctx context.Context,
	actor *Session,
	actorCallsign string,
	actorUserID int,
	request GMCommandRequest,
) error {
	request.Target = strings.TrimSpace(request.Target)
	if request.Target == "" {
		return nil
	}
	target := s.sessionByCallsign(request.Target)
	if target == actor {
		return nil
	}

	now := time.Now().UTC()
	state := store.ModerationState{
		Active:    true,
		StartedAt: now,
		Reason:    request.Reason,
	}
	if !request.Permanent && request.Command != GMCommandKick {
		if request.Duration <= 0 {
			return s.sendGMNotice(actor, moderationUsage(request.Command))
		}
		state.ExpiresAt = now.Add(request.Duration)
	}

	persisted := false
	if moderation, ok := s.users.(store.ModerationStore); ok {
		var found bool
		var err error
		switch request.Command {
		case GMCommandMuteBan:
			found, err = moderation.SetMutedByCallsign(ctx, request.Target, state)
		case GMCommandBan:
			found, err = moderation.SetBannedByCallsign(ctx, request.Target, state)
		}
		if err != nil {
			s.logBackendFallback("persist GM moderation", actorUserID, err)
			return err
		}
		persisted = found
	}
	if target == nil && !(persisted && request.Command != GMCommandKick) {
		return s.sendGMNotice(actor, fmt.Sprintf("%s was not found.", request.Target))
	}

	subject := request.Target
	if target != nil {
		_, currentCallsign, _, _ := target.identity()
		if currentCallsign != "" {
			subject = currentCallsign
		}
	}
	actorPayload, err := GMCommandAckPayload(request.Command, request.Reason, subject)
	if err != nil {
		return err
	}
	if err := actor.Write(ReplyGMCommand, actorPayload); err != nil {
		return err
	}
	if target == nil {
		return nil
	}

	targetPayload, err := GMCommandAckPayload(request.Command, request.Reason, actorCallsign)
	if err != nil {
		return err
	}
	_ = target.Write(ReplyGMCommand, targetPayload)
	switch request.Command {
	case GMCommandMuteBan:
		target.setMutedUntil(true, state.ExpiresAt)
		mutePayload, buildErr := MuteBanPayload(
			true, state.StartedAt, state.ExpiresAt, state.Reason,
		)
		if buildErr != nil {
			return buildErr
		}
		_ = target.Write(ReplyMuteBan, mutePayload)
	case GMCommandKick, GMCommandBan:
		if target.wire != nil {
			_ = target.wire.Close()
		}
	}
	if s.logger != nil {
		s.logger.Info("GM moderation command",
			"user_id", actorUserID, "callsign", actorCallsign,
			"command", byte(request.Command), "target", subject,
			"duration", request.Duration, "permanent", request.Permanent,
			"reason", request.Reason)
	}
	return nil
}

func (s *Server) handleGMSearch(actor *Session, callsign string) error {
	return s.sendGMNotice(actor, s.gmSearchDescription(callsign))
}

func (s *Server) gmSearchDescription(callsign string) string {
	target := s.sessionByCallsign(callsign)
	if target == nil {
		return fmt.Sprintf("%s is offline.", callsign)
	}
	_, subject, _, _ := target.identity()
	roomNumber, _ := target.roomLocation()
	if roomNumber >= 0 {
		state := "waiting"
		if room := s.roomByNumber(roomNumber); room != nil && room.Snapshot().GameStarted {
			state = "playing"
		}
		return fmt.Sprintf("%s is in room %d (%s).", subject, roomNumber, state)
	}
	s.mu.RLock()
	sheet, assigned := target.channelSheet, target.channelMemberAssigned
	s.mu.RUnlock()
	if assigned {
		return fmt.Sprintf("%s is in the lobby on sheet %d.", subject, int(sheet)+1)
	}
	return fmt.Sprintf("%s is online.", subject)
}

func (s *Server) sendGMNotice(session *Session, text string) error {
	payload, err := ChannelSystemMessagePayload(text)
	if err != nil {
		return err
	}
	return session.Write(ReplyChannelChat, payload)
}

func (s *Server) setChannelSilenced(enabled bool) {
	s.mu.Lock()
	s.channelSilenced = enabled
	s.mu.Unlock()
}

func (s *Server) channelChatAllowed(session *Session) bool {
	if session == nil || session.hasStaffAuthority() {
		return true
	}
	if session.muted() {
		return false
	}
	s.mu.RLock()
	silenced := s.channelSilenced
	s.mu.RUnlock()
	return !silenced
}

func (s *Server) sessionByCallsign(callsign string) *Session {
	key := normalizedCallsign(strings.TrimSpace(callsign))
	if key == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sessions := s.online.byCallsign[key]; sessions != nil {
		return sessions.newest()
	}
	return nil
}

func (s *Server) authenticatedSessions() []*Session {
	s.mu.RLock()
	candidates := s.online.connections.sessions()
	s.mu.RUnlock()
	result := make([]*Session, 0, len(candidates))
	for _, session := range candidates {
		loginName, callsign, userID, _ := session.identity()
		if loginName != "" && callsign != "" && userID > 0 {
			result = append(result, session)
		}
	}
	return result
}
