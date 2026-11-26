package game

import (
	"encoding/binary"
	"net/netip"
	"testing"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

func setClient2012ForTest(t *testing.T, enabled bool) {
	t.Helper()
	prev := Client2012
	Client2012 = enabled
	t.Cleanup(func() { Client2012 = prev })
}

func TestClient2012RoomConfigAndFieldPayloads(t *testing.T) {
	setClient2012ForTest(t, true)

	// 2012 room config omits the WeaponFlags byte (15 fixed bytes vs 16 in 2013).
	raw2012 := protocol.NewWriter(64).
		U8(8).UTF16LE("Test").
		U8(0).
		U8(4).     // Map
		U32(2).    // Mode
		U8(5).     // Wins
		U8(5).     // EquipmentCount
		U16(600).  // Time
		U16(40).   // Points
		U8(0x03).  // GameFlags
		U16(0).    // HeavyWeaponLimit
		U8(16).    // Capacity
		U8(1).     // MapSetting
		U8(2).     // RuleSetting
		Clone()

	cfg, err := ParseRoomConfig(raw2012)
	if err != nil {
		t.Fatalf("ParseRoomConfig(2012): %v", err)
	}
	if cfg.Title != "Test" || cfg.Map != 4 || cfg.Mode != 2 || cfg.Time != 600 || cfg.Points != 40 || cfg.Capacity != 16 || cfg.RuleSetting != 2 {
		t.Fatalf("unexpected 2012 room config: %+v", cfg)
	}

	// CreateRoomPayload omits the leading U32(0) in 2012 mode.
	create2012 := CreateRoomPayload(7, cfg)
	if len(create2012) != 5 {
		t.Fatalf("CreateRoomPayload(2012) len = %d, want 5", len(create2012))
	}
	if got := binary.LittleEndian.Uint16(create2012[1:3]); got != 7 {
		t.Fatalf("CreateRoomPayload(2012) room = %d, want 7", got)
	}

	snap := RoomSnapshot{Number: 7, Config: cfg, Users: 1, Open: true}
	list2012, err := FieldListPayload([]RoomSnapshot{snap})
	if err != nil {
		t.Fatalf("FieldListPayload(2012): %v", err)
	}
	if list2012[0] != 0 || list2012[1] != 1 {
		t.Fatalf("FieldListPayload(2012) header = %x, want 0001", list2012[:2])
	}
	state2012, err := FieldStatePayload(snap)
	if err != nil {
		t.Fatalf("FieldStatePayload(2012): %v", err)
	}
	rule2012 := ChangeRuleConfigPayload(cfg)

	Client2012 = false
	create2013 := CreateRoomPayload(7, cfg)
	if len(create2013)-len(create2012) != 4 {
		t.Fatalf("CreateRoomPayload delta = %d, want 4", len(create2013)-len(create2012))
	}
	list2013, err := FieldListPayload([]RoomSnapshot{snap})
	if err != nil {
		t.Fatalf("FieldListPayload(2013): %v", err)
	}
	// +2 leading bytes before count, +1 WeaponFlags byte in the single entry.
	if len(list2013)-len(list2012) != 3 {
		t.Fatalf("FieldListPayload delta = %d, want 3", len(list2013)-len(list2012))
	}
	state2013, err := FieldStatePayload(snap)
	if err != nil {
		t.Fatalf("FieldStatePayload(2013): %v", err)
	}
	if len(state2013)-len(state2012) != 1 {
		t.Fatalf("FieldStatePayload delta = %d, want 1", len(state2013)-len(state2012))
	}
	rule2013 := ChangeRuleConfigPayload(cfg)
	if len(rule2013)-len(rule2012) != 1 {
		t.Fatalf("ChangeRuleConfigPayload delta = %d, want 1", len(rule2013)-len(rule2012))
	}
}

func TestClient2012RosterAndEnterRoomPayloads(t *testing.T) {
	setClient2012ForTest(t, true)

	snap := RoomSnapshot{
		Number: 1,
		Master: 0,
		Config: RoomConfig{Title: "Room1", Map: 1, Mode: 1, Capacity: 16},
	}
	member := RosterMember{
		Slot:          0,
		Team:          1,
		TeamSlot:      0,
		Status:        1,
		SessionID:     42,
		Level:         10,
		LoginName:     "alice",
		Callsign:      "Alice",
		RedCharacter:  30006,
		BlueCharacter: 30005,
		GlobalIP:      netip.MustParseAddr("127.0.0.1"),
		GlobalPort:    40707,
		LocalIP:       netip.MustParseAddr("127.0.0.1"),
		LocalPort:     40707,
	}

	roster2012, err := RoomRosterPayload(snap, []RosterMember{member})
	if err != nil {
		t.Fatalf("RoomRosterPayload(2012): %v", err)
	}
	enter2012, err := EnterRoomPayload(0, member)
	if err != nil {
		t.Fatalf("EnterRoomPayload(2012): %v", err)
	}

	Client2012 = false
	roster2013, err := RoomRosterPayload(snap, []RosterMember{member})
	if err != nil {
		t.Fatalf("RoomRosterPayload(2013): %v", err)
	}
	// +1 WeaponFlags byte in room header, +4 skipped bytes after Slot per member.
	if len(roster2013)-len(roster2012) != 5 {
		t.Fatalf("RoomRosterPayload delta = %d, want 5", len(roster2013)-len(roster2012))
	}
	enter2013, err := EnterRoomPayload(0, member)
	if err != nil {
		t.Fatalf("EnterRoomPayload(2013): %v", err)
	}
	// 2013 adds 4 bytes before Slot (6 vs 2) and 4 bytes after Slot (4 vs 0).
	if len(enter2013)-len(enter2012) != 8 {
		t.Fatalf("EnterRoomPayload delta = %d, want 8", len(enter2013)-len(enter2012))
	}
}

func TestClient2012UserInfoEndGameAndGamePoints(t *testing.T) {
	setClient2012ForTest(t, true)

	info := store.AccountSnapshot{
		Level: 28, GP: 50000, Gold: 10000, WC: 500, Cash: 2000,
		Kills: 12, Deaths: 4, PlayTimeSeconds: 300,
	}
	user2012, err := UserInfoPayload("Alice", info)
	if err != nil {
		t.Fatalf("UserInfoPayload(2012): %v", err)
	}
	endRes := EndGameResult{
		Info: info, PlayTimeSeconds: 300,
		Players: []EndGamePlayer{{Slot: 0, Team: 1, GoldReward: 100, GPReward: 200, Winner: true}},
	}
	end2012, err := EndGamePayload(endRes)
	if err != nil {
		t.Fatalf("EndGamePayload(2012): %v", err)
	}
	gp2012 := GamePointsPayload(info, GamePointSlot{BonusGold: 10, Winner: true, Team: 1})

	Client2012 = false
	user2013, err := UserInfoPayload("Alice", info)
	if err != nil {
		t.Fatalf("UserInfoPayload(2013): %v", err)
	}
	// 2012 writes a 13th U32 (4 bytes) instead of the 1-byte P.T. bonus index.
	if len(user2012)-len(user2013) != 3 {
		t.Fatalf("UserInfoPayload delta (2012-2013) = %d, want 3", len(user2012)-len(user2013))
	}
	end2013, err := EndGamePayload(endRes)
	if err != nil {
		t.Fatalf("EndGamePayload(2013): %v", err)
	}
	// 2012 writes +2 U32s (+8 bytes) and omits WeaponFlags (-1 byte) => +7 bytes.
	if len(end2012)-len(end2013) != 7 {
		t.Fatalf("EndGamePayload delta (2012-2013) = %d, want 7", len(end2012)-len(end2013))
	}
	gp2013 := GamePointsPayload(info, GamePointSlot{BonusGold: 10, Winner: true, Team: 1})
	if len(gp2013)-len(gp2012) != 1 {
		t.Fatalf("GamePointsPayload delta (2013-2012) = %d, want 1", len(gp2013)-len(gp2012))
	}
}

func TestClient2012ChatInventoryAndEventShop(t *testing.T) {
	setClient2012ForTest(t, true)

	req2012 := protocol.NewWriter(16).U8(0).U8(4).UTF16LE("hi").Clone()
	parsed, err := ParseChannelChatRequest(req2012)
	if err != nil {
		t.Fatalf("ParseChannelChatRequest(2012): %v", err)
	}
	if parsed.Text != "hi" {
		t.Fatalf("chat text = %q, want hi", parsed.Text)
	}
	chat2012, err := ChannelChatPayload(req2012, "Alice", 12, false, 0, "")
	if err != nil {
		t.Fatalf("ChannelChatPayload(2012): %v", err)
	}
	sys2012, err := ChannelSystemMessagePayload("hello")
	if err != nil {
		t.Fatalf("ChannelSystemMessagePayload(2012): %v", err)
	}

	buyReq := BuyRequest{ItemID: 1001, Days: 7, PurchaseMode: 1}
	receipt := store.PurchaseReceipt{Gold: 5000, Cash: 1000, WC: 200, Expiry: "20261201120000"}
	buy2012 := BuyItemReplyPayload(buyReq, receipt, 0, false)

	useReq := GenericUseItemRequest{UniqueID: 2000001001, ItemID: 1001, Text: "ok"}
	info := store.AccountSnapshot{Gold: 5000, Cash: 1000, WC: 200, GP: 1000, Level: 10}
	use2012 := GenericUseItemReplyPayload(0, useReq, 1, info)

	event2012 := EventShopPayload(nil, true)

	Client2012 = false
	req2013 := protocol.NewWriter(24).U8(0).U32(0).U8(0).U8(4).UTF16LE("hi").Clone()
	chat2013, err := ChannelChatPayload(req2013, "Alice", 12, false, 0, "")
	if err != nil {
		t.Fatalf("ChannelChatPayload(2013): %v", err)
	}
	// +4 bytes color +1 byte empty prefix length.
	if len(chat2013)-len(chat2012) != 5 {
		t.Fatalf("ChannelChatPayload delta = %d, want 5", len(chat2013)-len(chat2012))
	}
	sys2013, err := ChannelSystemMessagePayload("hello")
	if err != nil {
		t.Fatalf("ChannelSystemMessagePayload(2013): %v", err)
	}
	if len(sys2013)-len(sys2012) != 5 {
		t.Fatalf("ChannelSystemMessagePayload delta = %d, want 5", len(sys2013)-len(sys2012))
	}
	buy2013 := BuyItemReplyPayload(buyReq, receipt, 0, false)
	// +2 bytes reserved U16 +4 bytes WC.
	if len(buy2013)-len(buy2012) != 6 {
		t.Fatalf("BuyItemReplyPayload delta = %d, want 6", len(buy2013)-len(buy2012))
	}
	use2013 := GenericUseItemReplyPayload(0, useReq, 1, info)
	// +4 GP +4 base +4 next +1 level = 13 bytes.
	if len(use2013)-len(use2012) != 13 {
		t.Fatalf("GenericUseItemReplyPayload delta = %d, want 13", len(use2013)-len(use2012))
	}
	event2013 := EventShopPayload(nil, true)
	// +3 forge grade bytes +1 trailing premium byte = 4 bytes.
	if len(event2013)-len(event2012) != 4 {
		t.Fatalf("EventShopPayload delta = %d, want 4", len(event2013)-len(event2012))
	}
}
