package store

import (
	"testing"
	"time"
)

func TestExtendedInventoryExpiryAccumulatesOwnedTime(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	existing := now.AddDate(0, 0, 30).Format("20060102150405")

	got := extendedInventoryExpiry(now, 7, existing)
	want := now.AddDate(0, 0, 37).Format("20060102150405")
	if got != want {
		t.Fatalf("extended expiry = %q, want %q", got, want)
	}
}

func TestExtendedInventoryExpiryStartsAtNowAfterExpiry(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	expired := now.AddDate(0, 0, -3).Format("2006-01-02 15:04:05")

	got := extendedInventoryExpiry(now, 7, expired)
	want := now.AddDate(0, 0, 7).Format("20060102150405")
	if got != want {
		t.Fatalf("renewed expiry = %q, want %q", got, want)
	}
}

func TestPermanentExpiryIsNotTreatedAsTimedOwnership(t *testing.T) {
	if _, timed := parseTimedInventoryExpiry("permanent"); timed {
		t.Fatal("permanent inventory row was treated as timed")
	}
	if _, timed := parseTimedInventoryExpiry(permanentInventoryExpiry); timed {
		t.Fatal("permanent wire expiry was treated as timed")
	}
}

func TestInventoryItemExpired(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		expiry string
		want   bool
	}{
		{name: "past compact", expiry: "20260721115959", want: true},
		{name: "exact boundary", expiry: "2026-07-21 12:00:00", want: true},
		{name: "future", expiry: "20260721120001", want: false},
		{name: "permanent word", expiry: "permanent", want: false},
		{name: "permanent wire value", expiry: permanentInventoryExpiry, want: false},
		{name: "malformed legacy value", expiry: "unknown", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := inventoryItemExpired(test.expiry, now); got != test.want {
				t.Fatalf("inventoryItemExpired(%q) = %t, want %t", test.expiry, got, test.want)
			}
		})
	}
}
