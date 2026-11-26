package game

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

const (
	rosterPowerItemCount = 15
)

type RosterMember struct {
	Slot       byte
	Team       byte
	TeamSlot   byte
	Status     byte
	RoomStatus byte
	SessionID  uint16
	Authority  byte
	// These fields are reserved by the wire layout.
	UnresolvedByte   byte
	Level            byte
	UnresolvedUint32 uint32
	PrideMarkID      uint32
	LoginName        string
	Callsign         string
	PrideName        string
	RedCharacter     uint16
	BlueCharacter    uint16
	RedCharItems     [16]uint16
	BlueCharItems    [16]uint16
	PowerItems       [15]uint16
	GlobalIP         netip.Addr
	GlobalPort       uint16
	LocalIP          netip.Addr
	LocalPort        uint16
	// Crests are the worn delegate emblems (roster tail, 2013 sub_3418A870).
	Crests []store.CrestDelegateSlot
}

func RoomRosterPayload(room RoomSnapshot, members []RosterMember) ([]byte, error) {
	if len(members) > 255 {
		return nil, fmt.Errorf("roster count exceeds one-byte protocol limit")
	}
	titleUnits := utf16.Encode([]rune(room.Config.Title))
	if len(titleUnits)*2 > 255 {
		return nil, fmt.Errorf("room title exceeds one-byte UTF-16 length")
	}
	passwordUnits := utf16.Encode([]rune(room.Config.Password))
	if len(passwordUnits)*2 > 255 {
		return nil, fmt.Errorf("room password exceeds one-byte UTF-16 length")
	}
	writer := protocol.NewWriter(512).U8(0)
	fieldState := byte(1)
	if room.GameStarted {
		fieldState = 2
	}
	writer.U8(fieldState).U8(room.Master).U16(uint16(room.Number)).
		U8(byte(len(titleUnits) * 2)).UTF16LE(room.Config.Title).
		U8(byte(len(passwordUnits) * 2)).UTF16LE(room.Config.Password).
		U8(room.Config.Map).U32(room.Config.Mode).U8(room.Config.Wins)
	equipmentCount := room.Config.EquipmentCount
	if equipmentCount == 0 {
		// Use the default five-slot equipment shape.
		equipmentCount = 5
	}
	writer.U8(equipmentCount).Bytes(make([]byte, int(equipmentCount))).
		U16(room.Config.Time).U16(room.Config.Points).
		U8(room.Config.GameFlags)
	if !Client2012 {
		writer.U8(room.Config.WeaponFlags)
	}
	writer.U16(room.Config.HeavyWeaponLimit).
		U8(room.Config.Capacity).Bytes([]byte{0, 0}).U8(room.Config.RuleSetting).Bool(room.PowerMaster).U8(byte(len(members)))

	for _, member := range members {
		if err := appendRosterMember(writer, member); err != nil {
			return nil, err
		}
	}
	classMinimum, classMaximum := roomClassLimits(room.Config)
	writer.U8(classMinimum).U8(classMaximum)
	return writer.Clone(), nil
}

func appendRosterMember(writer *protocol.Writer, member RosterMember) error {
	loginBytes, callsignUnits, prideUnits, err := rosterNameLengths(member)
	if err != nil {
		return err
	}
	writer.U8(member.Slot)
	if !Client2012 {
		writer.U32(0)
	}
	writer.U8(member.Team).U8(member.TeamSlot).U8(member.Status).
		U16(member.SessionID).U8(member.Authority).U8(member.UnresolvedByte).U8(member.Level).
		U32(member.UnresolvedUint32).U32(member.PrideMarkID).
		U8(byte(loginBytes)).ASCII(member.LoginName).
		U8(byte(callsignUnits * 2)).UTF16LE(member.Callsign).
		U8(byte(prideUnits * 2)).UTF16LE(member.PrideName)
	if err := appendEndpoint(writer, member.GlobalIP, member.GlobalPort); err != nil {
		return err
	}
	if err := appendEndpoint(writer, member.LocalIP, member.LocalPort); err != nil {
		return err
	}
	if member.RoomStatus == 0 {
		writer.U8(1)
	} else {
		writer.U8(member.RoomStatus)
	}
	appendPowerItemRegion(writer, member.PowerItems)
	appendCharacterItems(writer, member.RedCharacter, member.RedCharItems)
	appendCharacterItems(writer, member.BlueCharacter, member.BlueCharItems)
	writer.U16(0).U8(0x33)
	appendRosterTailRecords(writer, member.Crests)
	return nil
}

func EnterRoomPayload(status byte, member RosterMember) ([]byte, error) {
	loginBytes, callsignUnits, prideUnits, err := rosterNameLengths(member)
	if err != nil {
		return nil, err
	}

	// Reserved ranges surround the slot byte after the common result.
	writer := protocol.NewWriter(256).U8(status)
	if Client2012 {
		writer.U16(0).U8(member.Slot).U8(member.Team).U8(member.TeamSlot)
	} else {
		writer.Bytes(make([]byte, 6)).U8(member.Slot).Bytes(make([]byte, 4)).U8(member.Team).U8(member.TeamSlot)
	}
	writer.
		U16(member.SessionID).U8(member.Authority).U8(member.UnresolvedByte).U8(member.Level).
		U32(member.UnresolvedUint32).U32(member.PrideMarkID).
		U8(byte(loginBytes)).ASCII(member.LoginName).
		U8(byte(callsignUnits * 2)).UTF16LE(member.Callsign).
		U8(byte(prideUnits * 2)).UTF16LE(member.PrideName)
	appendPowerItemRegion(writer, member.PowerItems)
	appendCharacterItems(writer, member.RedCharacter, member.RedCharItems)
	appendCharacterItems(writer, member.BlueCharacter, member.BlueCharItems)
	writer.U16(0).U8(0x33).U8(0).U8(0)
	if err := appendEndpoint(writer, member.GlobalIP, member.GlobalPort); err != nil {
		return nil, err
	}
	if err := appendEndpoint(writer, member.LocalIP, member.LocalPort); err != nil {
		return nil, err
	}
	appendRosterTailRecords(writer, member.Crests)
	return writer.Clone(), nil
}

func rosterNameLengths(member RosterMember) (int, int, int, error) {
	loginBytes := len(member.LoginName)
	if loginBytes > 255 {
		return 0, 0, 0, fmt.Errorf("login name %q exceeds roster length", member.LoginName)
	}
	callsignUnits := len(utf16.Encode([]rune(member.Callsign)))
	if callsignUnits*2 > 255 {
		return 0, 0, 0, fmt.Errorf("callsign %q exceeds roster length", member.Callsign)
	}
	prideUnits := len(utf16.Encode([]rune(member.PrideName)))
	if prideUnits*2 > 255 {
		return 0, 0, 0, fmt.Errorf("pride name %q exceeds roster length", member.PrideName)
	}
	return loginBytes, callsignUnits, prideUnits, nil
}

// Team loadouts are counted lists. The first value is the base character ID;
// remaining values are installed character items. A character with no saved
// equipment uses [character, character], where the second value is its base
// body item.
func appendCharacterItems(writer *protocol.Writer, character uint16, items [16]uint16) {
	count := 1
	for _, itemID := range items {
		if itemID != 0 {
			count++
		}
	}
	if count == 1 {
		count = 2
	}
	writer.U8(byte(count)).U16(character)
	if count == 2 && items == ([16]uint16{}) {
		writer.U16(character)
		return
	}
	for _, itemID := range items {
		if itemID != 0 {
			writer.U16(itemID)
		}
	}
}

func appendPowerItemRegion(writer *protocol.Writer, powerItems [15]uint16) {
	// The region contains a selector, 15 values, and a trailing u16.
	writer.U8(1).U8(0)
	for field := 0; field < rosterPowerItemCount; field++ {
		writer.U16(powerItems[field])
	}
	writer.U16(0)
}

// appendRosterTailRecords writes the member's crest vector: u8 n, n x {u8
// slot, u16 crest} (2013 member parser sub_3418A870 and ENTER_ACK
// sub_34187040 -> player +108). The client does not clamp n and draws at most
// three emblems, so the list is capped at three.
func appendRosterTailRecords(writer *protocol.Writer, crests []store.CrestDelegateSlot) {
	crests = crests[:min(len(crests), 3)]
	writer.U8(byte(len(crests)))
	for _, crest := range crests {
		writer.U8(crest.Slot).U16(crest.CrestID)
	}
}

func appendEndpoint(writer *protocol.Writer, address netip.Addr, port uint16) error {
	if !address.Is4() {
		return fmt.Errorf("roster endpoint must be IPv4")
	}
	ip := address.As4()
	writer.Bytes(ip[:])
	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], port)
	writer.Bytes(portBytes[:])
	return nil
}
