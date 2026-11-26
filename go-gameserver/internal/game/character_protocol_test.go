package game

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

func TestResolveCharacterRequiresOwnership(t *testing.T) {
	owned := map[int]struct{}{30030: {}, 30048: {}}
	loadout := store.CharacterSelection{RedCharacter: 30030, BlueCharacter: 30048}
	if got := resolveCharacter(2, loadout, owned); got != 30030 {
		t.Fatalf("red character = %d", got)
	}
	if got := resolveCharacter(1, loadout, owned); got != 30048 {
		t.Fatalf("blue character = %d", got)
	}
	loadout.RedCharacter = 30069
	if got := resolveCharacter(2, loadout, owned); got != 30006 {
		t.Fatalf("unowned red character = %d", got)
	}
	if got := resolveCharacter(1, loadout, map[int]struct{}{}); got != 30005 {
		t.Fatalf("unowned blue character = %d", got)
	}
	loadout = store.CharacterSelection{RedCharacter: 30048, BlueCharacter: 30030}
	if got := resolveCharacter(2, loadout, owned); got != 30006 {
		t.Fatalf("blue character in red loadout = %d", got)
	}
	if got := resolveCharacter(1, loadout, owned); got != 30005 {
		t.Fatalf("red character in blue loadout = %d", got)
	}
}

func TestCharacterFactionCatalog(t *testing.T) {
	if len(characterFactions) != 80 {
		t.Fatalf("faction catalog has %d entries, want 80", len(characterFactions))
	}
	red, blue := 0, 0
	for itemID, faction := range characterFactions {
		if itemID < 30001 || itemID > 30080 {
			t.Fatalf("unexpected character item ID %d", itemID)
		}
		switch faction {
		case characterFactionRed:
			red++
		case characterFactionBlue:
			blue++
		default:
			t.Fatalf("character %d has invalid faction %d", itemID, faction)
		}
	}
	if red != 40 || blue != 40 {
		t.Fatalf("faction split = red %d / blue %d, want 40 / 40", red, blue)
	}
	for itemID, want := range map[int]characterFaction{
		30005: characterFactionBlue,
		30006: characterFactionRed,
		30009: characterFactionRed,
		30030: characterFactionRed,
		30048: characterFactionBlue,
		30069: characterFactionRed,
		30071: characterFactionBlue,
	} {
		if got := characterFactions[itemID]; got != want {
			t.Fatalf("character %d faction = %d, want %d", itemID, got, want)
		}
	}
}

func TestCharacterSelectionResolvesSlotKeys(t *testing.T) {
	current := store.CharacterSelection{RedCharacter: 30006, BlueCharacter: 30005}
	selected, first, second := resolveCharacterSelection(map[int]int{3: 30030, 4: 30048}, 3, 4, current)
	if first != 30030 || second != 30048 || selected.RedCharacter != 30030 || selected.BlueCharacter != 30048 {
		t.Fatalf("selection = %+v candidates=%d/%d", selected, first, second)
	}
	if !automaticBasicEcho(true, 30006, 30005) || automaticBasicEcho(false, 30006, 30005) {
		t.Fatal("initial basic-character echo detection is incorrect")
	}
	reordered, _, _ := resolveCharacterSelection(map[int]int{3: 30048, 4: 30030}, 3, 4, current)
	if reordered != selected {
		t.Fatalf("reversed selection = %+v, want %+v", reordered, selected)
	}
	unchanged, _, _ := resolveCharacterSelection(map[int]int{3: 30030, 4: 30069}, 3, 4, selected)
	if unchanged != selected {
		t.Fatalf("same-faction selection changed loadout: %+v", unchanged)
	}
}

func TestSaveCharacterSlotsMergesPartialUpdateWithoutChangingLoadout(t *testing.T) {
	users := &fakeUsers{
		items: []store.Item{
			{ID: 30005, Type: 1},
			{ID: 30006, Type: 1},
			{ID: 30069, Type: 1},
			{ID: 30071, Type: 1},
			{ID: 30009, Type: 1},
		},
		slots: map[int]int{
			1: 30005,
			2: 30006,
			3: 30069,
			4: 30071,
			5: 30009,
		},
		loadout: store.CharacterSelection{RedCharacter: 30069, BlueCharacter: 30071},
	}
	server := &Server{users: users}
	session := &Session{UserID: 42}
	payload := make([]byte, 1+69)
	payload[0] = 1
	payload[1] = 7
	binary.LittleEndian.PutUint32(payload[2:6], 2_000_030_009)
	// Equipment data can look like a unique ID and must not change loadout.
	binary.LittleEndian.PutUint32(payload[6:10], 2_000_030_009)

	if err := server.saveCharacterSlots(context.Background(), session, payload); err != nil {
		t.Fatal(err)
	}
	want := map[int]int{1: 30005, 2: 30006, 3: 30069, 4: 30071, 7: 30009}
	if !sameCharacterSlots(users.slots, want) {
		t.Fatalf("merged slots = %v, want %v", users.slots, want)
	}
	if users.loadout != (store.CharacterSelection{RedCharacter: 30069, BlueCharacter: 30071}) {
		t.Fatalf("loadout changed during slot save: %+v", users.loadout)
	}
	if got := users.charEquipment[30009][0]; got != 2_000_030_009 {
		t.Fatalf("saved character equipment slot 0 = %d", got)
	}
	if got := session.characterSelector(); !sameCharacterSlots(got, want) {
		t.Fatalf("session selector = %v, want %v", got, want)
	}
}

func TestInRoomDefaultCharacterBroadcastsFreshMemberUpdate(t *testing.T) {
	users := &fakeUsers{
		items: []store.Item{
			{ID: 30005, Type: 1},
			{ID: 30006, Type: 1},
			{ID: 30069, Type: 1},
			{ID: 30071, Type: 1},
		},
		slots: map[int]int{
			1: 30005,
			2: 30006,
			3: 30069,
			4: 30071,
		},
		loadout: store.CharacterSelection{RedCharacter: 30006, BlueCharacter: 30005},
	}
	harness := newFailSoftHarness(t, users)
	room := newRoom(0, RoomConfig{Title: "Shop room", Capacity: 8})
	if _, ok := room.Add(harness.session); !ok {
		t.Fatal("add shopper to room")
	}
	harness.server.mu.Lock()
	harness.server.rooms[room.Number] = room
	harness.server.mu.Unlock()

	// Prime the same stale entry-roster cache that exists when the player opens
	// the shop after joining a room.
	initial := harness.server.loadRosterState(context.Background(), harness.session, true)
	if initial.RedCharacter != 30006 || initial.BlueCharacter != 30005 {
		t.Fatalf("initial cached characters = %d/%d", initial.RedCharacter, initial.BlueCharacter)
	}

	done := make(chan error, 1)
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{
			MessageID: RequestDefaultChar,
			Payload:   []byte{3, 4},
		})
	}()
	ack := assertFrameID(t, harness.client, ReplyDefaultChar)
	memberUpdate := assertFrameID(t, harness.client, ReplyEnterRoom)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(ack.Payload) < 1 || ack.Payload[0] != 0 {
		t.Fatalf("default-character ACK = %x", ack.Payload)
	}

	characters := make([]byte, 10)
	characters[0], characters[5] = 2, 2
	binary.LittleEndian.PutUint16(characters[1:3], 30069)
	binary.LittleEndian.PutUint16(characters[3:5], 30069)
	binary.LittleEndian.PutUint16(characters[6:8], 30071)
	binary.LittleEndian.PutUint16(characters[8:10], 30071)
	if !bytes.Contains(memberUpdate.Payload, characters) {
		t.Fatalf("room member update does not contain equipped characters %d/%d: %x", 30069, 30071, memberUpdate.Payload)
	}
	if users.loadout != (store.CharacterSelection{RedCharacter: 30069, BlueCharacter: 30071}) {
		t.Fatalf("persisted in-room loadout = %+v", users.loadout)
	}
}
