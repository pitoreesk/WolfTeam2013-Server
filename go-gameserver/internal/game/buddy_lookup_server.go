package game

import (
	"context"
	"fmt"
	"strings"

	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

func (s *Server) handleGetUserID(ctx context.Context, session *Session, frame transport.Frame) error {
	_, _, userID, _ := session.identity()
	if userID < 1 {
		return fmt.Errorf("get-user-id request before authentication")
	}

	requested, err := ParseGetUserIDRequest(frame.Payload)
	if err != nil {
		return err
	}

	identity, found := store.BuddyIdentity{}, false
	if lookups, ok := s.users.(store.BuddyLookupStore); ok {
		identity, found, err = lookups.BuddyIdentityByCallsign(ctx, requested)
		if err != nil {
			s.logBackendFallback("look up buddy callsign", userID, err)
			identity, found = store.BuddyIdentity{}, false
		}
	}
	if !found {
		identity, found = s.onlineBuddyIdentity(requested)
	}
	if !found {
		identity = store.BuddyIdentity{}
	}

	payload, err := GetUserIDPayload(identity.Callsign, identity.Username)
	if err != nil {
		return err
	}
	s.logger.Debug("game user ID lookup", "requested_callsign", requested, "found", found, "username", identity.Username)
	return session.Write(ReplyGetUserID, payload)
}

func (s *Server) onlineBuddyIdentity(callsign string) (store.BuddyIdentity, bool) {
	for _, candidate := range s.channelSessions() {
		username, candidateCallsign, userID, _ := candidate.identity()
		if userID > 0 && strings.EqualFold(candidateCallsign, callsign) {
			return store.BuddyIdentity{Username: username, Callsign: candidateCallsign}, true
		}
	}
	return store.BuddyIdentity{}, false
}
