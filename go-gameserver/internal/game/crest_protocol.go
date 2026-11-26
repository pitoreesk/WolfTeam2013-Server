package game

import (
	"encoding/binary"
	"fmt"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

// 2013 TR crest (arma) packets, docs/protocol/crest_2013.md. Every ACK parser
// skips a u16 after the pump's result byte.

// CrestSinkPayload encodes CS_FD_CRESTSINK_ACK (0x1699, sub_3407BB70 ->
// sub_3407BAE0, manager dispatcher): u8 n, n x {u16 crest, u32 counter}; each
// counter lands in the group array (+1980 + 4*group).
func CrestSinkPayload(active []store.CrestActiveChallenge) ([]byte, error) {
	if len(active) > 255 {
		return nil, fmt.Errorf("crest progress count exceeds one-byte protocol limit")
	}
	writer := protocol.NewWriter(4 + len(active)*6).U8(0).U16(0).U8(byte(len(active)))
	for _, crest := range active {
		writer.U16(crest.CrestID).U32(crest.Progress)
	}
	return writer.Clone(), nil
}

// CrestUserEntry is one 0x134D list-2 row.
type CrestUserEntry struct {
	CrestID  uint16
	Percent  uint16 // non-zero: owned-crest vector; 100 = completed
	Active   bool
	Progress uint32 // sent only for an active challenge
}

// CrestUserPayload encodes CS_CH_CRESTUSERINFO_ACK (0x134D, sub_3418A440,
// login-section dispatcher only): u8 n, n x {u8 slot, u16 crest} delegate
// slots; u16 m, m x {u16 crest, u16 percent, u8 active, [u32 counter]}.
func CrestUserPayload(delegates []store.CrestDelegateSlot, entries []CrestUserEntry) ([]byte, error) {
	if len(delegates) > 3 || len(entries) > 65535 {
		return nil, fmt.Errorf("crest list exceeds the client limits")
	}
	writer := protocol.NewWriter(6 + len(delegates)*3 + len(entries)*9).U8(0).U16(0).U8(byte(len(delegates)))
	for _, slot := range delegates {
		writer.U8(slot.Slot).U16(slot.CrestID)
	}
	writer.U16(uint16(len(entries)))
	for _, entry := range entries {
		writer.U16(entry.CrestID).U16(min(entry.Percent, 100)).Bool(entry.Active)
		if entry.Active {
			writer.U32(entry.Progress)
		}
	}
	return writer.Clone(), nil
}

// ParseCrestEquipRequest decodes CS_CH_CRESTEQUIP_REQ (0x134E, sub_34307150):
// u8 slot, u16 crest.
func ParseCrestEquipRequest(payload []byte) (byte, uint16, error) {
	if len(payload) < 3 {
		return 0, 0, fmt.Errorf("crest equip payload is too short")
	}
	return payload[0], binary.LittleEndian.Uint16(payload[1:3]), nil
}

// CrestEquipPayload encodes CS_CH_CRESTEQUIP_ACK (0x134F, sub_34307590): the
// client clears its delegate vector, then reads u8 n, n x {u8 slot, u16 crest}.
func CrestEquipPayload(delegates []store.CrestDelegateSlot) []byte {
	delegates = delegates[:min(len(delegates), 3)]
	writer := protocol.NewWriter(4 + len(delegates)*3).U8(0).U16(0).U8(byte(len(delegates)))
	for _, slot := range delegates {
		writer.U8(slot.Slot).U16(slot.CrestID)
	}
	return writer.Clone()
}

// ParseCrestChallengeRequest decodes CS_CH_CRESTCHALLENGE_REQ (0x1352,
// sub_34338170): u8 n, n x u16 crest.
func ParseCrestChallengeRequest(payload []byte) ([]uint16, error) {
	if len(payload) < 1 || len(payload) < 1+2*int(payload[0]) {
		return nil, fmt.Errorf("crest challenge payload is too short")
	}
	crests := make([]uint16, payload[0])
	for i := range crests {
		crests[i] = binary.LittleEndian.Uint16(payload[1+2*i:])
	}
	return crests, nil
}

// CrestChallengePayload encodes CS_CH_CRESTCHALLENGE_ACK (0x1353,
// sub_34338E10): u32 Gold balance, u8 n, n x {u16 crest, u16 counter} appended
// to the active-challenge vector.
func CrestChallengePayload(gold uint32, started []uint16) []byte {
	started = started[:min(len(started), 255)]
	writer := protocol.NewWriter(8 + len(started)*4).U8(0).U16(0).U32(gold).U8(byte(len(started)))
	for _, crest := range started {
		writer.U16(crest).U16(0)
	}
	return writer.Clone()
}

// ParseCrestCancelRequest decodes CS_CH_CRESTCHALLENGECANCEL_REQ (0x1358,
// sub_343380C0): u16 crest.
func ParseCrestCancelRequest(payload []byte) (uint16, error) {
	if len(payload) < 2 {
		return 0, fmt.Errorf("crest cancel payload is too short")
	}
	return binary.LittleEndian.Uint16(payload[0:2]), nil
}

// CrestCancelPayload encodes CS_CH_CRESTCHALLENGECANCEL_ACK (0x1359,
// sub_34338D30): u16 crest, removed from the active vector with its counter.
func CrestCancelPayload(crestID uint16) []byte {
	return protocol.NewWriter(5).U8(0).U16(0).U16(crestID).Clone()
}

// CrestRewardPayload encodes CS_IN_CRESTREWARD_ACK (0x145F, sub_3420BE20,
// manager dispatcher): u32 WC balance, u16 crest, u16 percent. The client sets
// the crest's percent, clears the group counter, drops the active challenge,
// raises the completion notice and refreshes the crest screen.
func CrestRewardPayload(wc int, crestID uint16) []byte {
	return protocol.NewWriter(11).U8(0).U16(0).U32(uint32(max(wc, 0))).U16(crestID).U16(100).Clone()
}
