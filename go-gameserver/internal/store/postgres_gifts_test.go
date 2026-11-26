package store

import (
	"testing"
	"time"
)

func TestGiftInboxDaysRemaining(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		expiresAt time.Time
		want      int
	}{
		{name: "new gift", expiresAt: giftExpiresAt(now), want: 90},
		{name: "partial final day", expiresAt: now.Add(time.Minute), want: 1},
		{name: "expired", expiresAt: now, want: 0},
		{name: "past", expiresAt: now.Add(-time.Second), want: 0},
		{name: "clamped malformed future", expiresAt: now.AddDate(1, 0, 0), want: 90},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := giftInboxDaysRemaining(now, test.expiresAt); got != test.want {
				t.Fatalf("giftInboxDaysRemaining() = %d, want %d", got, test.want)
			}
		})
	}
}
