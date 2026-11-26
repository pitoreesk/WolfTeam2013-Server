package game

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"time"
)

const (
	roomSlotCount        = 17
	teamSlotCount        = 8
	bombFuseSeconds      = 40
	kickVoteDuration     = 10 * time.Second
	redTeam              = byte(1)
	blueTeam             = byte(2)
	aiModeFlag           = uint32(0x040000)
	wolfHuntModeFlag     = uint32(0x000100)
	wolfHuntDominate     = uint32(0x001000)
	heroModeFlag         = byte(0x01)
	// Room status values the client caches per slot (member +312).
	// 0/1 are the room screen (1 is also what a room-section ENDGAME resets
	// every member to, sub_340D2280), 3 is "in the field" - loading, or the
	// spectator shell the STARTROUND handler (sub_342E2450) enters through
	// sub_3423D870 - and 4 is an active body (sub_3423F8B0). The server also
	// keeps a member's ready flag in RoomStatus 1 before the match starts.
	readyRoomStatus     = byte(1)
	spectatorRoomStatus = byte(3)
	activeRoomStatus    = byte(4)
	wolfHuntCreatureTeam = redTeam
	wolfHuntHumanTeam    = blueTeam
	wolfHuntKingLives    = byte(3)
	wolfHuntWolfLives    = byte(1)
	heroLifeKind         = byte(1)
	wolfHuntLifeKind     = byte(2)
	defaultClassMin      = byte(0)
	defaultClassMax      = byte(0x3d)
)

type RoomConfig struct {
	Title            string
	Password         string
	Capacity         byte
	Map              byte
	Mode             uint32
	Wins             byte
	EquipmentCount   byte
	Time             uint16
	Points           uint16
	GameFlags        byte
	WeaponFlags      byte
	HeavyWeaponLimit uint16
	MapSetting       byte
	RuleSetting      byte
	ClassLimitMin    byte
	ClassLimitMax    byte
	// The client emits these two u16 values only on channel types 3 and 4
	// (IDA: CS_FD_CREATE_REQ at CShell+0x341889C0). Their semantics are not
	// identified by the available client data, so they are retained neutrally.
	ChannelExtraU16A uint16
	ChannelExtraU16B uint16
	classLimitsSet   bool
	channelExtrasSet bool
}

func roomClassLimits(config RoomConfig) (byte, byte) {
	if !config.classLimitsSet && config.ClassLimitMin == 0 && config.ClassLimitMax == 0 {
		return defaultClassMin, defaultClassMax
	}
	return config.ClassLimitMin, config.ClassLimitMax
}

func roomAllowsLevel(config RoomConfig, level byte) bool {
	// Database-free sessions use 0xff as an unknown-level sentinel. It is not
	// a player rank and must not make otherwise valid room entry impossible.
	if level == 0xff {
		return true
	}
	minimum, maximum := roomClassLimits(config)
	return minimum <= level && level <= maximum
}

func heroModeEnabled(config RoomConfig) bool {
	return config.WeaponFlags&heroModeFlag != 0
}

type Room struct {
	mu sync.RWMutex

	Number             int16
	Config             RoomConfig
	Open               bool
	GameStarted        bool
	RoundStarted       bool
	Master             byte
	Slots              [roomSlotCount]*Session
	Users              byte
	Round              byte
	RoundStartedAt     time.Time
	GameStartedAt      time.Time
	RedScore           uint16
	BlueScore          uint16
	Flags              [5]byte
	FlagPoints         [roomSlotCount]byte
	Dead               [roomSlotCount]bool
	LateJoined         [roomSlotCount]bool
	MatchStats         [roomSlotCount]MatchPlayerStats
	PlantTime          time.Time
	BombPlanter        byte
	LastBotSpawnRound  int
	LastSpecialRound   int
	SpecialRoles       [roomSlotCount]byte
	WolfKings          [roomSlotCount]bool
	RemainingLives     [roomSlotCount]byte
	HeroSlots          [roomSlotCount]bool
	PrivateLadder      bool
	PowerMaster        bool
	SwitchedSides      bool
	sideSwitchChannel  bool
	ladderSearch       *PrivateLadderSearchRequest
	ladderCreator      *Session
	LadderKillTotals   [roomSlotCount]uint32
	kickVote           KickVote
	joinabilityChanged func(bool)
}

type MatchPlayerStats struct {
	Kills   uint32
	Deaths  uint32
	Plants  uint32
	Defuses uint32
}

type MatchPlayer struct {
	Session *Session
	Slot    byte
	Team    byte
	Stats   MatchPlayerStats
}

type KickVote struct {
	Active        bool
	Initiator     byte
	Target        byte
	Reason        byte
	StartedAt     time.Time
	TargetSession *Session
	Voted         [roomSlotCount]bool
	Choices       [roomSlotCount]byte
}

type KickVoteStatus struct {
	Target        byte
	Agreement     byte
	Opposition    byte
	Waiver        byte
	Passed        bool
	Resolved      bool
	TargetSession *Session
}

type RoomSnapshot struct {
	Number        int16
	Config        RoomConfig
	Open          bool
	GameStarted   bool
	RoundStarted  bool
	Master        byte
	Users         byte
	Members       []*Session
	Round         byte
	RedScore      uint16
	BlueScore     uint16
	PowerMaster   bool
	SwitchedSides bool
	Dead          [roomSlotCount]bool
	LateJoined    [roomSlotCount]bool
}

func newRoom(number int16, config RoomConfig) *Room {
	return &Room{
		Number: number, Config: config, Open: true, BombPlanter: 0xff,
		LastBotSpawnRound: -1, LastSpecialRound: -1,
	}
}

func (r *Room) Snapshot() RoomSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	members := make([]*Session, 0, r.Users)
	for _, session := range r.Slots {
		if session != nil {
			members = append(members, session)
		}
	}
	return RoomSnapshot{
		Number:        r.Number,
		Config:        r.Config,
		Open:          r.Open,
		GameStarted:   r.GameStarted,
		RoundStarted:  r.RoundStarted,
		Master:        r.Master,
		Users:         r.Users,
		Members:       members,
		Round:         r.Round,
		RedScore:      r.RedScore,
		BlueScore:     r.BlueScore,
		PowerMaster:   r.PowerMaster,
		SwitchedSides: r.SwitchedSides,
		Dead:          r.Dead,
		LateJoined:    r.LateJoined,
	}
}

func (s RoomSnapshot) IsRoundBased() bool {
	mode := byte(s.Config.Mode)
	return mode == 32 || mode == 48 || mode == 4 || mode == 20 || mode == 132 || (s.Config.Mode&wolfHuntModeFlag != 0)
}

func (r *Room) SetPowerMaster(enabled bool) {
	r.mu.Lock()
	r.PowerMaster = enabled
	r.mu.Unlock()
}

func (r *Room) Add(session *Session) (byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// A duplicated or retried enter-room request must be idempotent. Placing the
	// same Session in a second slot corrupts its RoomSlot and all room/team counts
	// while the original slot still points at it.
	for index, current := range r.Slots {
		if current == session {
			return byte(index), true
		}
	}
	if r.Users >= r.Config.Capacity && r.Config.Capacity != 0 {
		return 0xff, false
	}
	slot := -1
	for index, current := range r.Slots {
		if current == nil {
			slot = index
			break
		}
	}
	if slot < 0 {
		return 0xff, false
	}
	team := byte(1)
	if r.Config.Mode&wolfHuntModeFlag != 0 {
		team = wolfHuntHumanTeam
	} else if r.teamCountLocked(2) < r.teamCountLocked(1) {
		team = 2
	}
	teamSlot, available := r.availableTeamSlotLocked(team, session)
	if !available {
		if team == redTeam {
			team = blueTeam
		} else {
			team = redTeam
		}
		teamSlot, available = r.availableTeamSlotLocked(team, session)
		if !available {
			return 0xff, false
		}
	}
	r.Slots[slot] = session
	if r.GameStarted {
		r.LateJoined[slot] = true
	} else {
		r.LateJoined[slot] = false
	}
	r.Dead[slot] = false
	r.MatchStats[slot] = MatchPlayerStats{}
	r.HeroSlots[slot] = false
	r.Users++
	session.mu.Lock()
	roomMembershipVersion.Add(1)
	session.RoomNumber = r.Number
	session.lobbySince = 0
	session.RoomSlot = int16(slot)
	session.RoomTeam = team
	session.RoomTeamSlot = teamSlot
	session.RoomStatus = 0
	session.pendingClientRoomExit = false
	session.mu.Unlock()
	r.notifyJoinabilityLocked()
	return byte(slot), true
}

func (r *Room) MemberSlot(session *Session) (byte, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for index, current := range r.Slots {
		if current == session {
			return byte(index), true
		}
	}
	return 0xff, false
}

func (r *Room) Remove(session *Session) (slot byte, newMaster byte, empty bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	slot = 0xff
	for index, current := range r.Slots {
		if current == session {
			slot = byte(index)
			r.Slots[index] = nil
			r.Dead[index] = false
			r.MatchStats[index] = MatchPlayerStats{}
			r.HeroSlots[index] = false
			if r.kickVote.Active {
				if r.kickVote.TargetSession == session {
					r.kickVote = KickVote{}
				} else {
					r.kickVote.Voted[index] = false
					r.kickVote.Choices[index] = 0
				}
			}
			if r.Users > 0 {
				r.Users--
			}
			break
		}
	}
	if slot == r.Master {
		for index, current := range r.Slots {
			if current != nil && !current.IsBot {
				r.Master = byte(index)
				break
			}
		}
	}
	newMaster = r.Master
	humans := 0
	for _, current := range r.Slots {
		if current != nil && !current.IsBot {
			humans++
		}
	}
	if humans == 0 {
		for index := range r.Slots {
			r.Slots[index] = nil
		}
		r.HeroSlots = [roomSlotCount]bool{}
		r.Users = 0
	}
	empty = humans == 0
	session.mu.Lock()
	lobbySince := roomMembershipVersion.Add(1)
	session.RoomNumber = -1
	session.lobbySince = lobbySince
	session.RoomSlot = -1
	session.RoomTeam = 0xff
	session.RoomTeamSlot = 0xff
	session.RoomStatus = 0
	session.mu.Unlock()
	r.notifyJoinabilityLocked()
	return slot, newMaster, empty
}

func (r *Room) ProvisionBot(team, difficulty byte, wolf bool, peerPort uint16, requestedRow int) *Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	if team != 1 && team != 2 {
		return nil
	}
	if requestedRow >= 0 && requestedRow < teamSlotCount {
		for _, current := range r.Slots {
			if current == nil {
				continue
			}
			current.mu.Lock()
			matches := current.IsBot && current.RoomTeam == team && current.RoomTeamSlot == byte(requestedRow)
			if matches {
				current.BotDifficulty = difficulty
				current.BotWolf = wolf
				current.LoginName = botName(difficulty, current.BotNumber)
				current.mu.Unlock()
				return current
			}
			current.mu.Unlock()
		}
	}
	if r.teamCountLocked(team) >= teamSlotCount || int(r.Users) >= teamSlotCount*2 {
		return nil
	}
	botCount := 0
	for _, current := range r.Slots {
		if current != nil && current.IsBot {
			botCount++
		}
	}
	capacity := int(r.Config.Capacity)
	if capacity <= 0 || capacity > len(r.Slots) {
		capacity = len(r.Slots)
	}
	slot := -1
	for index := 0; index < capacity; index++ {
		if r.Slots[index] == nil {
			slot = index
			break
		}
	}
	if slot < 0 {
		return nil
	}
	name := botName(difficulty, byte(botCount+1))
	bot := newBotSession(name, botCount, peerPort)
	bot.RoomNumber = r.Number
	bot.RoomSlot = int16(slot)
	bot.RoomTeam = team
	bot.RoomTeamSlot = r.nextTeamSlotLocked(team, bot)
	if requestedRow >= 0 && requestedRow < teamSlotCount && !r.teamSlotOccupiedLocked(team, byte(requestedRow), bot) {
		bot.RoomTeamSlot = byte(requestedRow)
	}
	bot.RoomStatus = 0
	bot.BotWolf = wolf
	bot.BotDifficulty = difficulty
	bot.BotNumber = byte(botCount + 1)
	r.Slots[slot] = bot
	r.HeroSlots[slot] = false
	r.Users++
	r.Config.Mode = (r.Config.Mode &^ 0xff) | 0x02 | aiModeFlag
	r.notifyJoinabilityLocked()
	return bot
}

func (r *Room) RemoveBot(slot byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if int(slot) >= len(r.Slots) {
		return false
	}
	bot := r.Slots[slot]
	if bot == nil || !bot.IsBot {
		return false
	}
	r.Slots[slot] = nil
	r.Dead[slot] = false
	r.MatchStats[slot] = MatchPlayerStats{}
	r.HeroSlots[slot] = false
	if r.Users > 0 {
		r.Users--
	}
	r.notifyJoinabilityLocked()
	bot.mu.Lock()
	bot.RoomNumber = -1
	bot.RoomSlot = -1
	bot.RoomTeam = 0xff
	bot.RoomTeamSlot = 0xff
	bot.RoomStatus = 0
	bot.mu.Unlock()
	for _, session := range r.Slots {
		if session != nil && session.IsBot {
			return true
		}
	}
	r.Config.Mode &^= aiModeFlag
	return true
}

func botName(difficulty, number byte) string {
	difficultyName := "EASY"
	if difficulty == 2 {
		difficultyName = "NORMAL"
	} else if difficulty >= 3 {
		difficultyName = "HARD"
	}
	return fmt.Sprintf("AI %s %d", difficultyName, number)
}

func (r *Room) teamSlotOccupiedLocked(team, teamSlot byte, except *Session) bool {
	for _, session := range r.Slots {
		if session == nil || session == except {
			continue
		}
		session.mu.RLock()
		occupied := session.RoomTeam == team && session.RoomTeamSlot == teamSlot
		session.mu.RUnlock()
		if occupied {
			return true
		}
	}
	return false
}

func (r *Room) MoveToOtherTeam(session *Session) (slot, team, teamSlot byte, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.RoomNumber != r.Number || session.RoomSlot < 0 {
		return 0, 0, 0, false
	}
	team = 1
	if session.RoomTeam == 1 {
		team = 2
	}
	teamSlot, available := r.availableTeamSlotLocked(team, session)
	if !available {
		return 0, 0, 0, false
	}
	session.RoomTeam = team
	session.RoomTeamSlot = teamSlot
	return byte(session.RoomSlot), team, session.RoomTeamSlot, true
}

func (r *Room) SetStatus(session *Session, status byte) (byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.RoomNumber != r.Number || session.RoomSlot < 0 {
		return 0, false
	}
	session.RoomStatus = status
	return byte(session.RoomSlot), true
}

func (r *Room) TeamCounts() (team1, ready1, team2, ready2 int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, session := range r.Slots {
		if session == nil {
			continue
		}
		session.mu.RLock()
		team, status := session.RoomTeam, session.RoomStatus
		session.mu.RUnlock()
		switch team {
		case 1:
			team1++
			if status > 0 || session.IsBot {
				ready1++
			}
		case 2:
			team2++
			if status > 0 || session.IsBot {
				ready2++
			}
		}
	}
	return team1, ready1, team2, ready2
}

// BeginMatch applies the room transition and returns every slot whose ready
// state was promoted to the in-game state.
func (r *Room) BeginMatch() (changed []byte, gameMap, mode byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	gameMap, mode = r.Config.Map, byte(r.Config.Mode)
	if r.GameStarted {
		return nil, gameMap, mode
	}
	r.GameStarted = true
	r.GameStartedAt = time.Now()
	r.RedScore = 0
	r.BlueScore = 0
	r.Round = 1
	r.SwitchedSides = false
	r.Dead = [roomSlotCount]bool{}
	r.LateJoined = [roomSlotCount]bool{}
	r.MatchStats = [roomSlotCount]MatchPlayerStats{}
	r.LastSpecialRound = -1
	r.SpecialRoles = [roomSlotCount]byte{}
	r.WolfKings = [roomSlotCount]bool{}
	r.RemainingLives = [roomSlotCount]byte{}
	r.HeroSlots = [roomSlotCount]bool{}
	r.LadderKillTotals = [roomSlotCount]uint32{}
	r.PlantTime = time.Time{}
	r.BombPlanter = 0xff
	r.kickVote = KickVote{}
	for index, session := range r.Slots {
		if session == nil {
			continue
		}
		session.mu.Lock()
		if session.RoomStatus == 1 {
			session.RoomStatus = 3
			changed = append(changed, byte(index))
		}
		session.mu.Unlock()
	}
	return changed, gameMap, mode
}

func (r *Room) BeginRound(now time.Time) (round byte, remaining uint16, master byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.RoundStarted {
		return r.Round, r.remainingRoundSecondsLocked(now), r.Master
	}
	r.startRoundLocked(now)
	return r.Round, r.Config.Time, r.Master
}

// StartRoundWhenReady delays special-mode initialization until every connected
// player has requested the round. The barrier keeps team and role assignment
// synchronized for all participants.
func (r *Room) StartRoundWhenReady(session *Session, now time.Time) (slot byte, round byte, remaining uint16, master byte, startedNow, ready bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if session == nil {
		return 0xff, r.Round, 0, r.Master, false, false
	}
	session.mu.Lock()
	if session.RoomNumber != r.Number || session.RoomSlot < 0 || int(session.RoomSlot) >= len(r.Slots) || r.Slots[session.RoomSlot] != session {
		session.mu.Unlock()
		return 0xff, r.Round, 0, r.Master, false, false
	}
	slot = byte(session.RoomSlot)
	session.RoomStatus = 4
	session.mu.Unlock()
	if r.RoundStarted {
		return slot, r.Round, r.remainingRoundSecondsLocked(now), r.Master, false, true
	}
	if !r.GameStarted {
		return slot, r.Round, 0, r.Master, false, false
	}
	for _, session := range r.Slots {
		if session == nil || session.IsBot {
			continue
		}
		session.mu.RLock()
		team, status := session.RoomTeam, session.RoomStatus
		session.mu.RUnlock()
		if (team == 1 || team == 2) && status != 4 {
			return slot, r.Round, 0, r.Master, false, false
		}
	}
	r.startRoundLocked(now)
	return slot, r.Round, r.Config.Time, r.Master, true, true
}

func (r *Room) startRoundLocked(now time.Time) {
	r.RoundStartedAt = now
	r.RoundStarted = true
	r.Flags = [5]byte{}
	r.FlagPoints = [roomSlotCount]byte{}
	r.PlantTime = time.Time{}
	r.BombPlanter = 0xff
	r.kickVote = KickVote{}
}

func (r *Room) remainingRoundSecondsLocked(now time.Time) uint16 {
	if r.RoundStartedAt.IsZero() || now.Before(r.RoundStartedAt) {
		return r.Config.Time
	}
	elapsed := now.Sub(r.RoundStartedAt) / time.Second
	if elapsed >= time.Duration(r.Config.Time) {
		return 0
	}
	return r.Config.Time - uint16(elapsed)
}

type SpecialRoundState struct {
	WolfHunt bool
	Hero     bool
	Players  []WolfHuntTeamState
	Kings    []byte
	Wolves   []byte
	Heroes   []HeroSelection
	Life     []RemainLifeNotice
	Options  []HeroOptionPlayer
}

type HeroSelection struct {
	Slot byte
	Team byte
}

// PrepareSpecialRound initializes the server-owned role/life state once per
// round. Wolf Hunt is encoded in Mode's 0x100 family (0x180/0x980/0x1980).
// Hero mode uses WeaponFlags bit 0.
func (r *Room) PrepareSpecialRound() (SpecialRoundState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.GameStarted || !r.RoundStarted {
		return SpecialRoundState{}, false
	}
	if r.LastSpecialRound == int(r.Round) {
		// Return the already-selected state so every requester receives the same
		// role and team synchronization.
		return r.specialRoundStateLocked(), false
	}
	mode := r.Config.Mode
	wolfHunt := mode&wolfHuntModeFlag != 0
	hero := heroModeEnabled(r.Config)
	if !wolfHunt && !hero {
		return SpecialRoundState{}, false
	}
	r.LastSpecialRound = int(r.Round)
	r.SpecialRoles = [roomSlotCount]byte{}
	r.WolfKings = [roomSlotCount]bool{}
	r.RemainingLives = [roomSlotCount]byte{}
	r.HeroSlots = [roomSlotCount]bool{}

	active := make([]byte, 0, r.Users)
	activeSlots := make(map[byte]struct{}, r.Users)
	for slot, session := range r.Slots {
		// A member on the room screen has no body on the map and a late
		// joiner sits this round out; crowning either as King left a wolf
		// nobody could reach and a round that never ended.
		if !r.inRoundLocked(slot, session) {
			continue
		}
		session.mu.RLock()
		team := session.RoomTeam
		session.mu.RUnlock()
		if team != 1 && team != 2 {
			continue
		}
		active = append(active, byte(slot))
		activeSlots[byte(slot)] = struct{}{}
		if hero {
			r.RemainingLives[slot] = 1
		}
	}
	if wolfHunt {
		// Wolf Hunt starts with one or two Kings and assigns all other active
		// players to the human team.
		shuffled := append([]byte(nil), active...)
		rand.Shuffle(len(shuffled), func(i, j int) {
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		})
		kingCount := wolfHuntKingCount(len(shuffled))
		kingSlots := make(map[byte]struct{}, kingCount)
		for index := 0; index < kingCount; index++ {
			slot := shuffled[index]
			kingSlots[slot] = struct{}{}
			r.WolfKings[slot] = true
		}

		var creatureTeamSlot, humanTeamSlot byte
		for slot, session := range r.Slots {
			if session == nil {
				continue
			}
			if _, activeSlot := activeSlots[byte(slot)]; !activeSlot {
				continue
			}

			team, teamSlot, role := wolfHuntHumanTeam, humanTeamSlot, byte(2)
			if _, king := kingSlots[byte(slot)]; king {
				team, teamSlot, role = wolfHuntCreatureTeam, creatureTeamSlot, 1
				creatureTeamSlot++
			} else {
				humanTeamSlot++
			}
			session.mu.Lock()
			session.RoomTeam = team
			session.RoomTeamSlot = teamSlot
			session.mu.Unlock()
			r.SpecialRoles[slot] = role
			if team == wolfHuntCreatureTeam {
				r.RemainingLives[slot] = wolfHuntKingLives
			}
		}
	}
	return r.specialRoundStateLocked(), true
}

func (r *Room) specialRoundStateLocked() SpecialRoundState {
	mode := r.Config.Mode
	state := SpecialRoundState{
		WolfHunt: mode&wolfHuntModeFlag != 0,
		Hero:     heroModeEnabled(r.Config),
	}
	for slot, session := range r.Slots {
		if session == nil {
			continue
		}
		session.mu.RLock()
		team, teamSlot := session.RoomTeam, session.RoomTeamSlot
		session.mu.RUnlock()
		if team != 1 && team != 2 {
			continue
		}
		if state.WolfHunt {
			state.Players = append(state.Players, WolfHuntTeamState{Slot: byte(slot), Team: team, TeamSlot: teamSlot})
			state.Life = append(state.Life, RemainLifeNotice{
				Slot: byte(slot), Lives: r.RemainingLives[slot], Kind: wolfHuntLifeKind,
			})
			switch {
			case r.WolfKings[slot]:
				state.Kings = append(state.Kings, byte(slot))
			case r.SpecialRoles[slot] == 1:
				state.Wolves = append(state.Wolves, byte(slot))
			}
		}
		if state.Hero {
			lives := r.RemainingLives[slot]
			heroState := byte(0)
			if r.HeroSlots[slot] {
				heroState = heroLifeKind
				state.Heroes = append(state.Heroes, HeroSelection{Slot: byte(slot), Team: team})
			}
			state.Life = append(state.Life, RemainLifeNotice{Slot: byte(slot), Lives: lives, Kind: heroState})
			state.Options = append(state.Options, HeroOptionPlayer{Slot: byte(slot), State: heroState})
		}
	}
	orderWolfHuntTeamStates(state.Players)
	return state
}

// Move the growing human team from its highest destination row downward before
// filling the smaller creature team so no player temporarily shares an occupied
// row.
func orderWolfHuntTeamStates(players []WolfHuntTeamState) {
	sort.SliceStable(players, func(i, j int) bool {
		if players[i].Team != players[j].Team {
			return players[i].Team == wolfHuntHumanTeam
		}
		if players[i].TeamSlot != players[j].TeamSlot {
			return players[i].TeamSlot > players[j].TeamSlot
		}
		return players[i].Slot > players[j].Slot
	})
}

func wolfHuntKingCount(playerCount int) int {
	if playerCount == 0 {
		return 0
	}
	if playerCount >= 8 {
		return 2
	}
	return 1
}

type HeroLifeUpdate struct {
	Slot   byte
	Team   byte
	Active bool
	Lives  byte
	Kind   byte
}

// RecordHeroDeath applies Hero Deathmatch's handicap after the normal score
// update. A dead normal player on the trailing team becomes a Hero when the
// point gap permits another one. Existing Heroes remain Heroes while their
// team's current three-point allowance still includes them; excess Heroes
// return to normal on death.
func (r *Room) RecordHeroDeath(victim *Session) (life HeroLifeUpdate, selected, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if victim == nil || !heroModeEnabled(r.Config) || r.LastSpecialRound != int(r.Round) {
		return HeroLifeUpdate{}, false, false
	}
	victim.mu.RLock()
	slot, team := victim.RoomSlot, victim.RoomTeam
	victim.mu.RUnlock()
	if slot < 0 || slot >= roomSlotCount || r.Slots[slot] != victim || r.RemainingLives[slot] == 0 {
		return HeroLifeUpdate{}, false, false
	}
	heroCount := r.heroCountLocked(team)
	heroAllowance := r.heroAllowanceLocked(team)
	if r.HeroSlots[slot] {
		if heroCount > heroAllowance {
			r.HeroSlots[slot] = false
		}
	} else if heroCount < heroAllowance {
		r.HeroSlots[slot] = true
		selected = true
	}
	kind := byte(0)
	if r.HeroSlots[slot] {
		kind = heroLifeKind
	}
	// Keep a one-shot death token for Hero Deathmatch's optional assisted
	// REVIVALUSER path. Ordinary self-respawn is performed locally and
	// synchronized through the field-message channel.
	r.Dead[slot] = true
	return HeroLifeUpdate{
		Slot: byte(slot), Team: team, Active: true,
		Lives: r.RemainingLives[slot], Kind: kind,
	}, selected, true
}

func (r *Room) heroCountLocked(team byte) int {
	count := 0
	for slot, hero := range r.HeroSlots {
		if !hero || r.Slots[slot] == nil {
			continue
		}
		r.Slots[slot].mu.RLock()
		heroTeam := r.Slots[slot].RoomTeam
		r.Slots[slot].mu.RUnlock()
		if heroTeam == team {
			count++
		}
	}
	return count
}

func (r *Room) heroAllowanceLocked(team byte) int {
	var trailing, leading uint16
	switch team {
	case redTeam:
		trailing, leading = r.RedScore, r.BlueScore
	case blueTeam:
		trailing, leading = r.BlueScore, r.RedScore
	default:
		return 0
	}
	if leading < trailing+3 {
		return 0
	}
	// Add one Hero for each three-point deficit.
	return int((leading - trailing) / 3)
}

func (r *Room) EnablePrivateLadder() {
	r.mu.Lock()
	r.PrivateLadder = true
	r.mu.Unlock()
}

func (r *Room) IsPrivateLadder() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.PrivateLadder
}

func (r *Room) LadderRoundPlayers() ([]LadderRoundPlayer, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.PrivateLadder {
		return nil, false
	}
	players := make([]LadderRoundPlayer, 0, r.Users)
	for slot, session := range r.Slots {
		if session == nil {
			continue
		}
		total := r.MatchStats[slot].Kills
		roundScore := total - r.LadderKillTotals[slot]
		r.LadderKillTotals[slot] = total
		players = append(players, LadderRoundPlayer{
			Slot:       byte(slot),
			RoundScore: uint16(min(roundScore, uint32(^uint16(0)))),
			TotalScore: total,
		})
	}
	return players, true
}

func (r *Room) PromoteBots() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.LastBotSpawnRound == int(r.Round) {
		return nil
	}
	r.LastBotSpawnRound = int(r.Round)
	var slots []byte
	for index, session := range r.Slots {
		if session == nil || !session.IsBot {
			continue
		}
		session.mu.Lock()
		session.RoomStatus = 3
		session.mu.Unlock()
		slots = append(slots, byte(index))
	}
	return slots
}

// SetTitle renames the room (Pride Battle "A vs B" titles).
func (r *Room) SetTitle(title string) {
	r.mu.Lock()
	r.Config.Title = title
	r.mu.Unlock()
}

func (r *Room) UpdateConfig(config RoomConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// The mode word includes room options above its low byte. Only the AI marker
	// is server-owned.
	config.Mode = (config.Mode &^ aiModeFlag) | (r.Config.Mode & aiModeFlag)
	r.Config = config
	r.notifyJoinabilityLocked()
}

func (r *Room) notifyJoinabilityLocked() {
	if r.joinabilityChanged != nil {
		r.joinabilityChanged(roomAllowsQuickJoin(r.Open, r.Config, r.Users))
	}
}

type DeathResult struct {
	Accepted            bool
	Slot                byte
	RedScore            uint16
	BlueScore           uint16
	WinTeam             byte
	FinishRound         bool
	EndGame             bool
	WolfHuntConverted   bool
	WolfHuntTeam        byte
	WolfHuntLife        RemainLifeNotice
	WolfHuntLifeChanged bool
	SpecialRound        SpecialRoundState
}

func (r *Room) RecordDeath(session *Session) DeathResult {
	return r.recordDeath(nil, session)
}

func (r *Room) RecordKill(killer, victim *Session) DeathResult {
	return r.recordDeath(killer, victim)
}

func (r *Room) recordDeath(killer, victim *Session) DeathResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	if victim == nil {
		return DeathResult{}
	}
	victim.mu.RLock()
	slot, team := byte(victim.RoomSlot), victim.RoomTeam
	victim.mu.RUnlock()
	mode := byte(r.Config.Mode)
	wolfHunt := r.Config.Mode&wolfHuntModeFlag != 0
	result := DeathResult{
		Accepted:  !wolfHunt,
		Slot:      slot,
		RedScore:  r.RedScore,
		BlueScore: r.BlueScore,
	}
	if wolfHunt {
		if !r.GameStarted || !r.RoundStarted || int(slot) >= len(r.Slots) || r.Slots[slot] != victim || r.Dead[slot] {
			return result
		}
		result.Accepted = true
		r.Dead[slot] = true
	}
	killerTeam := byte(0)
	if killer != nil && killer != victim {
		killer.mu.RLock()
		killerTeam = killer.RoomTeam
		killerSlot := killer.RoomSlot
		killer.mu.RUnlock()
		if killerSlot >= 0 && killerSlot < roomSlotCount {
			r.MatchStats[killerSlot].Kills++
		}
	}
	if int(slot) < len(r.MatchStats) {
		r.MatchStats[slot].Deaths++
	}
	// Crystal point modes also score a point per kill (user report; the
	// PLAYERDEAD reply only carries the scores the server keeps).
	deathMatch := mode == 2 || mode == 18 || mode == 10 || r.Config.Mode&crystalModeFlag != 0
	iceHold := mode == 32 || mode == 48
	destruction := mode == 4 || mode == 20 || mode == 132
	if wolfHunt {
		// Revolt and Counterattack infect humans killed by wolves. A converted
		// normal wolf gets one life; a Wolf King keeps its three-life allowance.
		// Dominate converts either kind back to human only after its final life.
		// A team transition clears the King flag and updates the live roster.
		convertedTeam, convertedRole := byte(0), byte(0)
		if team == wolfHuntCreatureTeam {
			if r.RemainingLives[slot] > 0 {
				r.RemainingLives[slot]--
			}
			result.WolfHuntLife = RemainLifeNotice{
				Slot: slot, Lives: r.RemainingLives[slot], Kind: wolfHuntLifeKind,
			}
			result.WolfHuntLifeChanged = true
		}
		// Every human death infects, not only a kill by a wolf. A fall, a
		// self-blast or friendly fire arrives with no wolf killer (the engine
		// reports the victim as its own damager); leaving that human dead but
		// still on the human side kept the side "alive" until the round clock,
		// which Wolf Hunt awards to the humans.
		if team == wolfHuntHumanTeam {
			convertedTeam, convertedRole = wolfHuntCreatureTeam, 1
		} else if team == wolfHuntCreatureTeam && killerTeam == wolfHuntHumanTeam && killer != victim &&
			r.Config.Mode&wolfHuntDominate != 0 && r.RemainingLives[slot] == 0 {
			convertedTeam, convertedRole = wolfHuntHumanTeam, 2
		}
		if convertedTeam != 0 {
			victim.mu.Lock()
			victim.RoomTeam = convertedTeam
			victim.mu.Unlock()
			r.WolfKings[slot] = false
			r.SpecialRoles[slot] = convertedRole
			if convertedTeam == wolfHuntCreatureTeam {
				r.RemainingLives[slot] = wolfHuntWolfLives
			} else {
				r.RemainingLives[slot] = 0
			}
			r.compactTeamSlotsLocked()
			result.WolfHuntConverted = true
			result.WolfHuntTeam = convertedTeam
			result.WolfHuntLife = RemainLifeNotice{
				Slot: slot, Lives: r.RemainingLives[slot], Kind: wolfHuntLifeKind,
			}
			result.WolfHuntLifeChanged = true
			result.SpecialRound = r.specialRoundStateLocked()
		}
		// The client brings a player with a life left back on its own timer and
		// never sends 0x1662 for it (0x1662 is only the team-mate rescue).
		// Holding the death claim would drop that player's next death report.
		// Only a creature that has spent its last life stays down: it watches
		// the rest of the round.
		victim.mu.RLock()
		currentTeam := victim.RoomTeam
		victim.mu.RUnlock()
		if currentTeam != wolfHuntCreatureTeam || r.RemainingLives[slot] > 0 {
			r.Dead[slot] = false
		}
		if r.wolfHuntActiveTeamCountLocked(wolfHuntHumanTeam) == 0 {
			r.RedScore++
			result.FinishRound, result.WinTeam = true, wolfHuntCreatureTeam
			result.EndGame = r.RedScore >= uint16(r.Config.Wins)
		} else if r.wolfHuntActiveTeamCountLocked(wolfHuntCreatureTeam) == 0 || r.wolfHuntWolvesDefeatedLocked() {
			// A wolf remains in the round while it has role-specific lives.
			// Dominate can instead remove the creature team through conversion.
			r.BlueScore++
			result.FinishRound, result.WinTeam = true, wolfHuntHumanTeam
			result.EndGame = r.BlueScore >= uint16(r.Config.Wins)
		}
		result.RedScore, result.BlueScore = r.RedScore, r.BlueScore
		return result
	}

	if team == 2 {
		if deathMatch {
			r.recordDeathMatchPointLocked(killerTeam, 1, &result)
		}
		if iceHold || destruction {
			if int(slot) < len(r.Dead) {
				r.Dead[slot] = true
			}
			if r.teamDeadLocked(team, destruction) {
				r.RedScore++
				result.FinishRound, result.WinTeam = true, 1
				result.EndGame = r.isMatchCompleteLocked(1)
			}
		}
	} else if team == 1 {
		if deathMatch {
			r.recordDeathMatchPointLocked(killerTeam, 2, &result)
		}
		if iceHold || destruction {
			if int(slot) < len(r.Dead) {
				r.Dead[slot] = true
			}
			if r.teamDeadLocked(team, destruction) {
				r.BlueScore++
				result.FinishRound, result.WinTeam = true, 2
				result.EndGame = r.isMatchCompleteLocked(2)
			}
		}
	}
	result.RedScore, result.BlueScore = r.RedScore, r.BlueScore
	return result
}

func (r *Room) wolfHuntActiveTeamCountLocked(team byte) int {
	count := 0
	for index, session := range r.Slots {
		if !r.inRoundLocked(index, session) {
			continue
		}
		session.mu.RLock()
		matches := session.RoomTeam == team
		session.mu.RUnlock()
		if matches {
			count++
		}
	}
	return count
}

func (r *Room) wolfHuntWolvesDefeatedLocked() bool {
	found := false
	for slot, session := range r.Slots {
		if !r.inRoundLocked(slot, session) {
			continue
		}
		session.mu.RLock()
		wolf := session.RoomTeam == wolfHuntCreatureTeam
		session.mu.RUnlock()
		if !wolf {
			continue
		}
		found = true
		if !r.Dead[slot] || r.RemainingLives[slot] > 0 {
			return false
		}
	}
	return found
}

func (r *Room) compactTeamSlotsLocked() {
	var next [3]byte
	for _, team := range []byte{1, 2} {
		for _, session := range r.Slots {
			if session == nil {
				continue
			}
			session.mu.Lock()
			if session.RoomTeam == team {
				session.RoomTeamSlot = next[team]
				next[team]++
			}
			session.mu.Unlock()
		}
	}
}

func (r *Room) recordDeathMatchPointLocked(killerTeam, fallbackTeam byte, result *DeathResult) {
	scoringTeam := killerTeam
	if scoringTeam != 1 && scoringTeam != 2 {
		scoringTeam = fallbackTeam
	}
	if scoringTeam == 1 {
		r.RedScore++
		if r.RedScore >= uint16(r.Config.Points) {
			result.FinishRound, result.EndGame, result.WinTeam = true, true, 1
		}
		return
	}
	r.BlueScore++
	if r.BlueScore >= uint16(r.Config.Points) {
		result.FinishRound, result.EndGame, result.WinTeam = true, true, 2
	}
}

func (r *Room) teamDeadLocked(team byte, destruction bool) bool {
	if destruction && !r.PlantTime.IsZero() && team == 1 {
		return false
	}
	count := 0
	for index, session := range r.Slots {
		if session == nil {
			continue
		}
		session.mu.RLock()
		matches := session.RoomTeam == team
		inPlay := session.IsBot || session.RoomStatus == activeRoomStatus
		session.mu.RUnlock()
		// Only an active slot has a body that can die. A member still on the
		// room screen (status 0/1) or parked in the spectator shell (3) can
		// never be killed, so counting him kept the team alive forever.
		if matches && inPlay && !r.Dead[index] {
			count++
		}
	}
	return count == 0
}

// inFieldStatus reports whether a cached room status means the client has
// committed that slot to the running match (3 spectator/loading, 4 active).
func inFieldStatus(status byte) bool {
	return status == spectatorRoomStatus || status == activeRoomStatus
}

// inRoundLocked reports whether a slot takes part in the running round: in the
// field and not a late joiner waiting for the next round. Bots are placed in
// the field by the server itself.
func (r *Room) inRoundLocked(slot int, session *Session) bool {
	if session == nil || r.LateJoined[slot] {
		return false
	}
	if session.IsBot {
		return true
	}
	session.mu.RLock()
	status := session.RoomStatus
	session.mu.RUnlock()
	return inFieldStatus(status)
}

// InField reports whether the session is in the running match's field.
func (r *Room) InField(session *Session) bool {
	if session == nil {
		return false
	}
	if session.IsBot {
		return true
	}
	session.mu.RLock()
	defer session.mu.RUnlock()
	return inFieldStatus(session.RoomStatus)
}

// MarkLateJoined records that a slot reached the field after its round began,
// so it sits the round out. Add sets the same flag for a member who walked in
// mid-match; this covers a member who sat in the room screen since the start.
func (r *Room) MarkLateJoined(slot byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if int(slot) < len(r.LateJoined) && r.RoundStarted {
		r.LateJoined[slot] = true
		// Ice Hold ("Uyku Modu") decides rounds by elimination and lets a
		// team-mate stand a sleeping player up (0x1662). A player walking into
		// a running Ice Hold round arrives as one of the downed.
		if mode := byte(r.Config.Mode); mode == 32 || mode == 48 {
			r.Dead[slot] = true
		}
	}
}

// ClearLateJoined drops the late-joiner mark of a slot that plays this round.
func (r *Room) ClearLateJoined(slot byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if int(slot) < len(r.LateJoined) {
		r.LateJoined[slot] = false
	}
}

// MasterFieldStatus returns the master's slot and status while the master is
// in the running match's field.
func (r *Room) MasterFieldStatus() (slot, status byte, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.GameStarted || int(r.Master) >= len(r.Slots) || r.Slots[r.Master] == nil {
		return 0, 0, false
	}
	master := r.Slots[r.Master]
	master.mu.RLock()
	status = master.RoomStatus
	master.mu.RUnlock()
	if !inFieldStatus(status) {
		return 0, 0, false
	}
	return r.Master, status, true
}

// TeamChangeRefusal returns the CHANGETEAM result code that refuses a team
// change, or 0: 184 while the member is ready (stringtable 4184), 181 while he
// is in the running match (4181).
func (r *Room) TeamChangeRefusal(session *Session) byte {
	r.mu.RLock()
	gameStarted := r.GameStarted
	r.mu.RUnlock()
	session.mu.RLock()
	status := session.RoomStatus
	session.mu.RUnlock()
	switch {
	case gameStarted && inFieldStatus(status):
		return 181
	case !gameStarted && status == readyRoomStatus:
		return 184
	}
	return 0
}

// PromoteSpectator lifts a slot parked on the spectator status to active and
// clears its late-joiner mark (an Ice Hold late joiner revived by a
// team-mate). It reports whether anything changed.
func (r *Room) PromoteSpectator(slot byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if int(slot) >= len(r.Slots) || r.Slots[slot] == nil {
		return false
	}
	session := r.Slots[slot]
	session.mu.Lock()
	spectating := session.RoomStatus == spectatorRoomStatus && !session.IsBot && r.LateJoined[slot]
	if spectating {
		session.RoomStatus = activeRoomStatus
	}
	session.mu.Unlock()
	if spectating {
		r.LateJoined[slot] = false
	}
	return spectating
}

func (r *Room) Revive(session *Session) {
	if session == nil {
		return
	}
	session.mu.RLock()
	slot := session.RoomSlot
	session.mu.RUnlock()
	if slot >= 0 && slot < roomSlotCount {
		r.ReviveSlot(byte(slot))
	}
}

func (r *Room) ReviveSlot(slot byte) (team, teamSlot byte, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.GameStarted || !r.RoundStarted || int(slot) >= len(r.Slots) {
		return 0, 0, false
	}
	session := r.Slots[slot]
	if session == nil {
		return 0, 0, false
	}
	session.mu.RLock()
	team, teamSlot = session.RoomTeam, session.RoomTeamSlot
	session.mu.RUnlock()
	if team != 1 && team != 2 {
		return 0, 0, false
	}
	if heroModeEnabled(r.Config) && !r.Dead[slot] {
		return 0, 0, false
	}
	// Wolf Hunt releases the death claim as soon as a slot with a life left
	// respawns on its own, so only the life rule still holds: a creature that
	// has spent its last life stays out for the round.
	if r.Config.Mode&wolfHuntModeFlag != 0 && team == wolfHuntCreatureTeam && r.RemainingLives[slot] == 0 {
		return 0, 0, false
	}
	r.Dead[slot] = false
	return team, teamSlot, true
}

func (r *Room) FinishRound(endGame bool) (round byte, redScore, blueScore uint16, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.RoundStarted {
		return 0, 0, 0, false
	}
	r.Round++
	round, redScore, blueScore = r.Round, r.RedScore, r.BlueScore
	r.Dead = [roomSlotCount]bool{}
	r.PlantTime = time.Time{}
	r.BombPlanter = 0xff
	r.kickVote = KickVote{}
	r.RoundStarted = false
	if endGame {
		r.GameStarted = false
	}
	return round, redScore, blueScore, true
}

func (r *Room) CompleteRoundDelay(endGame bool, now time.Time) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !endGame {
		r.RoundStartedAt = now
		r.RoundStarted = true
		r.Flags = [5]byte{}
		r.PlantTime = time.Time{}
		r.BombPlanter = 0xff
		r.Dead = [roomSlotCount]bool{}
		promoted := make([]byte, 0, len(r.Slots))
		for slot, session := range r.Slots {
			if session == nil {
				continue
			}
			session.mu.Lock()
			// A member still on the room screen never loaded the field; he
			// enters when he presses Start (ENTERGAME) himself. Announcing
			// him as active (0x1617 status 4) made every client in the field
			// create a body nobody controls (sub_342E1B60 -> sub_342E04B0).
			if !session.IsBot && !inFieldStatus(session.RoomStatus) {
				session.mu.Unlock()
				continue
			}
			wasSpectator := session.RoomStatus != activeRoomStatus || r.LateJoined[slot]
			session.RoomStatus = activeRoomStatus
			session.mu.Unlock()
			if wasSpectator {
				promoted = append(promoted, byte(slot))
			}
		}
		r.LateJoined = [roomSlotCount]bool{}
		return promoted
	}
	for _, session := range r.Slots {
		if session == nil {
			continue
		}
		session.mu.Lock()
		session.RoomStatus = 0
		session.mu.Unlock()
	}
	return nil
}

func (r *Room) ForfeitEmptyTeam(team byte) (winTeam byte, endGame, triggered bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.GameStarted || !r.RoundStarted || team != 1 && team != 2 {
		return 0, false, false
	}
	if r.Config.Mode&wolfHuntModeFlag != 0 {
		if r.wolfHuntActiveTeamCountLocked(team) != 0 {
			return 0, false, false
		}
		if team == wolfHuntCreatureTeam {
			r.BlueScore++
			return wolfHuntHumanTeam, r.BlueScore >= uint16(r.Config.Wins), true
		}
		r.RedScore++
		return wolfHuntCreatureTeam, r.RedScore >= uint16(r.Config.Wins), true
	}
	if r.teamCountLocked(team) != 0 {
		return 0, false, false
	}
	if team == 2 {
		r.RedScore++
		endGame = r.Config.Mode&wolfHuntModeFlag == 0 || r.RedScore >= uint16(r.Config.Wins)
		return 1, endGame, true
	}
	r.BlueScore++
	endGame = r.Config.Mode&wolfHuntModeFlag == 0 || r.BlueScore >= uint16(r.Config.Wins)
	return 2, endGame, true
}

func (r *Room) RoundTimeout(now time.Time) (winTeam byte, endGame, triggered bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.RoundStarted || !r.GameStarted {
		return 0, false, false
	}
	// Once a destruction-mode bomb is planted, its fuse replaces the normal
	// round clock. Extending RoundStartedAt by an arbitrary amount can still
	// let the round timer win the race against a late plant.
	if r.isDestructionLocked() && !r.PlantTime.IsZero() {
		return 0, false, false
	}
	if now.Sub(r.RoundStartedAt) <= time.Duration(r.Config.Time)*time.Second {
		return 0, false, false
	}
	if r.Config.Mode&wolfHuntModeFlag != 0 {
		// Surviving humans win the round when the hunt clock expires. Wolf Hunt
		// is layered over base deathmatch mode 2, but it is a wins-based round
		// mode and must not use deathmatch's match-ending timeout path.
		r.BlueScore++
		return wolfHuntHumanTeam, r.BlueScore >= uint16(r.Config.Wins), true
	}
	mode := byte(r.Config.Mode)
	if mode == 2 || mode == 18 || mode == 10 {
		if r.RedScore > r.BlueScore {
			return 1, true, true
		}
		if r.BlueScore > r.RedScore {
			return 2, true, true
		}
		return 0, true, true
	}
	if mode == 4 || mode == 20 || mode == 132 || (r.Config.Mode&0x04 != 0) {
		defender := r.destructionDefenderTeamLocked()
		if defender == 1 {
			r.RedScore++
		} else {
			r.BlueScore++
		}
		return defender, r.isMatchCompleteLocked(defender), true
	}
	if mode == 32 || mode == 48 {
		redDead, blueDead := r.deadCountLocked(1), r.deadCountLocked(2)
		if redDead < blueDead {
			r.RedScore++
		} else if blueDead < redDead {
			r.BlueScore++
		}
		endGame = r.RedScore >= uint16(r.Config.Wins) || r.BlueScore >= uint16(r.Config.Wins)
		if r.RedScore >= r.BlueScore {
			return 1, endGame, true
		}
		return 2, endGame, true
	}
	if mode == 1 || mode == 17 || mode == 9 || mode == 129 {
		blueFlags, redFlags := r.flagCountLocked(1), r.flagCountLocked(2)
		if redFlags > blueFlags {
			r.RedScore++
		} else if blueFlags > redFlags {
			r.BlueScore++
		}
		endGame = r.RedScore >= uint16(r.Config.Wins) || r.BlueScore >= uint16(r.Config.Wins)
		if r.RedScore >= r.BlueScore {
			return 1, endGame, true
		}
		return 2, endGame, true
	}
	return 0, false, false
}

// The Destruction roles stay with the team numbers. The client's role value
// (room +240) is written only by 0x16A9 CHANGEROLE_ACK (sub_34182F20), which
// the server never sends, and sub_340A4120 derives the local role from it and
// the player's team number. The halftime 0x1659 moves the players between the
// teams (sub_3423E080), so the people change roles, not the team numbers.
func (r *Room) destructionAttackerTeamLocked() byte {
	return 1
}

func (r *Room) destructionDefenderTeamLocked() byte {
	return 2
}

// destructionSideSwitchFlag is the 2013 create dialog's "TeamChangeRoleOption"
// check box (sub_342B1E50): weapon-option bit 0x04, offered for modes with
// bits 0x25. In Spat and Pride Battle channels (types 1 and 2) the client
// always announces the side change instead (2013 sub_342E2900, 2012 and 2011
// ENDROUND_ACK); the 2012 and 2011 dialogs have no such option.
const destructionSideSwitchFlag = byte(0x04)

func (r *Room) switchTeamEnabledLocked() bool {
	return r.isDestructionLocked() && r.Config.Wins > 1 &&
		(r.Config.WeaponFlags&destructionSideSwitchFlag != 0 || r.sideSwitchChannel)
}

// SetSideSwitchChannel marks a room created in a Spat or Pride Battle channel,
// where Destruction always swaps sides at halftime.
func (r *Room) SetSideSwitchChannel(enabled bool) {
	r.mu.Lock()
	r.sideSwitchChannel = enabled
	r.mu.Unlock()
}

// CS_FD_ENDROUND_ACK (sub_342E2900) announces the side change (stringtable
// 1102) when the round that just ended (room +63, set by 0x1625) is 5, and the
// random decider (1104) when that round is 10 with equal wins. The 0x1659
// handler (sub_342E2E10) closes the round HUD in exactly those two states.
const (
	destructionHalftimeRound = 5
	destructionDeciderRound  = 10
)

func (r *Room) isMatchCompleteLocked(winningTeam byte) bool {
	if winningTeam == 1 {
		return r.RedScore >= uint16(r.Config.Wins)
	}
	return r.BlueScore >= uint16(r.Config.Wins)
}

func (r *Room) CheckAndTriggerSwitchTeam() (switched bool, redScore, blueScore uint16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.GameStarted || !r.switchTeamEnabledLocked() {
		return false, 0, 0
	}
	// FinishRound has already advanced Round to the round about to start.
	ended := int(r.Round) - 1
	swap := ended == destructionHalftimeRound && !r.SwitchedSides
	if ended == destructionDeciderRound && r.RedScore == r.BlueScore {
		swap = rand.IntN(2) == 1
	}
	if swap {
		r.SwitchedSides = true

		// The client swaps every slot's team on 0x1659 (sub_3423E080), so the
		// following ReplyRestartRound carries the teams it already has.
		for _, session := range r.Slots {
			if session == nil {
				continue
			}
			session.mu.Lock()
			if session.RoomTeam == 1 {
				session.RoomTeam = 2
			} else if session.RoomTeam == 2 {
				session.RoomTeam = 1
			}
			session.mu.Unlock()
		}

		// The 0x1659 handler also swaps the two win counts (+81/+216) and
		// scores (+234/+236).
		r.RedScore, r.BlueScore = r.BlueScore, r.RedScore

		return true, r.RedScore, r.BlueScore
	}
	return false, 0, 0
}

func (r *Room) BombTimeout(now time.Time) (winTeam byte, endGame, triggered bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.PlantTime.IsZero() || now.Sub(r.PlantTime) <= bombFuseSeconds*time.Second {
		return 0, false, false
	}
	attackerTeam := r.destructionAttackerTeamLocked()
	endGame = r.resolveBombLocked(attackerTeam, -1)
	return attackerTeam, endGame, true
}

func (r *Room) RecordBombPlant(reporter *Session, actor byte, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if reporter == nil || !r.GameStarted || !r.RoundStarted || !r.isDestructionLocked() || !r.PlantTime.IsZero() {
		return false
	}
	reporter.mu.RLock()
	reporterSlot := reporter.RoomSlot
	reporter.mu.RUnlock()
	if reporterSlot < 0 || reporterSlot >= roomSlotCount || r.Slots[reporterSlot] != reporter || int(actor) >= roomSlotCount {
		return false
	}
	planter := r.Slots[actor]
	if planter == nil {
		return false
	}
	planter.mu.RLock()
	planterSlot, planterTeam := planter.RoomSlot, planter.RoomTeam
	planter.mu.RUnlock()
	attackerTeam := r.destructionAttackerTeamLocked()
	if planterSlot != int16(actor) || planterTeam != attackerTeam || r.Dead[actor] {
		return false
	}
	r.PlantTime = now
	r.BombPlanter = actor
	r.MatchStats[actor].Plants++
	return true
}

func (r *Room) ResolveBombRemoval(reporter *Session, resultCode, actor byte, now time.Time) (winTeam byte, endGame, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if reporter == nil || (resultCode != 1 && resultCode != 2) || r.PlantTime.IsZero() || !r.GameStarted || !r.RoundStarted || !r.isDestructionLocked() {
		return 0, false, false
	}
	reporter.mu.RLock()
	reporterSlot := reporter.RoomSlot
	reporter.mu.RUnlock()
	if reporterSlot < 0 || reporterSlot >= roomSlotCount || r.Slots[reporterSlot] != reporter {
		return 0, false, false
	}

	defenderTeam := r.destructionDefenderTeamLocked()

	// A bomb-object result uses actor 0xff: the authority client computes the
	// winner from its peer message 12 (sub_34241630: 1 -> team 1, 0 -> team 2)
	// and names it in resultCode. It is accepted only after the server-side
	// fuse has elapsed, and credits no defuse.
	if actor == 0xff {
		if now.Sub(r.PlantTime) < bombFuseSeconds*time.Second {
			return 0, false, false
		}
		endGame = r.resolveBombLocked(resultCode, -1)
		return resultCode, endGame, true
	}

	// Validate the actor named by the packet instead of assuming the reporter is
	// the defuser.
	if int(actor) >= roomSlotCount {
		return 0, false, false
	}
	defuser := r.Slots[actor]
	if defuser == nil {
		return 0, false, false
	}
	defuser.mu.RLock()
	defuserSlot, defuserTeam := defuser.RoomSlot, defuser.RoomTeam
	defuser.mu.RUnlock()
	if defuserSlot != int16(actor) || defuserTeam != defenderTeam || r.Dead[actor] {
		return 0, false, false
	}
	endGame = r.resolveBombLocked(defenderTeam, int16(actor))
	return defenderTeam, endGame, true
}

func (r *Room) resolveBombLocked(winTeam byte, defuserSlot int16) bool {
	r.PlantTime = time.Time{}
	r.BombPlanter = 0xff
	if defuserSlot >= 0 && defuserSlot < roomSlotCount {
		r.MatchStats[defuserSlot].Defuses++
	}
	if winTeam == 1 {
		r.RedScore++
		return r.isMatchCompleteLocked(1)
	}
	r.BlueScore++
	return r.isMatchCompleteLocked(2)
}

// Crystal (Kristal) point mode, e.g. ladder 0x2408/0x2410.
const (
	// crystalModeFlag is the room mode bit of the crystal HUD (sub_342EC0B0).
	crystalModeFlag = 0x2000
	// crystalPoints is what a broken crystal is worth: the HUD shows a fixed
	// "+70 Points" (sub_342EC0B0, sub_34165250(70)).
	crystalPoints = 70
)

// RecordTowerDestroyed applies CS_FD_DESTROY_TOWER_REQ (sub_342E1480): the
// room master reports room +82, the team whose crystal broke (1 RED, 2 Blue;
// the HUD shows "%s takımın elması kırıldı"), and the other team scores 70.
func (r *Room) RecordTowerDestroyed(reporter *Session, brokenTeam byte) (redScore, blueScore uint16, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if reporter == nil || !r.GameStarted || !r.RoundStarted {
		return 0, 0, false
	}
	reporter.mu.RLock()
	slot := reporter.RoomSlot
	reporter.mu.RUnlock()
	if slot < 0 || slot >= roomSlotCount || r.Slots[slot] != reporter {
		return 0, 0, false
	}
	switch brokenTeam {
	case 1:
		r.BlueScore += crystalPoints
	case 2:
		r.RedScore += crystalPoints
	default:
		return 0, 0, false
	}
	return r.RedScore, r.BlueScore, true
}

// Flag events carried by FLAGPOINTGET_REQ 0x168A {u8 slot, u8 event}. The
// game module (object.lto) sends them; it is VMProtect-packed, so the codes
// come from a live 2012 capture-the-flag test: 1 when the enemy flag is
// picked up, 4 when it is brought to the own base. On the capture the client
// shows "... 8 Puan Kazandınız!" itself.
const (
	flagEventPickup  byte = 1
	flagEventCapture byte = 4
)

// flagEventPoints is the team score each flag event is worth.
var flagEventPoints = map[byte]int{
	flagEventPickup:  1,
	flagEventCapture: 8,
}

// RecordFlagPoint scores a flag event for the actor's team. It returns the
// actor's personal flag points and the team's new score (the two values of
// FLAGPOINTGET_ACK), the team, and whether the team reached the room's
// point target.
func (r *Room) RecordFlagPoint(reporter *Session, actor, event byte) (personalPoints, team, teamPoints byte, reachedTarget, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	points, known := flagEventPoints[event]
	if reporter == nil || !known || !r.GameStarted || !r.RoundStarted || int(actor) >= roomSlotCount {
		return 0, 0, 0, false, false
	}
	reporter.mu.RLock()
	reporterSlot := reporter.RoomSlot
	reporter.mu.RUnlock()
	if reporterSlot < 0 || reporterSlot >= roomSlotCount || r.Slots[reporterSlot] != reporter {
		return 0, 0, 0, false, false
	}
	player := r.Slots[actor]
	if player == nil {
		return 0, 0, 0, false, false
	}
	player.mu.RLock()
	playerSlot, playerTeam := player.RoomSlot, player.RoomTeam
	player.mu.RUnlock()
	if playerSlot != int16(actor) || (playerTeam != 1 && playerTeam != 2) || r.Dead[actor] {
		return 0, 0, 0, false, false
	}
	r.FlagPoints[actor] = byte(min(int(r.FlagPoints[actor])+points, 0xff))
	score := &r.RedScore
	if playerTeam == 2 {
		score = &r.BlueScore
	}
	*score += uint16(points)
	reachedTarget = r.Config.Points > 0 && *score >= uint16(r.Config.Points)
	return r.FlagPoints[actor], playerTeam, byte(min(*score, 0xff)), reachedTarget, true
}

func (r *Room) isDestructionLocked() bool {
	mode := byte(r.Config.Mode)
	return mode == 4 || mode == 20 || mode == 132
}

func (r *Room) isIceHoldLocked() bool {
	mode := byte(r.Config.Mode)
	return mode == 32 || mode == 48
}

// LastPlayers returns the sole living red and blue players.
func (r *Room) LastPlayers() (redSlot byte, red *Session, blueSlot byte, blue *Session, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.GameStarted || !r.RoundStarted {
		return 0xff, nil, 0xff, nil, false
	}
	redSlot, blueSlot = 0xff, 0xff
	redCount, blueCount := 0, 0
	for index, session := range r.Slots {
		if session == nil || r.Dead[index] {
			continue
		}
		session.mu.RLock()
		team := session.RoomTeam
		session.mu.RUnlock()
		switch team {
		case 1:
			redSlot, red = byte(index), session
			redCount++
		case 2:
			blueSlot, blue = byte(index), session
			blueCount++
		}
	}
	return redSlot, red, blueSlot, blue, redCount == 1 && blueCount == 1
}

// StartKickVote begins the ten-second in-game vote. The initiator is a normal
// eligible voter; no automatic agreement or target vote is recorded.
func (r *Room) StartKickVote(initiator *Session, targetSlot, reason byte, now time.Time) (byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if initiator == nil || initiator.IsBot || !r.GameStarted || !r.RoundStarted || r.kickVote.Active || reason > 4 || int(targetSlot) >= roomSlotCount {
		return 0xff, false
	}
	initiator.mu.RLock()
	initiatorSlot := initiator.RoomSlot
	initiator.mu.RUnlock()
	if initiatorSlot < 0 || initiatorSlot >= roomSlotCount || r.Slots[initiatorSlot] != initiator || byte(initiatorSlot) == targetSlot {
		return 0xff, false
	}
	target := r.Slots[targetSlot]
	if target == nil || target.IsBot {
		return 0xff, false
	}
	r.kickVote = KickVote{
		Active:        true,
		Initiator:     byte(initiatorSlot),
		Target:        targetSlot,
		Reason:        reason,
		StartedAt:     now,
		TargetSession: target,
	}
	return byte(initiatorSlot), true
}

// CastKickVote records Agreement (1), Opposition (0), or Waiver (2). It may
// resolve immediately when a strict majority is reached or becomes impossible.
func (r *Room) CastKickVote(voter *Session, target, choice byte, now time.Time) (KickVoteStatus, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.kickVote.Active {
		return KickVoteStatus{}, false
	}
	if !now.Before(r.kickVote.StartedAt.Add(kickVoteDuration)) {
		return r.resolveKickVoteLocked(true), false
	}
	if voter == nil || voter.IsBot || target != r.kickVote.Target || choice > 2 {
		return KickVoteStatus{}, false
	}
	voter.mu.RLock()
	voterSlot := voter.RoomSlot
	voter.mu.RUnlock()
	if voterSlot < 0 || voterSlot >= roomSlotCount || r.Slots[voterSlot] != voter || byte(voterSlot) == r.kickVote.Target || r.kickVote.Voted[voterSlot] {
		return KickVoteStatus{}, false
	}
	r.kickVote.Voted[voterSlot] = true
	r.kickVote.Choices[voterSlot] = choice
	return r.resolveKickVoteLocked(false), true
}

// ExpireKickVote converts every uncast eligible vote to Waiver after ten
// seconds.
func (r *Room) ExpireKickVote(now time.Time) (KickVoteStatus, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.kickVote.Active || now.Before(r.kickVote.StartedAt.Add(kickVoteDuration)) {
		return KickVoteStatus{}, false
	}
	return r.resolveKickVoteLocked(true), true
}

func (r *Room) resolveKickVoteLocked(forceTimeout bool) KickVoteStatus {
	vote := &r.kickVote
	status := KickVoteStatus{Target: vote.Target, TargetSession: vote.TargetSession}
	targetPresent := int(vote.Target) < roomSlotCount && r.Slots[vote.Target] == vote.TargetSession
	eligible, voted := 0, 0
	for index, session := range r.Slots {
		if session == nil || session.IsBot || byte(index) == vote.Target {
			continue
		}
		eligible++
		if forceTimeout && !vote.Voted[index] {
			vote.Voted[index] = true
			vote.Choices[index] = 2
		}
		if !vote.Voted[index] {
			continue
		}
		voted++
		switch vote.Choices[index] {
		case 1:
			status.Agreement++
		case 0:
			status.Opposition++
		case 2:
			status.Waiver++
		}
	}
	majority := eligible/2 + 1
	remaining := eligible - voted
	status.Passed = targetPresent && int(status.Agreement) >= majority
	status.Resolved = !targetPresent || forceTimeout || status.Passed || int(status.Agreement)+remaining < majority || voted >= eligible
	if status.Resolved {
		r.kickVote = KickVote{}
	}
	return status
}

func (r *Room) MatchPlayers() []MatchPlayer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	players := make([]MatchPlayer, 0, r.Users)
	for slot, session := range r.Slots {
		if session == nil {
			continue
		}
		session.mu.RLock()
		team, status := session.RoomTeam, session.RoomStatus
		session.mu.RUnlock()
		// Members on the room screen are not in the match: no round entry,
		// no result record, no reward.
		if r.GameStarted && !session.IsBot && !inFieldStatus(status) {
			continue
		}
		players = append(players, MatchPlayer{
			Session: session,
			Slot:    byte(slot),
			Team:    team,
			Stats:   r.MatchStats[slot],
		})
	}
	return players
}

func (r *Room) MatchDurationSeconds(now time.Time) uint32 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.GameStartedAt.IsZero() || now.Before(r.GameStartedAt) {
		return 0
	}
	seconds := now.Sub(r.GameStartedAt) / time.Second
	if seconds > time.Duration(^uint32(0))*time.Second {
		return ^uint32(0)
	}
	return uint32(seconds)
}

func (r *Room) UpdateFlagControl(flag, team byte) (endRound, endGame bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if flag < byte(len(r.Flags)) {
		r.Flags[flag] = team
	}
	for _, owner := range r.Flags {
		if owner != r.Flags[0] {
			return false, false
		}
	}
	if team == 1 {
		r.RedScore++
	} else if team == 2 {
		r.BlueScore++
	}
	endGame = r.RedScore >= uint16(r.Config.Wins) || r.BlueScore >= uint16(r.Config.Wins)
	r.Flags = [5]byte{}
	return true, endGame
}

func (r *Room) teamCountLocked(team byte) int {
	count := 0
	for _, session := range r.Slots {
		if session == nil {
			continue
		}
		session.mu.RLock()
		matches := session.RoomTeam == team
		session.mu.RUnlock()
		if matches {
			count++
		}
	}
	return count
}

func (r *Room) deadCountLocked(team byte) int {
	count := 0
	for index, session := range r.Slots {
		if session == nil || !r.Dead[index] {
			continue
		}
		session.mu.RLock()
		matches := session.RoomTeam == team
		session.mu.RUnlock()
		if matches {
			count++
		}
	}
	return count
}

func (r *Room) flagCountLocked(team byte) int {
	count := 0
	for _, owner := range r.Flags {
		if owner == team {
			count++
		}
	}
	return count
}

func (r *Room) nextTeamSlotLocked(team byte, except *Session) byte {
	teamSlot, _ := r.availableTeamSlotLocked(team, except)
	return teamSlot
}

func (r *Room) availableTeamSlotLocked(team byte, except *Session) (byte, bool) {
	for candidate := 0; candidate < teamSlotCount; candidate++ {
		used := false
		for _, session := range r.Slots {
			if session == nil || session == except {
				continue
			}
			session.mu.RLock()
			matches := session.RoomTeam == team && session.RoomTeamSlot == byte(candidate)
			session.mu.RUnlock()
			if matches {
				used = true
				break
			}
		}
		if !used {
			return byte(candidate), true
		}
	}
	return 0, false
}

func (r *Room) Broadcast(messageID uint16, payload []byte) {
	snapshot := r.Snapshot()
	for _, session := range snapshot.Members {
		_ = session.Write(messageID, payload)
	}
}

func (r *Room) SessionAt(slot byte) *Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if int(slot) >= len(r.Slots) {
		return nil
	}
	return r.Slots[slot]
}

func (r *Room) ValidateGameRecord(reporter *Session, record GameRecordRequest) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if reporter == nil || !r.GameStarted || !r.RoundStarted || int(record.Victim) >= roomSlotCount {
		return false
	}
	reporter.mu.RLock()
	reporterSlot := reporter.RoomSlot
	reporter.mu.RUnlock()
	if reporterSlot < 0 || reporterSlot >= roomSlotCount || r.Slots[reporterSlot] != reporter || r.Slots[record.Victim] == nil {
		return false
	}
	return record.Killer == 0xff || int(record.Killer) < roomSlotCount && r.Slots[record.Killer] != nil
}

func (r *Room) IsMaster(session *Session) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if session == nil || int(r.Master) >= len(r.Slots) {
		return false
	}
	return r.Slots[r.Master] == session
}

func (r *Room) AtCapacity() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.Config.Capacity != 0 && r.Users >= r.Config.Capacity
}

func (r *Room) UsesDeathmatchRules() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	mode := byte(r.Config.Mode)
	return mode == 2 || mode == 18 || mode == 10
}

func (r *Room) UsesAIDeathmatchRules() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.Config.Mode&aiModeFlag != 0
}

// SetLadderSearch records the search of the ladder player who created the
// room, so later searchers can be sent here (FINDUSER_START_ACK room list).
func (r *Room) SetLadderSearch(creator *Session, search PrivateLadderSearchRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := search
	r.ladderSearch, r.ladderCreator = &copied, creator
}

// LadderSearch returns the creator's ladder search, if the room has one.
func (r *Room) LadderSearch() (PrivateLadderSearchRequest, *Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.ladderSearch == nil {
		return PrivateLadderSearchRequest{}, nil, false
	}
	return *r.ladderSearch, r.ladderCreator, true
}
