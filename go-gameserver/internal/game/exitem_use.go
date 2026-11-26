package game

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

// exItemUse is one generated exItemUses entry.
type exItemUse struct {
	Type         int
	Gold, WC, GP int
}

// exItemEffect maps a CS_IN_USEITEM_REQ to its server effect. The client
// picks its reply message from the same use type (strings 7000/7500 + type,
// sub_34226820): 1 callsign (text = new name), 2 kill/death reset, 3
// win/lose reset, 4 Pride name (text = new name), coupons (Gold/KP/GP).
// Lucky boxes (25, 26) use 0x1422 and Full-up kits (12) use 0x1420.
func exItemEffect(request GenericUseItemRequest) (store.ExItemEffect, bool) {
	use, ok := exItemUses[request.ItemID]
	if !ok {
		return store.ExItemEffect{}, false
	}
	effect := store.ExItemEffect{Type: use.Type}
	switch {
	case use.Type == 1:
		effect.NewCallsign = request.Text
	case use.Type == 2:
		effect.ResetKills = true
	case use.Type == 3:
		effect.ResetWins = true
	case use.Type == 4:
		effect.NewPrideName = request.Text
	case use.Gold > 0 || use.WC > 0 || use.GP > 0:
		effect.Gold, effect.WC, effect.GP = use.Gold, use.WC, use.GP
	default:
		return store.ExItemEffect{}, false
	}
	if (use.Type == 1 || use.Type == 4) && request.Text == "" {
		return store.ExItemEffect{}, false
	}
	return effect, true
}

// FullUpRequest is CS_IN_USEFULLUPITEM_REQ (0x1420, sub_342C0640): u32
// weapon unique, 4 x u16 target upgrade values (the weapon's full-upgrade
// set from the client's weapon data +1836..+1843), u32 kit unique, u16 kit.
type FullUpRequest struct {
	WeaponUnique uint32
	Values       [4]uint16
	KitUnique    uint32
	KitID        uint16
}

func ParseFullUpRequest(payload []byte) (FullUpRequest, error) {
	if len(payload) < 18 {
		return FullUpRequest{}, fmt.Errorf("full-up payload is too short")
	}
	request := FullUpRequest{
		WeaponUnique: binary.LittleEndian.Uint32(payload[0:4]),
		KitUnique:    binary.LittleEndian.Uint32(payload[12:16]),
		KitID:        binary.LittleEndian.Uint16(payload[16:18]),
	}
	for i := range request.Values {
		request.Values[i] = binary.LittleEndian.Uint16(payload[4+2*i:])
	}
	return request, nil
}

// FullUpReplyPayload encodes CS_IN_USEFULLUPITEM_ACK (0x1421, sub_342C0790):
// u32 weapon unique, str8 weapon expiry, u16 weapon count, u32 kit unique,
// u16 kit units left (0 removes the kit). The client copies the upgrade
// values from its own weapon data.
func FullUpReplyPayload(request FullUpRequest, expiry string, count uint16, kitLeft uint16) []byte {
	writer := protocol.NewWriter(16 + len(expiry)).U8(0).U32(request.WeaponUnique).
		U8(byte(len(expiry))).ASCII(expiry).U16(count).U32(request.KitUnique).U16(kitLeft)
	return writer.Clone()
}

func (s *Server) handleFullUpItem(ctx context.Context, session *Session, payload []byte) error {
	request, err := ParseFullUpRequest(payload)
	if err != nil {
		return session.Write(ReplyUseFullUpItem, []byte{1})
	}
	weaponID := int(request.WeaponUnique) - 2_000_000_000
	use, isKit := exItemUses[int(request.KitID)]
	exItems, ok := s.users.(store.ExItemStore)
	_, _, userID, _ := session.identity()
	if !ok || !isKit || use.Type != 12 || weaponID < 1 || weaponID > 9999 {
		return session.Write(ReplyUseFullUpItem, []byte{1})
	}
	done, err := exItems.FullUpgrade(ctx, userID, weaponID, int(request.KitID), request.Values)
	if err != nil {
		s.logBackendFallback("full upgrade", userID, err)
	}
	s.logger.Info("full-up kit use", "user_id", userID, "weapon", weaponID, "kit", request.KitID, "values", request.Values, "done", done)
	if !done {
		return session.Write(ReplyUseFullUpItem, []byte{1})
	}
	session.invalidateRosterState()
	items, err := s.users.Items(ctx, userID)
	if err != nil {
		s.logBackendFallback("reload inventory after full upgrade", userID, err)
	}
	expiry, count, kitLeft := string(unlimitedExpiryText), uint16(0xffff), 0
	weaponQuantity := 0
	for _, item := range items {
		switch item.ID {
		case weaponID:
			weaponQuantity += max(item.Quantity, 1)
			expiry = string(expiryStampFrom(item.Expiry))
		case int(request.KitID):
			kitLeft += max(item.Quantity, 1)
		}
	}
	if weaponQuantity > 0 {
		count = uint16(expiryStamp(expiry).weaponUnits(weaponQuantity, time.Now()))
	}
	return session.Write(ReplyUseFullUpItem, FullUpReplyPayload(request, expiry, count, uint16(min(kitLeft, 0xffff))))
}
