package game

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"
)

type batchEquipmentUsers struct {
	*fakeUsers
	calls   int
	userID  int
	updates map[int]int
}

func (users *batchEquipmentUsers) SetEquipmentBatch(_ context.Context, userID int, updates map[int]int) error {
	users.calls++
	users.userID = userID
	users.updates = make(map[int]int, len(updates))
	for slot, value := range updates {
		users.updates[slot] = value
	}
	return nil
}

func TestEquipmentListHasExpectedLayout(t *testing.T) {
	var equipment [34]int
	equipment[0] = 100 // primary
	equipment[2] = 200 // secondary
	equipment[3] = 300 // grenade 2
	equipment[4] = 400 // knife
	equipment[5] = 500 // grenade 1
	equipment[24] = 600
	entitlements := [10]bool{true, true, true}
	payload := EquipmentListPayload(equipment, entitlements)
	if len(payload) != 362 || payload[1] != 4 {
		t.Fatalf("unexpected equipment payload: len=%d prefix=%x", len(payload), payload[:4])
	}
	checks := map[int]uint32{4: 400, 14: 100, 19: 500, 23: 300, 87: 200, 27: 600}
	for offset, want := range checks {
		if got := binary.LittleEndian.Uint32(payload[offset : offset+4]); got != want {
			t.Fatalf("offset %d = %d, want %d", offset, got, want)
		}
	}
	for set := 0; set < equipmentSetCount; set++ {
		record := 2 + set*(2+equipmentRecordSize)
		if payload[record] != byte(set) || payload[record+1] != 0 {
			t.Fatalf("set %d header = %x", set, payload[record:record+2])
		}
	}
	firstRecord := payload[4 : 4+equipmentRecordSize]
	for power := len(entitlements); power < equipmentPowerFieldCount; power++ {
		offset := 23 + power*4
		if got := binary.LittleEndian.Uint32(firstRecord[offset : offset+4]); got != 0 {
			t.Fatalf("reserved power field %d = %d, want zero", power, got)
		}
	}
	if firstRecord[87] != 3 {
		t.Fatalf("active power slots = %d, want 3", firstRecord[87])
	}
	itemCheck := ItemCheckPayload(equipment, entitlements)
	if len(itemCheck) != 361 || itemCheck[0] != 0 || itemCheck[1] != 4 ||
		binary.LittleEndian.Uint32(itemCheck[3:7]) != 400 {
		t.Fatalf("item-check ACK framing = %x", itemCheck[:12])
	}
	for set := 0; set < equipmentSetCount; set++ {
		entry := 2 + set*(1+equipmentRecordSize)
		if itemCheck[entry] != byte(set) {
			t.Fatalf("item-check set %d ID = %d", set, itemCheck[entry])
		}
		record := itemCheck[entry+1 : entry+1+equipmentRecordSize]
		wantPower := uint32(0)
		if set == 0 {
			wantPower = 600
		}
		if got := binary.LittleEndian.Uint32(record[23:27]); got != wantPower {
			t.Fatalf("item-check set %d first power = %d, want %d", set, got, wantPower)
		}
		wantSlots := byte(0)
		if set == 0 {
			wantSlots = 3
		}
		if record[87] != wantSlots {
			t.Fatalf("item-check set %d active slots = %d, want %d", set, record[87], wantSlots)
		}
	}
	itemList := 2 + equipmentSetCount*(1+equipmentRecordSize)
	if itemCheck[itemList] != 1 || binary.LittleEndian.Uint16(itemCheck[itemList+1:itemList+3]) != 600 {
		t.Fatalf("item-check active item registry = %x", itemCheck[itemList:])
	}
}

func TestApplyEquipmentUsesWireStorageSlots(t *testing.T) {
	request := make([]byte, 90)
	request[0] = 1
	request[1] = 0
	values := map[int]int{
		2:  2_000_001_001,
		7:  2_000_001_002,
		12: 2_000_002_001,
		17: 2_000_009_001,
		21: 2_000_009_002,
		25: 2_000_010_001,
		29: 2_000_010_002,
		33: 2_000_010_003,
		37: 2_000_010_004,
		41: 2_000_010_005,
		85: 2_000_003_001,
	}
	for offset, value := range values {
		binary.LittleEndian.PutUint32(request[offset:offset+4], uint32(value))
	}
	users := &fakeUsers{}
	if err := ApplyEquipment(context.Background(), users, 42, request); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"0":  values[12],
		"1":  values[7],
		"2":  values[85],
		"3":  values[21],
		"4":  values[2],
		"5":  values[17],
		"24": values[25],
		"25": values[29],
		"26": values[33],
		"27": values[37],
		"28": values[41],
		"29": 0,
		"30": 0,
		"31": 0,
		"32": 0,
		"33": 0,
	}
	users.mu.Lock()
	got := users.equipment
	users.mu.Unlock()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("equipment writes = %v, want %v", got, want)
	}
}

func TestApplyEquipmentUsesOneBatchWhenStoreSupportsIt(t *testing.T) {
	request := make([]byte, 90)
	request[0] = 1
	request[1] = 0
	values := map[int]int{
		2:  2_000_001_001,
		7:  2_000_001_002,
		12: 2_000_002_001,
		17: 2_000_009_001,
		21: 2_000_009_002,
		25: 2_000_010_001,
		85: 2_000_003_001,
	}
	for offset, value := range values {
		binary.LittleEndian.PutUint32(request[offset:offset+4], uint32(value))
	}
	users := &batchEquipmentUsers{fakeUsers: &fakeUsers{}}
	if err := ApplyEquipment(context.Background(), users, 42, request); err != nil {
		t.Fatal(err)
	}
	want := map[int]int{
		0:  values[12],
		1:  values[7],
		2:  values[85],
		3:  values[21],
		4:  values[2],
		5:  values[17],
		24: values[25],
		25: 0,
		26: 0,
		27: 0,
		28: 0,
		29: 0,
		30: 0,
		31: 0,
		32: 0,
		33: 0,
	}
	users.fakeUsers.mu.Lock()
	fallbackWrites := len(users.fakeUsers.equipment)
	users.fakeUsers.mu.Unlock()
	if users.calls != 1 || users.userID != 42 || !reflect.DeepEqual(users.updates, want) || fallbackWrites != 0 {
		t.Fatalf("batch calls=%d user=%d updates=%v fallback=%d", users.calls, users.userID, users.updates, fallbackWrites)
	}
}
