package game

import "math/bits"

const (
	maxRoomCount       = 255
	roomIndexWordSize  = 64
	roomIndexWordCount = (maxRoomCount + roomIndexWordSize - 1) / roomIndexWordSize
)

func setRoomIndexBit(index *[roomIndexWordCount]uint64, number int16, value bool) {
	if number < 0 || int(number) >= maxRoomCount {
		return
	}
	word := int(number) / roomIndexWordSize
	mask := uint64(1) << (uint(number) % roomIndexWordSize)
	if value {
		index[word] |= mask
	} else {
		index[word] &^= mask
	}
}

func lowestIndexedRoom(index *[roomIndexWordCount]uint64) (int16, bool) {
	for word, value := range index {
		if value == 0 {
			continue
		}
		number := word*roomIndexWordSize + bits.TrailingZeros64(value)
		if number < maxRoomCount {
			return int16(number), true
		}
	}
	return -1, false
}

func lowestFreeRoom(index *[roomIndexWordCount]uint64) (int16, bool) {
	for word, occupied := range index {
		available := ^occupied
		if word == roomIndexWordCount-1 && maxRoomCount%roomIndexWordSize != 0 {
			available &= uint64(1)<<(maxRoomCount%roomIndexWordSize) - 1
		}
		if available == 0 {
			continue
		}
		number := word*roomIndexWordSize + bits.TrailingZeros64(available)
		return int16(number), true
	}
	return -1, false
}

func roomAllowsQuickJoin(open bool, config RoomConfig, users byte) bool {
	return open && config.Password == "" && (config.Capacity == 0 || users < config.Capacity)
}

// indexRoomLocked registers a room in both indexes. The callback runs while a
// room mutation holds the room lock, so it only takes the server lock; callers
// must not acquire a room lock while holding Server.mu.
func (s *Server) indexRoomLocked(room *Room) {
	setRoomIndexBit(&s.occupiedRooms, room.Number, true)
	setRoomIndexBit(&s.joinableRooms, room.Number, roomAllowsQuickJoin(room.Open, room.Config, room.Users))
	room.joinabilityChanged = func(joinable bool) {
		s.mu.Lock()
		if s.rooms[room.Number] == room {
			setRoomIndexBit(&s.joinableRooms, room.Number, joinable)
		}
		s.mu.Unlock()
	}
}

func (s *Server) deleteRoom(room *Room) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rooms[room.Number] != room {
		return false
	}
	delete(s.rooms, room.Number)
	setRoomIndexBit(&s.occupiedRooms, room.Number, false)
	setRoomIndexBit(&s.joinableRooms, room.Number, false)
	return true
}

func (s *Server) quickEnterRoomNumber() int16 {
	return s.quickEnterRoomNumberForLevel(defaultClassMin)
}

func (s *Server) quickEnterRoomNumberForLevel(level byte) int16 {
	s.mu.RLock()
	candidates := make([]*Room, 0, maxRoomCount)
	for number := int16(0); number < maxRoomCount; number++ {
		word := int(number) / roomIndexWordSize
		mask := uint64(1) << (uint(number) % roomIndexWordSize)
		if s.joinableRooms[word]&mask != 0 {
			if room := s.rooms[number]; room != nil {
				candidates = append(candidates, room)
			}
		}
	}
	s.mu.RUnlock()

	for _, room := range candidates {
		snapshot := room.Snapshot()
		if roomAllowsQuickJoin(snapshot.Open, snapshot.Config, snapshot.Users) &&
			roomAllowsLevel(snapshot.Config, level) {
			return snapshot.Number
		}
	}
	return -1
}
