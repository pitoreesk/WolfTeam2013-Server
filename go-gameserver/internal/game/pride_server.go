package game

import (
	"context"
	"fmt"
	"sort"
	"time"

	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

func (s *Server) handlePrideMessage(ctx context.Context, session *Session, frame transport.Frame) error {
	prides, ok := s.users.(store.PrideStore)
	if !ok {
		return fmt.Errorf("configured user store does not support pride packets")
	}
	_, _, userID, _ := session.identity()
	switch frame.MessageID {
	case RequestPrideCreate:
		request, err := ParsePrideCreateRequest(frame.Payload)
		if err != nil {
			return err
		}
		pride, member, created, err := prides.CreatePride(ctx, userID, request.Name, request.Intro)
		if err != nil {
			s.logBackendFallback("create pride", userID, err)
		}
		success := created && err == nil
		gold := 0
		if success {
			if info, infoErr := s.users.AccountSnapshot(ctx, userID); infoErr == nil {
				gold = info.Gold
			}
		}
		if writeErr := session.Write(ReplyPrideCreate, PrideCreatePayload(pride, member, gold, success)); writeErr != nil {
			return writeErr
		}
		if success {
			s.refreshPrideDisplaysAndRosters(ctx, session)
		}
		return nil
	case RequestPrideClose:
		pride, success, err := prides.SchedulePrideClose(ctx, userID)
		if err != nil {
			s.logBackendFallback("schedule pride closure", userID, err)
		}
		return session.Write(ReplyPrideClose, PrideClosePayload(pride, success && err == nil))
	case RequestPrideList:
		request, err := ParsePrideListRequest(frame.Payload)
		if err != nil {
			return err
		}
		page, err := prides.PrideList(ctx, request.Name, request.Master, int(request.Page), 10)
		if err != nil {
			s.logBackendFallback("list prides", userID, err)
			page = store.PridePage{}
		}
		payload, err := PrideListPayload(request.Page, page)
		if err != nil {
			return err
		}
		return session.Write(ReplyPrideList, payload)
	case RequestPrideInfo:
		prideID, err := ParsePrideIDRequest(frame.Payload)
		if err != nil {
			return err
		}
		pride, found, err := prides.PrideByID(ctx, prideID)
		if err != nil {
			s.logBackendFallback("load pride info", userID, err)
			found = false
		}
		userRole := byte(0)
		if found && pride.MasterUserID == userID {
			userRole = 8
		} else if found {
			if members, err := prides.PrideMembers(ctx, prideID); err == nil {
				for _, m := range members {
					if m.UserID == userID {
						userRole = m.Role
						break
					}
				}
			}
		}
		return session.Write(ReplyPrideInfo, PrideInfoPayload(pride, userRole, found))
	case RequestPrideAsk:
		prideID, err := ParsePrideIDRequest(frame.Payload)
		if err != nil {
			return err
		}
		pride, applied, backendErr := prides.ApplyPride(ctx, userID, prideID)
		if backendErr != nil {
			s.logBackendFallback("apply to pride", userID, backendErr)
		}
		payload := PrideApplicationPayload(pride)
		if applied {
			if applications, listErr := prides.PrideApplications(ctx, pride.MasterUserID); listErr == nil {
				// 0x1739 appends to the master's list, so it carries only the
				// new applicant.
				var added []store.PrideApplication
				for _, application := range applications {
					if application.UserID == userID {
						added = append(added, application)
					}
				}
				if waiting, buildErr := PrideWaitingListPayload(added); buildErr == nil && len(added) != 0 {
					if masterSession := s.sessionForUserID(pride.MasterUserID); masterSession != nil {
						_ = masterSession.Write(ReplyPrideWaitingSync, waiting)
					}
				}
			}
		}
		return session.Write(ReplyPrideAsk, payload)
	case RequestPrideAskCancel:
		prideID, err := ParsePrideIDRequest(frame.Payload)
		if err != nil {
			return err
		}
		pride, _, backendErr := prides.CancelPrideApplication(ctx, userID, prideID)
		if backendErr != nil {
			s.logBackendFallback("cancel pride application", userID, backendErr)
		}
		return session.Write(ReplyPrideAskCancel, PrideApplicationPayload(pride))
	case RequestPrideWaiting:
		applications, err := prides.PrideApplications(ctx, userID)
		if err != nil {
			s.logBackendFallback("load pride applications", userID, err)
			applications = nil
		}
		payload, err := PrideWaitingListPayload(applications)
		if err != nil {
			return err
		}
		return session.Write(ReplyPrideWaiting, payload)
	case RequestPrideAccept:
		request, err := ParsePrideAcceptRequest(frame.Payload)
		if err != nil {
			return err
		}
		var pride store.Pride
		var member store.PrideMember
		var decisionSucceeded bool
		var backendErr error
		if request.Accept {
			pride, member, decisionSucceeded, backendErr = prides.AcceptPrideApplication(ctx, userID, request.Username)
		} else {
			pride, member, decisionSucceeded, backendErr = prides.RejectPrideApplication(ctx, userID, request.Username)
		}
		if backendErr != nil {
			s.logBackendFallback("accept pride application", userID, backendErr)
		}
		if member.Username == "" {
			member.Username, member.Callsign = request.Username, request.Callsign
		}
		accepted := request.Accept && decisionSucceeded && backendErr == nil
		payload := PrideAcceptPayload(pride, member, request.Mode, accepted)
		if err := session.Write(ReplyPrideAccept, payload); err != nil {
			return err
		}
		if decisionSucceeded {
			if target := s.sessionForUserID(member.UserID); target != nil && target != session {
				_ = target.Write(ReplyPrideAccept, payload)
			}
		}
		if accepted {
			s.refreshPrideDisplaysAndRosters(ctx, session, s.sessionForUserID(member.UserID))
		}
		return nil
	case RequestPrideLeave:
		pride, member, success, err := prides.LeavePride(ctx, userID)
		if err != nil {
			s.logBackendFallback("leave pride", userID, err)
		}
		completed := success && err == nil
		if writeErr := session.Write(ReplyPrideLeave, PrideMemberChangePayload(pride, member, completed)); writeErr != nil {
			return writeErr
		}
		if completed {
			s.refreshPrideDisplaysAndRosters(ctx, session)
		}
		return nil
	case RequestPrideMembers:
		pride, _, found, err := prides.PrideForUser(ctx, userID)
		var members []store.PrideMember
		if err == nil && found {
			members, err = prides.PrideMembers(ctx, pride.ID)
		}
		if err != nil {
			s.logBackendFallback("load pride members", userID, err)
			members = nil
		}
		payload, err := PrideMembersPayload(members)
		if err != nil {
			return err
		}
		return session.Write(ReplyPrideMembers, payload)
	case RequestPrideKick:
		username, err := ParsePrideUsernameRequest(frame.Payload)
		if err != nil {
			return err
		}
		pride, member, success, backendErr := prides.KickPrideMember(ctx, userID, username)
		if backendErr != nil {
			s.logBackendFallback("kick pride member", userID, backendErr)
		}
		completed := success && backendErr == nil
		if writeErr := session.Write(ReplyPrideKick, PrideMemberChangePayload(pride, member, completed)); writeErr != nil {
			return writeErr
		}
		if completed {
			s.refreshPrideDisplaysAndRosters(ctx, session, s.sessionForUserID(member.UserID))
		}
		return nil
	case RequestPrideMaster:
		username, err := ParsePrideUsernameRequest(frame.Payload)
		if err != nil {
			return err
		}
		pride, member, success, backendErr := prides.ChangePrideMaster(ctx, userID, username)
		if backendErr != nil {
			s.logBackendFallback("change pride master", userID, backendErr)
		}
		if !success || backendErr != nil {
			return session.Write(ReplyPrideMaster, PrideChangeMasterPayload(pride, member, false))
		}
		// CS_PR_CHANGEMASTER_ACK (section vtable +0x100, sub_341777C0) is the
		// change notice: it carries the new master's callsign, which the
		// client stores as the Pride's master name. A receiver with grade 0
		// (the old master) becomes 8 (member) and a member (8) whose own
		// callsign is the carried one becomes 0 (master); other members keep
		// their grade. Every online member gets it.
		notice := PrideChangeMasterPayload(pride, store.PrideMember{Callsign: member.Callsign}, true)
		if err := session.Write(ReplyPrideMaster, notice); err != nil {
			return err
		}
		affected := []*Session{session}
		for _, other := range s.onlinePrideSessions(pride.ID) {
			if other == session {
				continue
			}
			_ = other.Write(ReplyPrideMaster, notice)
			affected = append(affected, other)
		}
		s.refreshPrideDisplaysAndRosters(ctx, affected...)
		return nil
	case RequestPrideMessages:
		intro, notice, err := ParsePrideMessagesRequest(frame.Payload)
		if err != nil {
			return err
		}
		pride, _, backendErr := prides.SetPrideMessages(ctx, userID, intro, notice)
		if backendErr != nil {
			s.logBackendFallback("update pride messages", userID, backendErr)
		}
		return session.Write(ReplyPrideMessages, PrideMessagesPayload(pride))
	case RequestPrideMemo:
		prideID, err := ParsePrideIDRequest(frame.Payload)
		if err != nil {
			return err
		}
		memos, backendErr := prides.PrideMemos(ctx, prideID)
		if backendErr != nil {
			s.logBackendFallback("load pride memos", userID, backendErr)
			memos = nil
		}
		payload, err := PrideMemosPayload(prideID, memos)
		if err != nil {
			return err
		}
		if err := session.Write(ReplyPrideMemo, PrideMemoStatusPayload()); err != nil {
			return err
		}
		return session.Write(ReplyPrideMemoSync, payload)
	case RequestPrideMemoAdd:
		request, err := ParsePrideMemoRequest(frame.Payload)
		if err != nil {
			return err
		}
		memos, _, backendErr := prides.AddPrideMemo(ctx, userID, request.PrideID, request.Message)
		if backendErr != nil {
			s.logBackendFallback("add pride memo", userID, backendErr)
			memos = nil
		}
		payload, err := PrideMemosPayload(request.PrideID, memos)
		if err != nil {
			return err
		}
		if err := session.Write(ReplyPrideMemoAdd, payload); err != nil {
			return err
		}
		s.broadcastPrideMembers(ctx, prides, request.PrideID, session, ReplyPrideMemoSync, payload)
		return nil
	case RequestPrideMemoDelete:
		prideID, memoID, err := ParsePrideMemoDeleteRequest(frame.Payload)
		if err != nil {
			return err
		}
		deleted, backendErr := prides.DeletePrideMemo(ctx, userID, prideID, memoID)
		if backendErr != nil {
			s.logBackendFallback("delete pride memo", userID, backendErr)
		}
		memos, loadErr := prides.PrideMemos(ctx, prideID)
		if loadErr != nil {
			s.logBackendFallback("reload pride memos after delete", userID, loadErr)
			memos = nil
		}
		payload, buildErr := PrideMemoDeletePayload(prideID, memos)
		if buildErr != nil {
			return buildErr
		}
		if err := session.Write(ReplyPrideMemoDelete, payload); err != nil {
			return err
		}
		if deleted {
			s.broadcastPrideMembers(ctx, prides, prideID, session, ReplyPrideMemoSync, payload)
		}
		return nil
	case RequestPrideFullInfo:
		request, err := ParsePrideInfoRequest(frame.Payload)
		if err != nil {
			return err
		}
		pride, found, backendErr := prides.PrideByID(ctx, request.ID)
		var members []store.PrideMember
		if backendErr == nil && found {
			members, backendErr = prides.PrideMembers(ctx, request.ID)
		}
		if backendErr != nil {
			s.logBackendFallback("load full pride info", userID, backendErr)
			found = false
		}
		userRole := byte(0)
		if found && pride.MasterUserID == userID {
			userRole = 8
		} else if found {
			for _, m := range members {
				if m.UserID == userID {
					userRole = m.Role
					break
				}
			}
		}
		return session.Write(ReplyPrideFullInfo, PrideDetailedInfoPayload(request, pride, members, userRole, found))
	case RequestPrideCloseCancel:
		pride, success, err := prides.CancelPrideClose(ctx, userID)
		if err != nil {
			s.logBackendFallback("cancel pride closure", userID, err)
		}
		status := byte(0)
		if success {
			status = pride.CloseStatus
		}
		return session.Write(ReplyPrideCloseCancel, PrideCloseCancelPayload(status))
	case RequestPrideResult:
		prideID, err := ParsePrideIDRequest(frame.Payload)
		if err != nil {
			return err
		}
		// If prideID==0 the client wants our own pride's summary
		if prideID == 0 {
			pride, _, found, _ := prides.PrideForUser(ctx, userID)
			if found {
				prideID = pride.ID
			}
		}
		summary, backendErr := prides.PrideBattleResult(ctx, prideID)
		if backendErr != nil {
			s.logBackendFallback("load pride battle result", userID, backendErr)
		}
		return session.Write(ReplyPrideResult, PrideBattleResultPayload(summary))
	case RequestPrideBattleList:
		req, err := ParsePrideBattleListRequest(frame.Payload)
		if err != nil {
			return err
		}
		prideID := req.PrideID
		if prideID == 0 {
			pride, _, found, _ := prides.PrideForUser(ctx, userID)
			if found {
				prideID = pride.ID
			}
		}
		page := int(req.Arg2)
		isClassic := req.Arg1 == 1
		entries, total, backendErr := prides.PrideBattleHistory(ctx, prideID, page, isClassic)
		if backendErr != nil {
			s.logBackendFallback("load pride battle list", userID, backendErr)
			entries = nil
			total = 0
		}
		return session.Write(ReplyPrideBattleList, PrideBattleListPayload(entries, total))

	case RequestPrideMemberIDs:
		usernames, err := prides.PrideMemberUsernames(ctx, userID)
		if err != nil {
			s.logBackendFallback("load pride member IDs", userID, err)
			usernames = nil
		}
		return session.Write(ReplyPrideMemberIDs, PrideMemberIDsPayload(usernames))
	case RequestMyPrideInfo:
		contribution := 0
		if _, member, found, err := prides.PrideForUser(ctx, userID); err == nil && found {
			contribution = member.Contribution
		}
		return session.Write(ReplyMyPrideInfo, MyPrideInfoPayload(contribution))
	case RequestMyPrideChain:
		chain, position := s.prideChain(session)
		if position == 0 {
			return session.Write(ReplyMyPrideChain, MyPrideChainPayload(0, 0))
		}
		level := prideChainLevel(position, len(chain))
		return session.Write(ReplyMyPrideChain, MyPrideChainPayload(prideChainLevelCode(level), byte(len(chain))))
	case RequestPrideChainList:
		chain, _ := s.prideChain(session)
		return session.Write(ReplyPrideChainList, PrideChainListPayload(chain))
	case RequestPrideRecentRank:
		prideID, err := ParsePrideIDRequest(frame.Payload)
		if err != nil {
			return err
		}
		var ranks []int
		if recent, ok := s.users.(interface {
			PrideRecentRanks(context.Context, uint32, int) ([]int, error)
		}); ok {
			loaded, err := recent.PrideRecentRanks(ctx, prideID, 7)
			if err != nil {
				s.logBackendFallback("load pride recent ranks", userID, err)
			} else {
				ranks = loaded
			}
		}
		return session.Write(ReplyPrideRecentRank, PrideRecentRankPayload(prideID, ranks))
	case RequestPrideChanged, RequestPrideChangedAck:
		// 2012 CS_PR_GETCHANGEDINFO_REQ (sub_34108B10) has no reply handler
		// (no 0x1731 case); the server would push pending change notices,
		// which the client confirms with CS_PR_ACKCHANGEDINFO_REQ (0x1732,
		// no 0x1733 handler either). No notice queue exists, so nothing is sent.
		return nil
	default:
		return fmt.Errorf("unsupported pride message 0x%04x", frame.MessageID)
	}
}

func (s *Server) sessionForUserID(userID int) *Session {
	if userID < 1 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if set := s.online.byUserID[userID]; set != nil {
		return set.oldest()
	}
	if session := s.online.connections.oldestMatching(func(session *Session) bool {
		_, _, candidate, _ := session.identity()
		return candidate == userID
	}); session != nil {
		return session
	}
	return nil
}

func (s *Server) broadcastPrideMembers(ctx context.Context, prides store.PrideStore, prideID uint32, exclude *Session, messageID uint16, payload []byte) {
	members, err := prides.PrideMembers(ctx, prideID)
	if err != nil {
		return
	}
	for _, member := range members {
		if target := s.sessionForUserID(member.UserID); target != nil && target != exclude {
			_ = target.Write(messageID, payload)
		}
	}
}

func (s *Server) runPrideProcessor(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			prides, ok := s.users.(store.PrideStore)
			if !ok {
				return
			}
			maintenanceCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			closures, err := prides.CompletePrideClosures(maintenanceCtx, now)
			cancel()
			if err != nil {
				s.logger.Warn("complete due pride closures", "error", err)
				continue
			}
			for _, closure := range closures {
				payload := PrideCloseCompletePayload(closure)
				liveMembers := make([]*Session, 0, len(closure.MemberUserIDs))
				for _, userID := range closure.MemberUserIDs {
					if target := s.sessionForUserID(userID); target != nil {
						liveMembers = append(liveMembers, target)
						_ = target.Write(ReplyPrideCloseComplete, payload)
					}
				}
				s.refreshPrideDisplaysAndRosters(ctx, liveMembers...)
			}
		case <-ctx.Done():
			return
		}
	}
}

// prideChain lists the online members of the session's Pride on this server
// in login order (the Pride chain) and the session's 1-based position (0
// without a Pride).
func (s *Server) prideChain(session *Session) ([]PrideChainEntry, int) {
	_, prideID := session.prideDisplay()
	if prideID == 0 {
		return nil, 0
	}
	type member struct {
		session  *Session
		callsign string
		order    uint64
		userID   int
	}
	var members []member
	s.mu.RLock()
	for other := range s.sessions {
		if other.IsBot {
			continue
		}
		_, callsign, userID, _ := other.identity()
		if _, otherPride := other.prideDisplay(); userID < 1 || otherPride != prideID {
			continue
		}
		members = append(members, member{session: other, callsign: callsign, order: other.getLoginOrder(), userID: userID})
	}
	s.mu.RUnlock()
	sort.Slice(members, func(i, j int) bool {
		if members[i].order != members[j].order {
			return members[i].order < members[j].order
		}
		return members[i].userID < members[j].userID
	})
	if len(members) > prideChainMaxMembers {
		members = members[:prideChainMaxMembers]
	}
	chain := make([]PrideChainEntry, len(members))
	position := 0
	for index, m := range members {
		chain[index] = PrideChainEntry{Callsign: m.callsign, Order: uint32(index + 1)}
		if m.session == session {
			position = index + 1
		}
	}
	return chain, position
}

// onlinePrideSessions returns the logged-in sessions of a Pride's members.
func (s *Server) onlinePrideSessions(prideID uint32) []*Session {
	if prideID == 0 {
		return nil
	}
	var sessions []*Session
	s.mu.RLock()
	for session := range s.sessions {
		if session.IsBot {
			continue
		}
		if _, _, userID, _ := session.identity(); userID < 1 {
			continue
		}
		if _, id := session.prideDisplay(); id == prideID {
			sessions = append(sessions, session)
		}
	}
	s.mu.RUnlock()
	return sessions
}
