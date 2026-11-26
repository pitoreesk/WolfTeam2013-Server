package game

import (
	"context"
	"errors"

	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

// vipStore returns the persistent VIP Lounge backend, if the repository has one.
func (s *Server) vipStore() (store.VIPLoungeStore, bool) {
	vip, ok := s.users.(store.VIPLoungeStore)
	return vip, ok
}

// vipLounges loads the lounge configuration for EVENTSHOPINFO; without a
// backend every lounge stays hidden.
func (s *Server) vipLounges(ctx context.Context) []store.VIPLounge {
	vip, ok := s.vipStore()
	if !ok {
		return nil
	}
	lounges, err := vip.VIPLounges(ctx)
	if err != nil {
		s.logger.Warn("load VIP lounges", "error", err)
		return nil
	}
	return lounges
}

func (s *Server) enabledVIPPeriods(ctx context.Context, lounge byte) []byte {
	for _, config := range s.vipLounges(ctx) {
		if config.Lounge == lounge && config.Visible {
			return config.Periods
		}
	}
	return nil
}

func (s *Server) handleVIPLounge(ctx context.Context, session *Session, frame transport.Frame) error {
	_, _, userID, _ := session.identity()
	vip, ok := s.vipStore()
	lounge := byte(0)
	if len(frame.Payload) > 0 {
		lounge = frame.Payload[0]
	}
	switch frame.MessageID {
	case RequestVIPTicketCount:
		counts := map[byte]uint16{}
		if ok {
			loaded, err := vip.VIPTicketCounts(ctx, userID, lounge)
			if err != nil {
				s.logBackendFallback("load VIP tickets", userID, err)
			} else {
				counts = loaded
			}
		}
		return session.Write(ReplyVIPTicketCount, VIPTicketCountPayload(lounge, s.enabledVIPPeriods(ctx, lounge), counts))
	case RequestVIPItemList:
		period := byte(0)
		if len(frame.Payload) > 1 {
			period = frame.Payload[1]
		}
		// Always answer with five rows: a missing 0x1445 blocks every tab
		// click (the client's request-pending flag stays set).
		var page store.VIPPage
		if ok {
			loaded, err := vip.VIPPage(ctx, userID, lounge, period)
			if err != nil {
				s.logBackendFallback("load VIP page", userID, err)
			} else {
				page = loaded
			}
		}
		return session.Write(ReplyVIPItemList, VIPShopItemsPayload(lounge, period, page))
	case RequestVIPRankList:
		var entries []store.VIPRankEntry
		if ok {
			loaded, err := vip.VIPRankList(ctx, userID, lounge)
			if err != nil {
				s.logBackendFallback("load VIP rank list", userID, err)
			} else {
				entries = loaded
			}
		}
		return session.Write(ReplyVIPRankList, VIPRankListPayload(lounge, entries))
	case RequestVIPBuy:
		request, err := ParseVIPBuyRequest(frame.Payload)
		if err != nil || !ok {
			return session.Write(ReplyVIPBuy, VIPBuyFailedPayload())
		}
		tickets, items, err := vip.RedeemVIPTicket(ctx, userID, request.Lounge, request.Period, request.Tier, request.Items)
		if err != nil {
			if !errors.Is(err, store.ErrVIPRedeemRejected) {
				s.logBackendFallback("redeem VIP ticket", userID, err)
			}
			return session.Write(ReplyVIPBuy, VIPBuyFailedPayload())
		}
		session.invalidateRosterState()
		s.logger.Info("VIP Lounge ticket redeemed", "user_id", userID, "lounge", request.Lounge,
			"period", request.Period, "tier", request.Tier, "items", len(items))
		return session.Write(ReplyVIPBuy, VIPBuyReplyPayload(request, tickets, items))
	}
	return nil
}

// recordVIPSpend credits a completed purchase to the currency's VIP lounge and
// pushes the ticket banner (0x1467) when a tier ticket was granted.
func (s *Server) recordVIPSpend(ctx context.Context, session *Session, currency, amount int) {
	vip, ok := s.vipStore()
	if !ok || amount <= 0 {
		return
	}
	_, _, userID, _ := session.identity()
	granted, err := vip.RecordVIPSpend(ctx, userID, currency, amount)
	if err != nil {
		s.logBackendFallback("record VIP spend", userID, err)
		return
	}
	for _, lounge := range granted {
		s.logger.Info("VIP Lounge ticket granted", "user_id", userID, "lounge", lounge)
		_ = session.Write(ReplyVIPTicketNotify, VIPTicketNotifyPayload(lounge))
	}
}
