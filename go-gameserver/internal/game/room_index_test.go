package game

import (
	"io"
	"log/slog"
	"net/netip"
	"testing"
)

func newRoomIndexTestServer() *Server {
	loopback := netip.MustParseAddr("127.0.0.1")
	return NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestRoomIndexAllocatorReusesLowestNumber(t *testing.T) {
	server := newRoomIndexTestServer()
	first, err := server.allocateRoom(RoomConfig{Title: "first", Capacity: 8})
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.allocateRoom(RoomConfig{Title: "second", Capacity: 8})
	if err != nil {
		t.Fatal(err)
	}
	third, err := server.allocateRoom(RoomConfig{Title: "third", Capacity: 8})
	if err != nil {
		t.Fatal(err)
	}
	if first.Number != 0 || second.Number != 1 || third.Number != 2 {
		t.Fatalf("initial room numbers = %d, %d, %d", first.Number, second.Number, third.Number)
	}
	if !server.deleteRoom(second) {
		t.Fatal("delete indexed room failed")
	}
	replacement, err := server.allocateRoom(RoomConfig{Title: "replacement", Capacity: 8})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Number != 1 {
		t.Fatalf("replacement room number = %d, want 1", replacement.Number)
	}
}

func TestRoomIndexAllocatorHonorsUpperBound(t *testing.T) {
	var occupied [roomIndexWordCount]uint64
	for index := range occupied {
		occupied[index] = ^uint64(0)
	}
	setRoomIndexBit(&occupied, maxRoomCount-1, false)
	if number, ok := lowestFreeRoom(&occupied); !ok || number != maxRoomCount-1 {
		t.Fatalf("last free room = %d, %t; want %d, true", number, ok, maxRoomCount-1)
	}
	setRoomIndexBit(&occupied, maxRoomCount-1, true)
	if number, ok := lowestFreeRoom(&occupied); ok {
		t.Fatalf("full index returned room %d", number)
	}
}

func TestQuickEnterIndexTracksRoomEligibility(t *testing.T) {
	server := newRoomIndexTestServer()
	first, err := server.allocateRoom(RoomConfig{Title: "first", Capacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.allocateRoom(RoomConfig{Title: "second", Capacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := server.quickEnterRoomNumber(); got != first.Number {
		t.Fatalf("initial quick-enter room = %d, want %d", got, first.Number)
	}

	firstUser := &Session{RoomNumber: -1, RoomSlot: -1}
	secondUser := &Session{RoomNumber: -1, RoomSlot: -1}
	if _, ok := first.Add(firstUser); !ok {
		t.Fatal("add first user")
	}
	if _, ok := first.Add(secondUser); !ok {
		t.Fatal("add second user")
	}
	if got := server.quickEnterRoomNumber(); got != second.Number {
		t.Fatalf("quick-enter with first room full = %d, want %d", got, second.Number)
	}

	first.Remove(firstUser)
	if got := server.quickEnterRoomNumber(); got != first.Number {
		t.Fatalf("quick-enter after vacancy = %d, want %d", got, first.Number)
	}

	passwordConfig := first.Snapshot().Config
	passwordConfig.Password = "secret"
	first.UpdateConfig(passwordConfig)
	if got := server.quickEnterRoomNumber(); got != second.Number {
		t.Fatalf("quick-enter with password = %d, want %d", got, second.Number)
	}

	passwordConfig.Password = ""
	first.UpdateConfig(passwordConfig)
	if got := server.quickEnterRoomNumber(); got != first.Number {
		t.Fatalf("quick-enter after password removal = %d, want %d", got, first.Number)
	}

	if !server.deleteRoom(first) {
		t.Fatal("delete first room")
	}
	if got := server.quickEnterRoomNumber(); got != second.Number {
		t.Fatalf("quick-enter after deletion = %d, want %d", got, second.Number)
	}
}

func TestQuickEnterSkipsRoomsOutsideClassLimit(t *testing.T) {
	server := newRoomIndexTestServer()
	first, err := server.allocateRoom(RoomConfig{
		Title: "higher class", Capacity: 8, ClassLimitMin: 10, ClassLimitMax: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.allocateRoom(RoomConfig{
		Title: "lower class", Capacity: 8, ClassLimitMin: 0, ClassLimitMax: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := server.quickEnterRoomNumberForLevel(3); got != second.Number {
		t.Fatalf("level 3 quick-enter room = %d, want %d", got, second.Number)
	}
	if got := server.quickEnterRoomNumberForLevel(12); got != first.Number {
		t.Fatalf("level 12 quick-enter room = %d, want %d", got, first.Number)
	}
	if got := server.quickEnterRoomNumberForLevel(30); got != -1 {
		t.Fatalf("level 30 quick-enter room = %d, want none", got)
	}
}
