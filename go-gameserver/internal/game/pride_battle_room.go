package game

import (
	"strings"

	"wtserver-go/internal/store"
)

// prideBattleTitle names a Pride Battle room after its Prides: the master's
// Pride alone while nobody from another Pride is inside, then "A vs B" with
// the first other Pride that joined.
func prideBattleTitle(masterPride string, members []*Session) string {
	masterPride = strings.TrimSpace(masterPride)
	if masterPride == "" {
		return ""
	}
	for _, member := range members {
		if member == nil || member.IsBot {
			continue
		}
		name, _ := member.prideDisplay()
		name = strings.TrimSpace(name)
		if name != "" && !strings.EqualFold(name, masterPride) {
			return masterPride + " vs " + name
		}
	}
	return masterPride
}

// syncPrideBattleTitle keeps a Pride Battle channel room titled after the
// Prides in it. The room members get CS_FD_PBCHGFIELDNAME_ACK (0x1657, room
// dispatcher sub_34250C90 -> sub_34248A60: u8 n + UTF-16 title into room +102,
// then redrawn) and the lobby gets the new title through FIELDSTATE.
func (s *Server) syncPrideBattleTitle(room *Room) {
	if room == nil {
		return
	}
	snapshot := room.Snapshot()
	var master *Session
	for _, member := range snapshot.Members {
		member.mu.RLock()
		slot := member.RoomSlot
		member.mu.RUnlock()
		if slot == int16(snapshot.Master) {
			master = member
		}
	}
	if master == nil || master.channelType() != store.ChannelTypePrideBattle {
		return
	}
	masterPride, _ := master.prideDisplay()
	title := prideBattleTitle(masterPride, snapshot.Members)
	if title == "" || title == snapshot.Config.Title {
		return
	}
	payload, err := PBFieldNamePayload(title)
	if err != nil {
		return
	}
	room.SetTitle(title)
	room.Broadcast(ReplyPBFieldName, payload)
	s.broadcastFieldState(room)
}

// prideBattleFixedRules applies the Pride Battle channel's fixed match length.
// The create/rule dialog (sub_342B2370) shows a single fixed entry in channel
// type 2 instead of the win/point choices - "6 kazanma" (stringtable 405) for
// modes with bits 0x24/0x10100 and for flag modes (bit 1), "160 puan" (406)
// for deathmatch (bit 2) and "120 puan" (447) for 0x8000 - but sends whatever
// the hidden value was, so the room ended up with the normal default (3).
func prideBattleFixedRules(config RoomConfig) RoomConfig {
	mode := config.Mode
	switch {
	case mode&0x24 != 0 || mode&0x10100 != 0:
		config.Wins = 6
	case mode&0x01 != 0:
		config.Wins = 6
	case mode&0x02 != 0:
		config.Points = 160
	case mode&0x8000 != 0:
		config.Points = 120
	}
	return config
}
