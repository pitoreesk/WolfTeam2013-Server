package game

import (
	"bytes"
	"encoding/binary"
	"testing"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

func TestCompatibilityPayloadLayoutsMatchProtocol(t *testing.T) {
	power, err := PowerUserInfoPayload(false, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(power) != 18 || !bytes.Equal(power, make([]byte, 18)) {
		t.Fatalf("power-user payload = %x", power)
	}
	enabledPower, err := PowerUserInfoPayload(true, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(enabledPower) != 18 || binary.LittleEndian.Uint32(enabledPower[1:5]) != 1 ||
		binary.LittleEndian.Uint32(enabledPower[5:9]) != 1 ||
		binary.LittleEndian.Uint32(enabledPower[14:18]) != 0x1f {
		t.Fatalf("enabled power-user payload = %x", enabledPower)
	}
	settingsRequest := protocol.NewWriter(11).U32(0xff112233).U8(6).UTF16LE("VIP").Clone()
	settings, err := ParsePowerUserSettings(settingsRequest)
	if err != nil || settings.Color != 0xff112233 || settings.Label != "VIP" {
		t.Fatalf("Power User settings = %+v, %v", settings, err)
	}
	if !bytes.Equal(PowerUserStatusPayload(1), []byte{1}) {
		t.Fatalf("Power User failure status = %x", PowerUserStatusPayload(1))
	}
	powerItems, err := PowerMasterItemsPayload(1, []PowerRoomItem{{UniqueID: 2_000_003_005, ItemID: 3005}})
	if err != nil {
		t.Fatal(err)
	}
	if len(powerItems) != 11 || powerItems[0] != 0 || powerItems[1] != 1 || powerItems[2] != 1 ||
		binary.LittleEndian.Uint32(powerItems[3:7]) != 2_000_003_005 ||
		binary.LittleEndian.Uint16(powerItems[7:9]) != 3005 ||
		binary.LittleEndian.Uint16(powerItems[9:11]) != 0xffff {
		t.Fatalf("Power Master item payload = %x", powerItems)
	}

	macros, err := MacroMessagePayload([]MacroMessage{{Slot: 2, Flag: 1, Text: "OK"}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(macros, []byte{0, 1, 2, 1, 4, 'O', 0, 'K', 0}) {
		t.Fatalf("macro payload = %x", macros)
	}

	if !bytes.Equal(SNSAccountPayload(), []byte{0, 0}) {
		t.Fatalf("SNS payload = %x", SNSAccountPayload())
	}
	// 2013 TR sub_34182BA0: per lounge {id, start, end, visible, m x {period, 1}},
	// then 3-byte forge entries {index, show, grade} and the Premium shop byte.
	eventShop := EventShopPayload([]store.VIPLounge{{Lounge: 1, Visible: true, Start: "20120901", End: "20301231", Periods: []byte{0, 7}}}, true)
	expectedEventShop := []byte{0, 3, 0, 0, 0, 0, 0, 1, 8}
	expectedEventShop = append(expectedEventShop, "20120901"...)
	expectedEventShop = append(expectedEventShop, 8)
	expectedEventShop = append(expectedEventShop, "20301231"...)
	expectedEventShop = append(expectedEventShop, 1, 2, 0, 1, 7, 1, 2, 0, 0, 0, 0)
	expectedEventShop = append(expectedEventShop, 3, 0, 1, 61, 1, 0, 61, 2, 0, 61, 1)
	if !bytes.Equal(eventShop, expectedEventShop) {
		t.Fatalf("event-shop payload = %x, want %x", eventShop, expectedEventShop)
	}
	if !bytes.Equal(LimitedItemsPayload(), []byte{0, 0}) {
		t.Fatalf("limited-items payload = %x", LimitedItemsPayload())
	}
	if !bytes.Equal(EventRewardListPayload(nil), []byte{0, 0, 0, 0, 0, 0}) {
		t.Fatalf("event-reward-list payload = %x", EventRewardListPayload(nil))
	}
	// sub_34177130: u32 event id, u8 n, then u16 item, u8 required unit,
	// u16 required value, u16 bonus item, u8 bonus unit, u16 bonus amount.
	oneplus := EventRewardListPayload([]store.EventBonus{{
		ItemID: 17001, RequiredUnit: 1, RequiredValue: 1, BonusItemID: 17001, BonusUnit: 1, BonusAmount: 30,
	}})
	wantOnePlus := []byte{0, 1, 0, 0, 0, 1, 0x69, 0x42, 1, 1, 0, 0x69, 0x42, 1, 30, 0}
	if !bytes.Equal(oneplus, wantOnePlus) {
		t.Fatalf("1+1 event list = %x, want %x", oneplus, wantOnePlus)
	}
	if !bytes.Equal(InitFieldPayload(), []byte{0}) || !bytes.Equal(TunnelingPayload(true), []byte{0, 1}) {
		t.Fatalf("field compatibility payloads = %x %x", InitFieldPayload(), TunnelingPayload(true))
	}
	if RequestTunneling != 0x1640 || ReplyTunneling != 0x1641 {
		t.Fatalf("tunneling IDs = request 0x%04x reply 0x%04x", RequestTunneling, ReplyTunneling)
	}
}

func TestPrideEmptyPayloadIsStructurallyComplete(t *testing.T) {
	if got := PrideResultPayload(); len(got) != 27 || !bytes.Equal(got, make([]byte, 27)) {
		t.Fatalf("pride result = %x", got)
	}
	pack, err := WPackSubItemsPayload(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pack, []byte{0, 0}) {
		t.Fatalf("W-pack sub-items = %x", pack)
	}
}
