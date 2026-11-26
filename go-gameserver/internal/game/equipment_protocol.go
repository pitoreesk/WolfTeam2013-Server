package game

import (
	"encoding/binary"

	"wtserver-go/internal/protocol"
)

const (
	equipmentSetCount        = 4
	equipmentRecordSize      = 88
	equipmentPowerFieldCount = 15
)

func EquipmentListPayload(equipment [34]int, entitlements [10]bool) []byte {
	// The common result and set count are followed by set ID, optional name,
	// and an 88-byte record.
	writer := protocol.NewWriter(2 + equipmentSetCount*(2+equipmentRecordSize)).
		U8(0).
		U8(equipmentSetCount)
	for set := 0; set < equipmentSetCount; set++ {
		writer.U8(byte(set)).U8(0) // set ID and empty display name
		record := equipmentRecord(equipment, entitlements, set)
		writer.Bytes(record[:])
	}
	return writer.Clone()
}

func ItemCheckPayload(equipment [34]int, entitlements [10]bool) []byte {
	// The result byte is followed by set snapshots and a counted item-ID list.
	writer := protocol.NewWriter(368).U8(0).U8(equipmentSetCount)
	activeSlots := contiguousPowerSlots(entitlements)
	for set := 0; set < equipmentSetCount; set++ {
		record := equipmentRecord(equipment, entitlements, set)
		writer.U8(byte(set)).Bytes(record[:])
	}
	items := make([]uint16, 0, activeSlots)
	for power := 0; power < activeSlots; power++ {
		if itemID := equipmentItemID(equipment[24+power]); itemID > 0 && itemID <= 0xffff {
			items = append(items, uint16(itemID))
		}
	}
	writer.U8(byte(len(items)))
	for _, itemID := range items {
		writer.U16(itemID)
	}
	return writer.Clone()
}

func equipmentRecord(equipment [34]int, entitlements [10]bool, set int) [equipmentRecordSize]byte {
	var record [equipmentRecordSize]byte
	storage := set * 6
	binary.LittleEndian.PutUint32(record[0:4], uint32(equipment[storage+4]))
	binary.LittleEndian.PutUint32(record[5:9], uint32(equipment[storage+1]))
	binary.LittleEndian.PutUint32(record[10:14], uint32(equipment[storage]))
	binary.LittleEndian.PutUint32(record[15:19], uint32(equipment[storage+5]))
	binary.LittleEndian.PutUint32(record[19:23], uint32(equipment[storage+3]))
	for power := 0; power < equipmentPowerFieldCount; power++ {
		value := 0
		if set == 0 && power < len(entitlements) {
			value = equipment[24+power]
		}
		offset := 23 + power*4
		binary.LittleEndian.PutUint32(record[offset:offset+4], uint32(value))
	}
	binary.LittleEndian.PutUint32(record[83:87], uint32(equipment[storage+2]))
	if set == 0 {
		record[87] = byte(contiguousPowerSlots(entitlements))
	}
	return record
}

func contiguousPowerSlots(entitlements [10]bool) int {
	count := 1
	for slot := 1; slot < len(entitlements); slot++ {
		if !entitlements[slot] {
			break
		}
		count++
	}
	return count
}

func equipmentItemID(unique int) int {
	if unique <= 0 {
		return 0
	}
	if unique >= 2_000_000_000 {
		return unique - 2_000_000_000
	}
	if unique >= 2_000_000_000-0x10000 {
		return unique - (2_000_000_000 - 0x10000)
	}
	if unique >= 1_000_000_000 {
		return unique - 1_000_000_000
	}
	if unique <= 0xffff {
		return unique
	}
	return 0
}
