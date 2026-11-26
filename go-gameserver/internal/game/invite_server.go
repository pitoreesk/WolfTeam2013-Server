package game

import (
	"fmt"
	"strings"
)

func (s *Server) handleInviteSelect(sender *Session, request InviteSelectRequest) error {
	_, senderCallsign, senderUserID, _ := sender.identity()
	if senderUserID < 1 {
		return fmt.Errorf("invite-select request before authentication")
	}
	room := s.sessionRoom(sender)
	if room == nil || request.RoomNumber != uint16(room.Number) {
		return fmt.Errorf("invite-select room does not match sender room")
	}
	if _, member := room.MemberSlot(sender); !member {
		return fmt.Errorf("invite-select sender is not a room member")
	}
	snapshot := room.Snapshot()
	if !snapshot.Open || snapshot.GameStarted {
		return fmt.Errorf("invite-select room is not accepting invitations")
	}

	selected := make(map[*Session]struct{}, len(request.Targets))
	for _, requestedCallsign := range request.Targets {
		target := s.channelSessionByCallsign(requestedCallsign)
		if target == nil || target == sender {
			continue
		}
		if _, duplicate := selected[target]; duplicate {
			continue
		}
		selected[target] = struct{}{}
		_, targetCallsign, targetUserID, _ := target.identity()
		if targetUserID < 1 {
			continue
		}
		invite := InviteNotification{
			SenderCallsign: senderCallsign,
			Message:        request.Message,
			RoomNumber:     request.RoomNumber,
			RoomToken:      request.RoomToken,
			SenderSlot:     request.SenderSlot,
		}
		selectPayload, err := InviteSelectReplyPayload(targetCallsign)
		if err != nil {
			return err
		}
		sendPayload, err := InviteSendReplyPayload(invite)
		if err != nil {
			return err
		}
		if err := sender.Write(ReplyInviteSelect, selectPayload); err != nil {
			s.logger.Debug("invite-select acknowledgement failed", "sender_user_id", senderUserID, "target_user_id", targetUserID, "error", err)
		}
		if err := target.Write(ReplyInviteSend, sendPayload); err != nil {
			s.logger.Debug("invite delivery failed", "sender_user_id", senderUserID, "target_user_id", targetUserID, "error", err)
		}
	}
	return nil
}

func (s *Server) handleInviteSend(sender *Session) error {
	// Reject an unauthenticated standalone delivery request.
	return sender.Write(ReplyInviteSend, InviteSendFailurePayload())
}

func (s *Server) channelSessionByCallsign(callsign string) *Session {
	key := normalizedCallsign(callsign)
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := s.online.byCallsign[key]
	if set == nil {
		return nil
	}
	return set.oldestMatching(func(candidate *Session) bool {
		loginName, candidateCallsign, userID, _ := candidate.identity()
		candidate.mu.RLock()
		inLobby := candidate.RoomNumber == -1
		candidate.mu.RUnlock()
		return inLobby && userID > 0 && loginName != "" && strings.TrimSpace(candidateCallsign) != "" && normalizedCallsign(candidateCallsign) == key
	})
}
