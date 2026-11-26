package store

import (
	"context"
	"fmt"
	"strings"
)

// BuddyIdentity is the account/display-name pair returned by a user-ID lookup.
type BuddyIdentity struct {
	Username string
	Callsign string
}

// BuddyLookupStore is optional so lightweight and test UserStore
// implementations do not need to provide social lookup persistence.
type BuddyLookupStore interface {
	BuddyIdentityByCallsign(context.Context, string) (BuddyIdentity, bool, error)
}

func (s *PostgresStore) BuddyIdentityByCallsign(ctx context.Context, callsign string) (BuddyIdentity, bool, error) {
	callsign = strings.TrimSpace(callsign)
	if callsign == "" {
		return BuddyIdentity{}, false, nil
	}

	var identity BuddyIdentity
	err := s.pool.QueryRow(ctx, `
		SELECT username, callsign
		FROM users
		WHERE lower(callsign) = lower($1)`, callsign).Scan(&identity.Username, &identity.Callsign)
	if noRows(err) {
		return BuddyIdentity{}, false, nil
	}
	if err != nil {
		return BuddyIdentity{}, false, fmt.Errorf("look up buddy callsign %q: %w", callsign, err)
	}
	return identity, true, nil
}
