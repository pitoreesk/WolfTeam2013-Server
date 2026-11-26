package game

import (
	"context"
	"fmt"

	"wtserver-go/internal/store"
)

const (
	channelChatAll byte = iota
	// Type 1 is Pride chat in the field/room lobby and team chat after the game
	// has started.
	channelChatPrideOrTeam
	channelChatDead
	channelChatTeamDead
	channelChatPride
	channelChatShout
	channelChatPowerUser
)

func (s *Server) channelChatRecipients(
	ctx context.Context,
	sender *Session,
	chatType byte,
) ([]*Session, error) {
	if chatType == channelChatShout {
		return s.authenticatedSessions(), nil
	}

	room := s.sessionRoom(sender)
	if room == nil {
		if chatType == channelChatPrideOrTeam || chatType == channelChatPride {
			return s.prideChatRecipients(ctx, sender)
		}
		return s.channelSessions(), nil
	}

	snapshot := room.Snapshot()
	if snapshot.GameStarted &&
		(chatType == channelChatPrideOrTeam || chatType == channelChatTeamDead) {
		return roomTeamChatRecipients(snapshot.Members, sender), nil
	}
	if chatType == channelChatPride ||
		(!snapshot.GameStarted && chatType == channelChatPrideOrTeam) {
		return s.prideChatRecipients(ctx, sender)
	}
	return snapshot.Members, nil
}

func roomTeamChatRecipients(members []*Session, sender *Session) []*Session {
	if sender == nil {
		return nil
	}
	sender.mu.RLock()
	team := sender.RoomTeam
	sender.mu.RUnlock()
	if team != redTeam && team != blueTeam {
		return nil
	}
	recipients := make([]*Session, 0, len(members))
	for _, candidate := range members {
		candidate.mu.RLock()
		candidateTeam := candidate.RoomTeam
		candidate.mu.RUnlock()
		if candidateTeam == team {
			recipients = append(recipients, candidate)
		}
	}
	return recipients
}

func (s *Server) prideChatRecipients(ctx context.Context, sender *Session) ([]*Session, error) {
	prides, ok := s.users.(store.PrideChatStore)
	if !ok || sender == nil {
		return nil, nil
	}
	_, _, userID, _ := sender.identity()
	pride, _, found, err := prides.PrideForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("load Pride chat membership for user %d: %w", userID, err)
	}
	if !found || pride.ID == 0 {
		return nil, nil
	}
	members, err := prides.PrideMembers(ctx, pride.ID)
	if err != nil {
		return nil, fmt.Errorf("load Pride %d chat members: %w", pride.ID, err)
	}
	memberIDs := make(map[int]struct{}, len(members))
	for _, member := range members {
		if member.UserID > 0 {
			memberIDs[member.UserID] = struct{}{}
		}
	}
	recipients := make([]*Session, 0, len(memberIDs))
	for _, candidate := range s.authenticatedSessions() {
		_, _, candidateUserID, _ := candidate.identity()
		if _, member := memberIDs[candidateUserID]; member {
			recipients = append(recipients, candidate)
		}
	}
	return recipients, nil
}
