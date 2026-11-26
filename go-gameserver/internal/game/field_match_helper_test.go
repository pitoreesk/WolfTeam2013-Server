package game

// beginTestMatch starts a match the way the room does: every seated human is
// ready (status 1) when the master starts, so BeginMatch takes them all into
// the field. Members left on the room screen are not in the match.
func beginTestMatch(room *Room) {
	room.mu.RLock()
	slots := room.Slots
	room.mu.RUnlock()
	for _, session := range slots {
		if session == nil || session.IsBot {
			continue
		}
		session.mu.Lock()
		if session.RoomStatus == 0 {
			session.RoomStatus = readyRoomStatus
		}
		session.mu.Unlock()
	}
	room.BeginMatch()
}
