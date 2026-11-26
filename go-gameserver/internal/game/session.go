package game

import (
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

var roomMembershipVersion atomic.Uint64

type Session struct {
	wire         *transport.EncryptedConn
	logger       *slog.Logger
	sessionID    uint16
	AdvertisedIP netip.Addr
	PeerIP       netip.Addr

	mu            sync.RWMutex
	LoginName     string
	Callsign      string
	UserID        int
	UserLevel     byte
	UserAuthority byte
	ChannelType   store.ChannelType
	// PrideName/PrideMarkID are cached after login so room and channel roster
	// packets can carry the same Pride display data as user-info packets.
	// The current database has no crest/mark column, so PrideMarkID remains 0
	// until a proven source for that value is added.
	PrideName     string
	PrideMarkID   uint32
	// loginOrder orders the online members of a Pride chain (first logged
	// in first).
	loginOrder uint64
	Muted         bool
	MuteExpiresAt time.Time
	UDPKicked     bool
	RoomNumber    int16
	RoomSlot      int16
	RoomTeam      byte
	RoomTeamSlot  byte
	RoomStatus    byte
	lobbySince    uint64
	// pendingClientRoomExit remembers a server-side departure whose first
	// notification may need to be replayed after a section transition.
	pendingClientRoomExit     bool
	pendingClientRoomExitSlot byte
	// channelSheet and channelMemberID are guarded by Server.mu. The member ID
	// is stable while the session remains in a lobby sheet and is released while
	// the session is in a room.
	channelSheet          byte
	channelMemberID       byte
	channelMemberAssigned bool
	UDPIP                 netip.Addr
	UDPPort               uint16
	LocalUDPIP            netip.Addr
	LocalUDPPort          uint16
	RelayPort             uint16
	IsBot                 bool
	BotWolf               bool
	BotDifficulty         byte
	BotNumber             byte
	matchUDPRemote        netip.AddrPort
	relayUDPRemote        netip.AddrPort

	rosterMu            sync.Mutex
	rosterCache         rosterState
	rosterCached        bool
	rosterIncludesPower bool

	CharacterSelector        map[int]int
	InitialDefaultCharCommit bool
	PendingBoxUnique         int
	PendingBoxReward         int
	PendingBoxDays           int
	PendingBoxRemaining      int
	PendingTutorialTrack     byte
	PendingTutorialLevel     byte
	PendingPermanentTicket   int
	PendingForgeCategory     byte
	PendingForgeRound        byte
	PendingForgeItem         int
	PendingForgeDays         int
	PlayTimeSeconds          uint32
	PowerUserColor           uint32
	PowerUserLabel           string
}

func newSession(wire *transport.EncryptedConn, advertisedIP, peerIP netip.Addr) (*Session, error) {
	return &Session{
		wire:         wire,
		AdvertisedIP: advertisedIP,
		PeerIP:       peerIP,
		RoomNumber:   -1,
		RoomSlot:     -1,
		RoomTeam:     0xff,
		RoomTeamSlot: 0xff,
	}, nil
}

func newBotSession(name string, seed int, peerPort uint16) *Session {
	loopback := netip.MustParseAddr("127.0.0.1")
	session := &Session{
		AdvertisedIP: loopback,
		PeerIP:       loopback,
		LoginName:    name,
		Callsign:     name,
		UserLevel:    0,
		RoomNumber:   -1,
		RoomSlot:     -1,
		RoomTeam:     0xff,
		RoomTeamSlot: 0xff,
		UDPIP:        loopback,
		UDPPort:      peerPort,
		sessionID:    uint16(0x08e0 + (seed & 0x1f)),
		IsBot:        true,
	}
	return session
}

func (s *Session) SessionID() uint16 {
	return s.sessionID
}

func (s *Session) deferClientRoomExit(slot byte) {
	s.mu.Lock()
	s.pendingClientRoomExit = true
	s.pendingClientRoomExitSlot = slot
	s.mu.Unlock()
}

func (s *Session) pendingClientRoomDeparture() (byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pendingClientRoomExitSlot, s.pendingClientRoomExit
}

func (s *Session) clearPendingClientRoomExit(slot byte) {
	s.mu.Lock()
	if s.pendingClientRoomExit && s.pendingClientRoomExitSlot == slot {
		s.pendingClientRoomExit = false
	}
	s.mu.Unlock()
}

func (s *Session) Write(messageID uint16, payload []byte) error {
	if s.IsBot || s.wire == nil {
		return nil
	}
	_, err := s.wire.WriteMessage(messageID, payload)
	if err == nil {
		s.logWrite(messageID, payload)
	}
	return err
}

func (s *Session) WriteMessages(messages []transport.OutboundMessage) error {
	if s.IsBot || s.wire == nil || len(messages) == 0 {
		return nil
	}
	if err := s.wire.WriteMessages(messages); err != nil {
		return err
	}
	for _, message := range messages {
		s.logWrite(message.MessageID, message.Payload)
	}
	return nil
}

func (s *Session) logWrite(messageID uint16, payload []byte) {
	if s.logger == nil {
		return
	}
	firstByte := -1
	if len(payload) != 0 {
		firstByte = int(payload[0])
	}
	prefixLength := min(len(payload), 8)
	s.logger.Debug("game message sent",
		"message_id", fmt.Sprintf("0x%04x", messageID),
		"payload_bytes", len(payload),
		"first_byte", firstByte,
		"prefix", fmt.Sprintf("% x", payload[:prefixLength]))
}

func (s *Session) setAuthenticated(username string, userID int, callsign string, level byte) {
	s.setAuthenticatedWithAuthority(username, userID, callsign, level, 0)
}

func (s *Session) setAuthenticatedWithAuthority(username string, userID int, callsign string, level, authority byte) {
	s.mu.Lock()
	s.LoginName = username
	s.UserID = userID
	s.Callsign = callsign
	s.UserLevel = level
	s.UserAuthority = authority
	s.PrideName = ""
	s.PrideMarkID = 0
	s.mu.Unlock()
	s.invalidateRosterState()
}

func (s *Session) setChannelType(channelType store.ChannelType) {
	if !channelType.Valid() {
		channelType = store.ChannelTypeNormal
	}
	s.mu.Lock()
	s.ChannelType = channelType
	s.mu.Unlock()
}

func (s *Session) channelType() store.ChannelType {
	s.mu.RLock()
	channelType := s.ChannelType
	s.mu.RUnlock()
	if !channelType.Valid() {
		return store.ChannelTypeNormal
	}
	return channelType
}

func (s *Session) setPrideDisplay(name string, markID uint32) {
	s.mu.Lock()
	s.PrideName = name
	s.PrideMarkID = markID
	s.mu.Unlock()
}

func (s *Session) prideDisplay() (string, uint32) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.PrideName, s.PrideMarkID
}

func (s *Session) setMuted(muted bool) {
	s.setMutedUntil(muted, time.Time{})
}

func (s *Session) setMutedUntil(muted bool, expiresAt time.Time) {
	s.mu.Lock()
	s.Muted = muted
	s.MuteExpiresAt = expiresAt
	s.mu.Unlock()
}

func (s *Session) muted() bool {
	return s.mutedAt(time.Now())
}

func (s *Session) mutedAt(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Muted && !s.MuteExpiresAt.IsZero() && !now.Before(s.MuteExpiresAt) {
		s.Muted = false
		s.MuteExpiresAt = time.Time{}
	}
	return s.Muted
}

func (s *Session) roomLocation() (roomNumber int16, roomStatus byte) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.RoomNumber, s.RoomStatus
}

// cachedRosterState serializes the initial roster load for a session. Room
// joins can build overlapping roster payloads, so a simple get-then-set cache
// would still allow duplicate database reads for the same player.
func (s *Session) cachedRosterState(includePower bool, load func() rosterState) rosterState {
	s.rosterMu.Lock()
	defer s.rosterMu.Unlock()
	if s.rosterCached && (!includePower || s.rosterIncludesPower) {
		return s.rosterCache
	}
	state := load()
	s.rosterCache = state
	s.rosterCached = true
	s.rosterIncludesPower = includePower
	return state
}

func (s *Session) invalidateRosterState() {
	s.rosterMu.Lock()
	s.rosterCache = rosterState{}
	s.rosterCached = false
	s.rosterIncludesPower = false
	s.rosterMu.Unlock()
}

func (s *Session) updateCallsign(callsign string) {
	s.mu.Lock()
	s.Callsign = callsign
	s.mu.Unlock()
}

func (s *Session) addPlayTime(seconds uint32) uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	maximum := ^uint32(0)
	if maximum-s.PlayTimeSeconds < seconds {
		s.PlayTimeSeconds = maximum
	} else {
		s.PlayTimeSeconds += seconds
	}
	return s.PlayTimeSeconds
}

func (s *Session) setPlayTime(seconds uint32) {
	s.mu.Lock()
	s.PlayTimeSeconds = seconds
	s.mu.Unlock()
}

func (s *Session) playTimeSeconds() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.PlayTimeSeconds
}

func (s *Session) setPowerUserSettings(color uint32, label string) {
	s.mu.Lock()
	s.PowerUserColor = color
	s.PowerUserLabel = label
	s.mu.Unlock()
}

func (s *Session) powerUserSettings() (uint32, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.PowerUserColor, s.PowerUserLabel
}

func (s *Session) setCharacterSelector(slots map[int]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.CharacterSelector = cloneIntMap(slots)
}

func (s *Session) characterSelector() map[int]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneIntMap(s.CharacterSelector)
}

func (s *Session) expectInitialDefaultCharacter() {
	s.mu.Lock()
	s.InitialDefaultCharCommit = true
	s.mu.Unlock()
}

func (s *Session) consumeInitialDefaultCharacter() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.InitialDefaultCharCommit
	s.InitialDefaultCharCommit = false
	return value
}

func (s *Session) setPendingBox(unique, reward, days, remaining int) {
	s.mu.Lock()
	s.PendingBoxUnique, s.PendingBoxReward, s.PendingBoxDays, s.PendingBoxRemaining = unique, reward, days, remaining
	s.mu.Unlock()
}

func (s *Session) takePendingBox() (unique, reward, days, remaining int, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.PendingBoxReward <= 0 {
		return 0, 0, 0, 0, false
	}
	unique, reward, days, remaining = s.PendingBoxUnique, s.PendingBoxReward, s.PendingBoxDays, s.PendingBoxRemaining
	s.PendingBoxUnique, s.PendingBoxReward, s.PendingBoxDays, s.PendingBoxRemaining = 0, 0, 0, 0
	return unique, reward, days, remaining, true
}

func (s *Session) setPendingTutorial(track, level byte) {
	s.mu.Lock()
	s.PendingTutorialTrack, s.PendingTutorialLevel = track, level
	s.mu.Unlock()
}

func (s *Session) takePendingTutorial() (track, level byte, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	track, level = s.PendingTutorialTrack, s.PendingTutorialLevel
	s.PendingTutorialTrack, s.PendingTutorialLevel = 0, 0
	return track, level, (track == 1 || track == 2) && level > 0
}

func (s *Session) setPendingForge(category, round byte, item, days int) {
	s.mu.Lock()
	s.PendingForgeCategory = category
	s.PendingForgeRound = round
	s.PendingForgeItem = item
	s.PendingForgeDays = days
	s.mu.Unlock()
}

func (s *Session) pendingForge(category, round byte) (item, days int, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.PendingForgeItem, s.PendingForgeDays,
		s.PendingForgeItem > 0 && s.PendingForgeCategory == category && s.PendingForgeRound == round
}

func (s *Session) clearPendingForge() {
	s.mu.Lock()
	s.PendingForgeCategory = 0
	s.PendingForgeRound = 0
	s.PendingForgeItem = 0
	s.PendingForgeDays = 0
	s.mu.Unlock()
}

func cloneIntMap(source map[int]int) map[int]int {
	result := make(map[int]int, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (s *Session) identity() (string, string, int, byte) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.LoginName, s.Callsign, s.UserID, s.UserLevel
}

func (s *Session) lobbyAudienceVersion() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lobbySince
}

func (s *Session) observeMatchUDP(remote netip.AddrPort) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.matchUDPRemote == remote {
		return false
	}
	s.matchUDPRemote = remote
	return true
}

func (s *Session) observeRelayUDP(remote netip.AddrPort) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.relayUDPRemote == remote {
		return false
	}
	s.relayUDPRemote = remote
	return true
}

func (s *Session) setLoginOrder(order uint64) {
	s.mu.Lock()
	s.loginOrder = order
	s.mu.Unlock()
}

func (s *Session) getLoginOrder() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loginOrder
}
