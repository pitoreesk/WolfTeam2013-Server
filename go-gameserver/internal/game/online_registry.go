package game

import (
	"container/list"
	"net/netip"
	"strings"
	"unicode"
)

// orderedSessionSet preserves connection order for protocol paths that
// historically selected the oldest or newest matching connection.
type orderedSessionSet struct {
	order    list.List
	elements map[*Session]*list.Element
}

type orderedSession struct {
	session  *Session
	sequence uint64
}

func (set *orderedSessionSet) add(session *Session, sequence uint64) {
	if set.elements == nil {
		set.elements = make(map[*Session]*list.Element)
	}
	if _, exists := set.elements[session]; exists {
		return
	}
	value := orderedSession{session: session, sequence: sequence}
	for current := set.order.Back(); current != nil; current = current.Prev() {
		if current.Value.(orderedSession).sequence < sequence {
			set.elements[session] = set.order.InsertAfter(value, current)
			return
		}
	}
	set.elements[session] = set.order.PushFront(value)
}

// append adds a session whose sequence is newer than every existing entry.
// Connection registration always has that property, so it does not need the
// ordered insertion scan used by duplicate identity indexes.
func (set *orderedSessionSet) append(session *Session, sequence uint64) {
	if set.elements == nil {
		set.elements = make(map[*Session]*list.Element)
	}
	if _, exists := set.elements[session]; exists {
		return
	}
	set.elements[session] = set.order.PushBack(orderedSession{session: session, sequence: sequence})
}

func (set *orderedSessionSet) remove(session *Session) {
	if element := set.elements[session]; element != nil {
		set.order.Remove(element)
		delete(set.elements, session)
	}
}

func (set *orderedSessionSet) oldest() *Session {
	if element := set.order.Front(); element != nil {
		return element.Value.(orderedSession).session
	}
	return nil
}

func (set *orderedSessionSet) newest() *Session {
	if element := set.order.Back(); element != nil {
		return element.Value.(orderedSession).session
	}
	return nil
}

func (set *orderedSessionSet) empty() bool {
	return set.order.Len() == 0
}

func (set *orderedSessionSet) sessions() []*Session {
	result := make([]*Session, 0, set.order.Len())
	for element := set.order.Front(); element != nil; element = element.Next() {
		result = append(result, element.Value.(orderedSession).session)
	}
	return result
}

func (set *orderedSessionSet) oldestMatching(match func(*Session) bool) *Session {
	for element := set.order.Front(); element != nil; element = element.Next() {
		session := element.Value.(orderedSession).session
		if match(session) {
			return session
		}
	}
	return nil
}

func (set *orderedSessionSet) newestMatching(match func(*Session) bool) *Session {
	for element := set.order.Back(); element != nil; element = element.Prev() {
		session := element.Value.(orderedSession).session
		if match(session) {
			return session
		}
	}
	return nil
}

func (set *orderedSessionSet) except(exclude *Session) []*Session {
	result := make([]*Session, 0, set.order.Len())
	for element := set.order.Front(); element != nil; element = element.Next() {
		session := element.Value.(orderedSession).session
		if session != exclude {
			result = append(result, session)
		}
	}
	return result
}

// orderedSessionMembership keeps membership changes O(1) while enumerating in
// the immutable connection order. A player leaving and later returning to the
// lobby only toggles a map entry instead of being inserted back into a list.
type orderedSessionMembership struct {
	order   *orderedSessionSet
	members map[*Session]struct{}
}

func newOrderedSessionMembership(order *orderedSessionSet) orderedSessionMembership {
	return orderedSessionMembership{
		order:   order,
		members: make(map[*Session]struct{}),
	}
}

func (membership *orderedSessionMembership) add(session *Session) {
	membership.members[session] = struct{}{}
}

func (membership *orderedSessionMembership) remove(session *Session) {
	delete(membership.members, session)
}

func (membership *orderedSessionMembership) sessions() []*Session {
	result := make([]*Session, 0, len(membership.members))
	if membership.order == nil {
		return result
	}
	for element := membership.order.order.Front(); element != nil; element = element.Next() {
		session := element.Value.(orderedSession).session
		if _, active := membership.members[session]; active {
			result = append(result, session)
		}
	}
	return result
}

type onlineRegistry struct {
	byUserID      map[int]*orderedSessionSet
	byCallsign    map[string]*orderedSessionSet
	bySessionID   map[uint16]*orderedSessionSet
	byUDPEndpoint map[netip.AddrPort]*orderedSessionSet
	connections   *orderedSessionSet
	lobby         orderedSessionMembership
	connectionAge map[*Session]uint64
	nextSequence  uint64
}

func newOnlineRegistry() onlineRegistry {
	connections := &orderedSessionSet{}
	return onlineRegistry{
		byUserID:      make(map[int]*orderedSessionSet),
		byCallsign:    make(map[string]*orderedSessionSet),
		bySessionID:   make(map[uint16]*orderedSessionSet),
		byUDPEndpoint: make(map[netip.AddrPort]*orderedSessionSet),
		connections:   connections,
		lobby:         newOrderedSessionMembership(connections),
		connectionAge: make(map[*Session]uint64),
	}
}

// normalizedCallsign produces the same equivalence classes as strings.EqualFold
// while retaining a stable map key for non-ASCII callsigns.
func normalizedCallsign(callsign string) string {
	var normalized strings.Builder
	normalized.Grow(len(callsign))
	for _, character := range callsign {
		canonical := character
		for folded := unicode.SimpleFold(character); folded != character; folded = unicode.SimpleFold(folded) {
			if folded < canonical {
				canonical = folded
			}
		}
		normalized.WriteRune(canonical)
	}
	return normalized.String()
}

func (registry *onlineRegistry) add(session *Session) {
	registry.trackConnection(session)
	_, callsign, userID, _ := session.identity()
	registry.addIdentity(session, userID, callsign)
	addSessionIndex(registry.bySessionID, session.SessionID(), session, registry.connectionAge[session])
	if endpoint, ok := sessionUDPEndpoint(session); ok {
		addSessionIndex(registry.byUDPEndpoint, endpoint, session, registry.connectionAge[session])
	}
	registry.refreshLobby(session)
}

func (registry *onlineRegistry) remove(session *Session) {
	_, callsign, userID, _ := session.identity()
	registry.removeIdentity(session, userID, callsign)
	removeSessionIndex(registry.bySessionID, session.SessionID(), session)
	if endpoint, ok := sessionUDPEndpoint(session); ok {
		removeSessionIndex(registry.byUDPEndpoint, endpoint, session)
	}
	registry.lobby.remove(session)
	registry.connections.remove(session)
	delete(registry.connectionAge, session)
}

func (registry *onlineRegistry) trackConnection(session *Session) {
	if _, exists := registry.connectionAge[session]; exists {
		return
	}
	registry.nextSequence++
	registry.connectionAge[session] = registry.nextSequence
	registry.connections.append(session, registry.nextSequence)
}

func (registry *onlineRegistry) replaceIdentity(session *Session, oldUserID int, oldCallsign string) {
	registry.removeIdentity(session, oldUserID, oldCallsign)
	_, callsign, userID, _ := session.identity()
	registry.addIdentity(session, userID, callsign)
	registry.refreshLobby(session)
}

func (registry *onlineRegistry) removeIdentityOnly(session *Session, userID int, callsign string) {
	registry.removeIdentity(session, userID, callsign)
	registry.lobby.remove(session)
}

func (registry *onlineRegistry) refreshLobby(session *Session) {
	registry.lobby.remove(session)
	loginName, callsign, userID, _ := session.identity()
	session.mu.RLock()
	inLobby := session.RoomNumber == -1 && userID > 0 && loginName != "" && strings.TrimSpace(callsign) != ""
	session.mu.RUnlock()
	if inLobby {
		registry.lobby.add(session)
	}
}

func (registry *onlineRegistry) replaceUDPEndpoint(session *Session, oldAddress netip.Addr, oldPort uint16) {
	if endpoint, ok := udpEndpoint(oldAddress, oldPort); ok {
		removeSessionIndex(registry.byUDPEndpoint, endpoint, session)
	}
	if endpoint, ok := sessionUDPEndpoint(session); ok {
		addSessionIndex(registry.byUDPEndpoint, endpoint, session, registry.connectionAge[session])
	}
}

func (registry *onlineRegistry) addIdentity(session *Session, userID int, callsign string) {
	if userID > 0 {
		addSessionIndex(registry.byUserID, userID, session, registry.connectionAge[session])
	}
	if callsign != "" {
		addSessionIndex(registry.byCallsign, normalizedCallsign(callsign), session, registry.connectionAge[session])
	}
}

func (registry *onlineRegistry) removeIdentity(session *Session, userID int, callsign string) {
	if userID > 0 {
		removeSessionIndex(registry.byUserID, userID, session)
	}
	if callsign != "" {
		removeSessionIndex(registry.byCallsign, normalizedCallsign(callsign), session)
	}
}

func sessionUDPEndpoint(session *Session) (netip.AddrPort, bool) {
	session.mu.RLock()
	defer session.mu.RUnlock()
	return udpEndpoint(session.UDPIP, session.UDPPort)
}

func udpEndpoint(address netip.Addr, port uint16) (netip.AddrPort, bool) {
	if !address.IsValid() || port == 0 {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(address.Unmap(), port), true
}

func addSessionIndex[K comparable](index map[K]*orderedSessionSet, key K, session *Session, sequence uint64) {
	set := index[key]
	if set == nil {
		set = &orderedSessionSet{}
		index[key] = set
	}
	set.add(session, sequence)
}

func removeSessionIndex[K comparable](index map[K]*orderedSessionSet, key K, session *Session) {
	set := index[key]
	if set == nil {
		return
	}
	set.remove(session)
	if set.empty() {
		delete(index, key)
	}
}

func (s *Server) refreshSessionLobby(session *Session) {
	s.mu.Lock()
	if _, tracked := s.sessions[session]; tracked {
		s.online.refreshLobby(session)
		if err := s.syncChannelMemberLocked(session); err != nil && s.logger != nil {
			s.logger.Warn("assign returning channel member", "error", err)
		}
	}
	s.mu.Unlock()
}

func (s *Server) sessionByUDPEndpoint(address netip.Addr, port uint16, includeBots bool) *Session {
	endpoint, ok := udpEndpoint(address, port)
	if !ok {
		return nil
	}
	s.mu.RLock()
	if set := s.online.byUDPEndpoint[endpoint]; set != nil {
		if session := set.oldestMatching(func(candidate *Session) bool {
			candidate.mu.RLock()
			defer candidate.mu.RUnlock()
			return (includeBots || !candidate.IsBot) && candidate.UDPIP.Unmap() == endpoint.Addr() && candidate.UDPPort == endpoint.Port()
		}); session != nil {
			s.mu.RUnlock()
			return session
		}
	}
	// Some tests construct server internals directly. Keep a compatibility
	// fallback; normal connections always populate the registry.
	if session := s.online.connections.oldestMatching(func(candidate *Session) bool {
		candidate.mu.RLock()
		defer candidate.mu.RUnlock()
		return (includeBots || !candidate.IsBot) && candidate.UDPIP.Unmap() == endpoint.Addr() && candidate.UDPPort == endpoint.Port()
	}); session != nil {
		s.mu.RUnlock()
		return session
	}
	s.mu.RUnlock()
	return nil
}
