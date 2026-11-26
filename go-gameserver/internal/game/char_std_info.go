package game

import (
	"context"
	"strings"

	"wtserver-go/internal/store"
)

// handleCharStdInfo answers 0x1324, the other-player information panel
// (lobby user list, buddy list, room roster). The target must be online.
func (s *Server) handleCharStdInfo(ctx context.Context, session *Session, payload []byte) error {
	request, err := ParseCharStdInfoRequest(payload)
	if err != nil {
		return nil
	}
	target := s.sessionByCallsign(request.Callsign)
	if target == nil {
		s.logger.Info("char-std-info target offline", "callsign", request.Callsign)
		return nil
	}
	login, callsign, userID, level := target.identity()
	if userID < 1 || request.LoginName != "" && !strings.EqualFold(login, request.LoginName) {
		return nil
	}
	info, err := s.users.AccountSnapshot(ctx, userID)
	if err != nil {
		s.logBackendFallback("load char-std-info", userID, err)
		info = store.AccountSnapshot{Callsign: callsign, Level: int(level)}
	}
	schoolID := uint32(0)
	if accounts, ok := s.users.(store.ChannelAccountStore); ok {
		if state, err := accounts.ChannelAccountState(ctx, userID); err == nil {
			schoolID = state.SchoolID
		}
	}
	var pride store.Pride
	var member store.PrideMember
	if prides, ok := s.users.(store.PrideStore); ok {
		if loaded, loadedMember, found, err := prides.PrideForUser(ctx, userID); err == nil && found {
			pride, member = loaded, loadedMember
		}
	}
	var crests []store.CrestDelegateSlot
	if crestStore, ok := s.crestStore(); ok {
		if state, err := crestStore.CrestState(ctx, userID); err == nil {
			crests = rosterCrests(state.Delegates)
		}
	}
	reply, err := CharStdInfoPayload(callsign, info, pride, member, schoolID, crests)
	if err != nil {
		return err
	}
	return session.Write(ReplyCharStdInfo, reply)
}
