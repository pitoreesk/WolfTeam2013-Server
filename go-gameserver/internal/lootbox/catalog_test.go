package lootbox

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

func TestParseAndResolveWeightedRewards(t *testing.T) {
	data := appendReward(nil, 17006, "Lucky Box", 15005, 0, 2500, 1, 1, 1, 0, 0)
	data = appendReward(data, 17006, "Lucky Box", 15030, 2500, 10000, 1, 1, 1, 0, 0)
	data = appendReward(data, 17006, "Lucky Box", 30011, 0, 6300, 0, 7, 30, 2, 2)
	catalog, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.BoxCount() != 1 || catalog.RewardCount() != 3 || !catalog.HasBox(17006) {
		t.Fatalf("catalog counts = %d boxes / %d rewards", catalog.BoxCount(), catalog.RewardCount())
	}
	for _, test := range []struct {
		roll int
		item int
	}{
		{roll: 0, item: 15005},
		{roll: 2499, item: 15005},
		{roll: 2500, item: 15030},
		{roll: 9999, item: 15030},
	} {
		reward, resolveErr := catalog.Resolve(17006, test.roll)
		if resolveErr != nil || reward.ItemID != test.item {
			t.Fatalf("roll %d = %+v, %v; want item %d", test.roll, reward, resolveErr, test.item)
		}
	}
}

func TestParseRejectsMalformedCatalog(t *testing.T) {
	valid := appendReward(nil, 17006, "Lucky Box", 15005, 0, 10000, 1, 1, 1, 0, 0)
	tests := []struct {
		name string
		data []byte
	}{
		{name: "empty", data: nil},
		{name: "truncated", data: valid[:len(valid)-1]},
		{name: "normal probability gap", data: appendReward(nil, 17006, "Lucky Box", 15005, 1, 10000, 1, 1, 1, 0, 0)},
		{name: "overlap", data: appendReward(valid, 17006, "Lucky Box", 15030, 9000, 10000, 1, 1, 1, 0, 0)},
		{name: "bad stack flag", data: appendReward(nil, 17006, "Lucky Box", 15005, 0, 10000, 2, 1, 1, 0, 0)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Parse(test.data); err == nil {
				t.Fatal("malformed catalog was accepted")
			}
		})
	}
}

func TestLoadSyntheticCatalog(t *testing.T) {
	data := appendReward(nil, 17001, "Starter Box", 15005, 0, 2500, 1, 1, 1, 0, 0)
	data = appendReward(data, 17001, "Starter Box", 15030, 2500, 10000, 1, 1, 1, 0, 0)
	data = appendReward(data, 17019, "Legend Box", 30011, 0, 10000, 0, 7, 30, 2, 0)

	path := filepath.Join(t.TempDir(), "bighitboxitem.dat")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.BoxCount() != 2 || catalog.RewardCount() != 3 {
		t.Fatalf("synthetic catalog = %d boxes / %d rewards", catalog.BoxCount(), catalog.RewardCount())
	}
	for _, test := range []struct {
		boxID  int
		roll   int
		itemID int
	}{
		{boxID: 17001, roll: 0, itemID: 15005},
		{boxID: 17001, roll: 2499, itemID: 15005},
		{boxID: 17001, roll: 2500, itemID: 15030},
		{boxID: 17001, roll: 9999, itemID: 15030},
		{boxID: 17019, roll: 0, itemID: 30011},
		{boxID: 17019, roll: 9999, itemID: 30011},
	} {
		reward, resolveErr := catalog.Resolve(test.boxID, test.roll)
		if resolveErr != nil || reward.ItemID != test.itemID {
			t.Errorf("box %d roll %d = %+v, %v; want item %d", test.boxID, test.roll, reward, resolveErr, test.itemID)
		}
	}
}

func appendReward(data []byte, boxID uint32, name string, values ...uint32) []byte {
	if len(values) != 8 {
		panic("appendReward needs eight values")
	}
	encodedName := utf16.Encode([]rune(name))
	record := make([]byte, 6+len(encodedName)*2+8*4)
	binary.LittleEndian.PutUint32(record[0:4], boxID)
	binary.LittleEndian.PutUint16(record[4:6], uint16(len(encodedName)))
	offset := 6
	for _, unit := range encodedName {
		binary.LittleEndian.PutUint16(record[offset:offset+2], unit)
		offset += 2
	}
	for _, value := range values {
		binary.LittleEndian.PutUint32(record[offset:offset+4], value)
		offset += 4
	}
	return append(data, record...)
}
