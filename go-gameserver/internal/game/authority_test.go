package game

import "testing"

func TestStaffAuthorityMatchesProtocolPredicate(t *testing.T) {
	for authority := 0; authority <= 255; authority++ {
		got := hasStaffAuthority(byte(authority))
		want := authority == 10 || authority == 12 || authority == 13
		if got != want {
			t.Fatalf("hasStaffAuthority(%d) = %v, want %v", authority, got, want)
		}
	}
}

func TestAuthorityRoomBypasses(t *testing.T) {
	for _, authority := range []byte{10, 12, 13} {
		if !authorityBypassesRoomClassLimits(authority) {
			t.Errorf("authority %d did not bypass room class limits", authority)
		}
	}
	if authorityBypassesRoomClassLimits(0) || authorityBypassesRoomClassLimits(16) {
		t.Fatal("ordinary or AI authority bypassed room class limits")
	}
	if !authorityBypassesRoomPassword(10) {
		t.Fatal("authority 10 did not bypass room password")
	}
	if authorityBypassesRoomPassword(12) || authorityBypassesRoomPassword(13) {
		t.Fatal("authority 12 or 13 bypassed room password")
	}
}
