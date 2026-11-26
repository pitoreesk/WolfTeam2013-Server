package game

import (
	"context"

	"wtserver-go/internal/store"
)

func (s *Server) handleCountedItemUse(ctx context.Context, session *Session, payload []byte) error {
	uniqueIDs, err := ParseUseCountItemRequest(payload)
	if err != nil {
		reply, _ := UseCountItemReplyPayload(1, nil)
		return session.Write(ReplyUseCountItem, reply)
	}
	countedItems, ok := s.users.(store.CountedItemStore)
	if !ok {
		reply, _ := UseCountItemReplyPayload(1, nil)
		return session.Write(ReplyUseCountItem, reply)
	}
	_, _, userID, _ := session.identity()
	results, err := countedItems.ConsumeCountedItems(ctx, userID, uniqueIDs)
	if err != nil {
		s.logBackendFallback("consume in-game counted items", userID, err)
		reply, _ := UseCountItemReplyPayload(1, nil)
		return session.Write(ReplyUseCountItem, reply)
	}
	session.invalidateRosterState()
	reply, err := UseCountItemReplyPayload(0, results)
	if err != nil {
		return err
	}
	return session.Write(ReplyUseCountItem, reply)
}
