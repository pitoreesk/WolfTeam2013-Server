package game

const (
	authorityObserverGM = byte(10)
	authorityStaffGM    = byte(12)
	authorityStaffGMAlt = byte(13)
)

// hasStaffAuthority contains the account-authority values assigned to the
// shared GM/staff group. Authority 16 belongs to AI room members.
func hasStaffAuthority(authority byte) bool {
	return authority == authorityObserverGM ||
		authority == authorityStaffGM ||
		authority == authorityStaffGMAlt
}

func authorityBypassesRoomPassword(authority byte) bool {
	return authority == authorityObserverGM
}

func authorityBypassesRoomClassLimits(authority byte) bool {
	return hasStaffAuthority(authority)
}

func (s *Session) hasStaffAuthority() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return hasStaffAuthority(s.UserAuthority)
}

func (s *Session) hasObserverAuthority() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return authorityBypassesRoomPassword(s.UserAuthority)
}
