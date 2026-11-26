package game

import (
	"context"
	"fmt"
	"math"

	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

func (s *Server) handleChannelAccountMessage(ctx context.Context, session *Session, frame transport.Frame) error {
	accounts, ok := s.users.(store.ChannelAccountStore)
	if !ok {
		return fmt.Errorf("configured user store does not support channel-account packets")
	}
	_, callsign, userID, _ := session.identity()
	if userID < 1 {
		return fmt.Errorf("channel-account request before authentication")
	}
	switch frame.MessageID {
	case RequestChangeNickname:
		requested, err := ParseChangeNicknameRequest(frame.Payload)
		if err != nil {
			return err
		}
		effective, changed, backendErr := accounts.ChangeCallsign(ctx, userID, requested)
		if backendErr != nil {
			s.logBackendFallback("change nickname", userID, backendErr)
			effective = callsign
		}
		if effective == "" {
			effective = callsign
		}
		if changed && backendErr == nil {
			s.setSessionCallsign(session, effective)
		}
		payload, err := ChangeNicknamePayload(effective)
		if err != nil {
			return err
		}
		if err := session.Write(ReplyChangeNickname, payload); err != nil {
			return err
		}
		if changed && backendErr == nil {
			s.broadcastChannelRoster()
		}
		return nil
	case RequestCheckVersion:
		payload, err := CheckVersionPayload(s.gameVersion)
		if err != nil {
			return err
		}
		return session.Write(ReplyCheckVersion, payload)
	case RequestChangeSchool:
		schoolID, err := ParseChangeSchoolRequest(frame.Payload)
		if err != nil {
			return err
		}
		state, changed, backendErr := accounts.ChangeSchool(ctx, userID, schoolID)
		if backendErr != nil {
			s.logBackendFallback("change school", userID, backendErr)
			if current, currentErr := accounts.ChannelAccountState(ctx, userID); currentErr == nil {
				state = current
			}
		}
		if !changed || backendErr != nil {
			state.SchoolID = math.MaxUint32
		}
		info, infoErr := s.users.AccountSnapshot(ctx, userID)
		if infoErr != nil {
			s.logBackendFallback("load Gold after school change", userID, infoErr)
		}
		return session.Write(ReplyChangeSchool, ChangeSchoolPayload(state.SchoolID, nonNegativeU32(info.Gold)))
	case RequestWolfPoint:
		state, err := accounts.ChannelAccountState(ctx, userID)
		if err != nil {
			s.logBackendFallback("load wolf points", userID, err)
			state = store.ChannelAccountState{}
		}
		return session.Write(ReplyWolfPoint, WolfPointPayload(state.WolfPoints))
	case RequestEventList:
		return session.Write(ReplyEventList, EventListPayload())
	case RequestAskRecommend:
		targetCallsign, err := ParseAskRecommendationRequest(frame.Payload)
		if err != nil {
			return err
		}
		targetID, requester, target, success, backendErr := accounts.AskRecommendation(ctx, userID, targetCallsign)
		if backendErr != nil {
			s.logBackendFallback("ask recommendation", userID, backendErr)
		}
		senderPayload, err := AskRecommendationPayload(success && backendErr == nil, target)
		if err != nil {
			return err
		}
		if err := session.Write(ReplyAskRecommend, senderPayload); err != nil {
			return err
		}
		if success && backendErr == nil {
			incomingPayload, buildErr := AskRecommendationPayload(true, requester)
			if buildErr != nil {
				return buildErr
			}
			if targetSession := s.sessionForUserID(targetID); targetSession != nil && targetSession != session {
				_ = targetSession.Write(ReplyAskRecommend, incomingPayload)
			}
		}
		return nil
	case RequestAnswerRecommend:
		request, err := ParseAnswerRecommendationRequest(frame.Payload)
		if err != nil {
			return err
		}
		requesterID, requester, responder, success, backendErr := accounts.AnswerRecommendation(ctx, userID, request.Callsign, request.Accept)
		if backendErr != nil {
			s.logBackendFallback("answer recommendation", userID, backendErr)
		}
		if !success || backendErr != nil || !request.Accept {
			return nil
		}
		ownPayload, err := AnswerRecommendationPayload(requester)
		if err != nil {
			return err
		}
		if err := session.Write(ReplyAnswerRecommend, ownPayload); err != nil {
			return err
		}
		requesterPayload, err := AnswerRecommendationPayload(responder)
		if err != nil {
			return err
		}
		if requesterSession := s.sessionForUserID(requesterID); requesterSession != nil && requesterSession != session {
			_ = requesterSession.Write(ReplyAnswerRecommend, requesterPayload)
		}
		return nil
	default:
		return fmt.Errorf("unsupported channel-account packet 0x%04x", frame.MessageID)
	}
}

func (s *Server) sendPendingRecommendations(ctx context.Context, session *Session, userID int) error {
	accounts, ok := s.users.(store.ChannelAccountStore)
	if !ok || userID < 1 {
		return nil
	}
	users, err := accounts.PendingRecommendations(ctx, userID)
	if err != nil {
		s.logBackendFallback("load recommendations", userID, err)
		users = nil
	}
	payload, err := RecommendationListPayload(users)
	if err != nil {
		return err
	}
	return session.Write(ReplyRecommendList, payload)
}
