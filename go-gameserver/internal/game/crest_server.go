package game

import (
	"context"
	"errors"

	"wtserver-go/internal/crestdata"
	"wtserver-go/internal/store"
)

// crestStore returns the crest backend, if the repository has one.
func (s *Server) crestStore() (store.CrestStore, bool) {
	crests, ok := s.users.(store.CrestStore)
	return crests, ok && s.crests != nil
}

// crestEntries builds the 0x134D list: every completed crest (percent 100)
// and every active challenge with its counter. Untouched crests are omitted.
func (s *Server) crestEntries(state store.CrestState) []CrestUserEntry {
	active := make(map[uint16]store.CrestActiveChallenge, len(state.Active))
	for _, challenge := range state.Active {
		active[challenge.CrestID] = challenge
	}
	var entries []CrestUserEntry
	for _, crest := range s.crests.All() {
		challenge, running := active[crest.ID]
		entry := CrestUserEntry{CrestID: crest.ID, Active: running, Progress: challenge.Progress}
		if state.Completed[crest.ID] {
			entry.Percent = 100
		}
		if entry.Percent != 0 || entry.Active {
			entries = append(entries, entry)
		}
	}
	return entries
}

// handleCrestUser answers 0x134C (sent during login, before the lobby).
func (s *Server) handleCrestUser(ctx context.Context, session *Session) error {
	_, _, userID, _ := session.identity()
	var state store.CrestState
	if crests, ok := s.crestStore(); ok {
		loaded, err := crests.CrestState(ctx, userID)
		if err != nil {
			s.logBackendFallback("load crest state", userID, err)
		}
		state = loaded
	}
	payload, err := CrestUserPayload(state.Delegates, s.crestEntries(state))
	if err != nil {
		return err
	}
	return session.Write(ReplyCrestUser, payload)
}

// handleCrestEquip answers 0x134E with the whole delegate list (0x134F). Only
// completed crests can be worn; crest 0 empties the slot.
func (s *Server) handleCrestEquip(ctx context.Context, session *Session, payload []byte) error {
	slot, crestID, err := ParseCrestEquipRequest(payload)
	if err != nil {
		return nil
	}
	_, _, userID, _ := session.identity()
	crests, ok := s.crestStore()
	if !ok {
		return session.Write(ReplyCrestEquip, CrestEquipPayload(nil))
	}
	var slots []store.CrestDelegateSlot
	if slot <= 2 {
		slots, err = crests.SetCrestDelegateSlot(ctx, userID, slot, crestID)
	} else {
		var state store.CrestState
		state, err = crests.CrestState(ctx, userID)
		slots = state.Delegates
	}
	if err != nil {
		s.logBackendFallback("save crest delegate slot", userID, err)
	}
	session.invalidateRosterState()
	s.logger.Info("crest delegate set", "user_id", userID, "slot", slot, "crest", crestID, "slots", slots)
	return session.Write(ReplyCrestEquip, CrestEquipPayload(slots))
}

// handleCrestChallenge answers 0x1352: buys the listed challenges (Gold,
// CrestData cost), at most three active and one per group. 0x1353 carries the
// Gold balance and only the started crests.
func (s *Server) handleCrestChallenge(ctx context.Context, session *Session, payload []byte) error {
	requested, err := ParseCrestChallengeRequest(payload)
	if err != nil {
		return nil
	}
	_, _, userID, _ := session.identity()
	info, err := s.users.AccountSnapshot(ctx, userID)
	if err != nil {
		s.logBackendFallback("load Gold for crest challenge", userID, err)
	}
	gold := uint32(max(info.Gold, 0))
	crests, ok := s.crestStore()
	if !ok {
		return session.Write(ReplyCrestChallenge, CrestChallengePayload(gold, nil))
	}
	state, err := crests.CrestState(ctx, userID)
	if err != nil {
		s.logBackendFallback("load crest state", userID, err)
		return session.Write(ReplyCrestChallenge, CrestChallengePayload(gold, nil))
	}
	groups := map[uint16]bool{}
	for _, challenge := range state.Active {
		groups[challenge.Group] = true
	}
	var starts []store.CrestChallengeStart
	var started []uint16
	cost := uint32(0)
	for _, crestID := range requested {
		crest, found := s.crests.Lookup(crestID)
		if !found || state.Completed[crestID] || groups[crest.Group] || len(state.Active)+len(starts) >= 3 {
			continue
		}
		// Sneak kills are a bit of the peer death message that is never
		// reported to the server (0x1634 carries only killer, victim and three
		// flags), so that challenge could never finish.
		if crest.Group == crestdata.GroupSneakKill {
			continue
		}
		groups[crest.Group] = true
		starts = append(starts, store.CrestChallengeStart{CrestID: crestID, Group: crest.Group})
		started = append(started, crestID)
		cost += crest.Cost
	}
	if len(starts) != 0 {
		remaining, err := crests.StartCrestChallenges(ctx, userID, starts, cost)
		switch {
		case err == nil:
			gold = remaining
			s.logger.Info("crest challenge started", "user_id", userID, "crests", started, "cost", cost, "gold", gold)
		case errors.Is(err, store.ErrInsufficientCrestGold), errors.Is(err, store.ErrCrestChallengeConflict):
			s.logger.Info("crest challenge refused", "user_id", userID, "crests", started, "cost", cost, "error", err)
			started = nil
		default:
			s.logBackendFallback("start crest challenges", userID, err)
			started = nil
		}
	}
	return session.Write(ReplyCrestChallenge, CrestChallengePayload(gold, started))
}

// handleCrestCancel answers 0x1358: drops an active challenge (no refund).
func (s *Server) handleCrestCancel(ctx context.Context, session *Session, payload []byte) error {
	crestID, err := ParseCrestCancelRequest(payload)
	if err != nil {
		return nil
	}
	_, _, userID, _ := session.identity()
	if crests, ok := s.crestStore(); ok {
		cancelled, err := crests.CancelCrestChallenge(ctx, userID, crestID)
		if err != nil {
			s.logBackendFallback("cancel crest challenge", userID, err)
		}
		s.logger.Info("crest challenge cancelled", "user_id", userID, "crest", crestID, "cancelled", cancelled)
	}
	return session.Write(ReplyCrestCancel, CrestCancelPayload(crestID))
}

// crestSinkPayload builds 0x1699 from the active challenge counters.
func (s *Server) crestSinkPayload(ctx context.Context, userID int) ([]byte, error) {
	var active []store.CrestActiveChallenge
	if crests, ok := s.crestStore(); ok {
		state, err := crests.CrestState(ctx, userID)
		if err != nil {
			s.logBackendFallback("load crest counters", userID, err)
		}
		active = state.Active
	}
	return CrestSinkPayload(active)
}

// awardCrestEvent counts one gameplay event for the player's challenge of
// that group. The client never counts itself (its hook sub_3407DB90 only asks
// with 0x1698), so the server pushes the new counters (0x1699) and, when the
// target is reached, the completion (0x145F); both are handled by the
// manager dispatcher (sub_34136C10) in every section.
func (s *Server) awardCrestEvent(ctx context.Context, session *Session, group uint16) {
	if session == nil || session.IsBot {
		return
	}
	crests, ok := s.crestStore()
	if !ok {
		return
	}
	_, _, userID, _ := session.identity()
	if userID < 1 {
		return
	}
	credit, err := crests.CreditCrestGroup(ctx, userID, group, 1, func(crestID uint16) (uint32, uint32, bool) {
		crest, found := s.crests.Lookup(crestID)
		return crest.Target, crest.RewardWC, found
	})
	if err != nil {
		s.logBackendFallback("count crest event", userID, err)
		return
	}
	if !credit.Moved {
		return
	}
	s.logger.Info("crest challenge progress", "user_id", userID, "crest", credit.CrestID, "group", group,
		"progress", credit.Progress, "completed", credit.Completed)
	if credit.Completed {
		session.invalidateRosterState()
		if err := session.Write(ReplyCrestReward, CrestRewardPayload(credit.WC, credit.CrestID)); err != nil {
			return
		}
	}
	if payload, err := s.crestSinkPayload(ctx, userID); err == nil {
		_ = session.Write(ReplyCrestSink, payload)
	}
}

// awardWolfKingCrests credits the players a Wolf Hunt round just picked as
// Kings. Call only when PrepareSpecialRound reports a new selection.
func (s *Server) awardWolfKingCrests(ctx context.Context, room *Room, state SpecialRoundState) {
	if room == nil || !state.WolfHunt || room.UsesAIDeathmatchRules() {
		return
	}
	for _, slot := range state.Kings {
		s.awardCrestEvent(ctx, room.SessionAt(slot), crestdata.GroupWolfKing)
	}
}

// rosterCrests turns the worn delegate slots into the roster tail entries.
func rosterCrests(slots []store.CrestDelegateSlot) []store.CrestDelegateSlot {
	var crests []store.CrestDelegateSlot
	for _, slot := range slots {
		if slot.Slot <= 2 && slot.CrestID != 0 && len(crests) < 3 {
			crests = append(crests, slot)
		}
	}
	return crests
}
