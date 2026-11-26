package game

import (
	"encoding/binary"
	"fmt"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

const maxCountedItemUses = 32

// ParseUseCountItemRequest parses one inventory unique ID for each count-backed
// item used during the match.
func ParseUseCountItemRequest(payload []byte) ([]int, error) {
	if len(payload) < 1 {
		return nil, fmt.Errorf("use-count-item payload is empty")
	}
	count := int(payload[0])
	if count < 1 || count > maxCountedItemUses {
		return nil, fmt.Errorf("use-count-item count %d is invalid", count)
	}
	if required := 1 + count*4; len(payload) < required {
		return nil, fmt.Errorf("use-count-item payload is truncated: have %d, need %d", len(payload), required)
	}
	uniqueIDs := make([]int, count)
	seen := make(map[int]struct{}, count)
	for index := range count {
		offset := 1 + index*4
		uniqueID := int(binary.LittleEndian.Uint32(payload[offset : offset+4]))
		if uniqueID <= 2_000_000_000 || uniqueID > 2_000_065_535 {
			return nil, fmt.Errorf("use-count-item unique ID %d is invalid", uniqueID)
		}
		if _, duplicate := seen[uniqueID]; duplicate {
			return nil, fmt.Errorf("use-count-item unique ID %d is duplicated", uniqueID)
		}
		seen[uniqueID] = struct{}{}
		uniqueIDs[index] = uniqueID
	}
	return uniqueIDs, nil
}

// UseCountItemReplyPayload carries a result byte followed by
// {unique ID, remaining uint16} records.
func UseCountItemReplyPayload(status byte, items []store.CountedItemUse) ([]byte, error) {
	if status != 0 {
		return []byte{status}, nil
	}
	if len(items) > maxCountedItemUses {
		return nil, fmt.Errorf("use-count-item reply has %d records", len(items))
	}
	writer := protocol.NewWriter(2 + len(items)*6).U8(0).U8(byte(len(items)))
	for _, item := range items {
		if item.UniqueID <= 2_000_000_000 || item.UniqueID > 2_000_065_535 {
			return nil, fmt.Errorf("use-count-item reply unique ID %d is invalid", item.UniqueID)
		}
		remaining := min(max(item.Remaining, 0), 0xffff)
		writer.U32(uint32(item.UniqueID)).U16(uint16(remaining))
	}
	return writer.Clone(), nil
}
