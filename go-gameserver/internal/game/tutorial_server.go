package game

import (
	"context"

	"wtserver-go/internal/store"
)

// writeTutorialProgress sends CS_CH_TUTORIALCLEAR_LEVEL_ACK (0x1345) with the
// stored human/wolf chapter levels.
func (s *Server) writeTutorialProgress(ctx context.Context, session *Session) error {
	_, _, userID, _ := session.identity()
	progress, err := s.users.TutorialProgress(ctx, userID)
	if err != nil {
		s.logBackendFallback("load tutorial progress", userID, err)
		progress = store.TutorialProgress{}
	}
	return session.Write(ReplyTutorialClear, TutorialClearPayload(progress))
}
