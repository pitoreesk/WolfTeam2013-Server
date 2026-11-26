package game

import (
	"encoding/binary"
	"fmt"
	"sort"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

func ParseRoomConfig(payload []byte) (RoomConfig, error) {
	return parseRoomConfig(payload, true, false)
}

func ParseRoomConfigForChannel(payload []byte, channelType store.ChannelType) (RoomConfig, error) {
	return parseRoomConfig(payload, true, channelType == store.ChannelTypeLadder || channelType == store.ChannelTypePrivateLadder)
}

func parseRoomConfig(payload []byte, includeClassLimits, includeChannelExtras bool) (RoomConfig, error) {
	if len(payload) < 2 {
		return RoomConfig{}, fmt.Errorf("room config payload is too short")
	}
	titleBytes := int(payload[0])
	passwordLengthOffset := 1 + titleBytes
	if titleBytes%2 != 0 || passwordLengthOffset >= len(payload) {
		return RoomConfig{}, fmt.Errorf("invalid room title length %d", titleBytes)
	}
	passwordBytes := int(payload[passwordLengthOffset])
	fieldsOffset := passwordLengthOffset + 1 + passwordBytes
	fixedLen := 16
	if Client2012 {
		fixedLen = 15
	}
	// 2013 TR fixed fields: map, u32 mode, wins, equipment count, u16 time,
	// u16 points, flags, weapon flags, u16 heavy-weapon limit, capacity = 16.
	// 2012 TR omits weapon flags (15 bytes).
	if passwordBytes%2 != 0 || fieldsOffset+fixedLen > len(payload) {
		return RoomConfig{}, fmt.Errorf("invalid room password or fixed field length")
	}
	config := RoomConfig{
		Title:    decodeUTF16Low(payload[1:passwordLengthOffset]),
		Password: decodeUTF16Low(payload[passwordLengthOffset+1 : fieldsOffset]),
	}
	offset := fieldsOffset
	config.Map = payload[offset]
	offset++
	config.Mode = binary.LittleEndian.Uint32(payload[offset : offset+4])
	offset += 4
	config.Wins = payload[offset]
	offset++
	config.EquipmentCount = payload[offset]
	offset++
	config.Time = binary.LittleEndian.Uint16(payload[offset : offset+2])
	offset += 2
	config.Points = binary.LittleEndian.Uint16(payload[offset : offset+2])
	offset += 2
	config.GameFlags = payload[offset]
	offset++
	if !Client2012 {
		// 2013 TR writes the weapon-flags byte after the flags again
		// (CS_FD_CREATE_REQ sub_34184270, CS_FD_CHANGERULE_REQ sub_34249710); the
		// 2012 client had none.
		config.WeaponFlags = payload[offset]
		offset++
	}
	config.HeavyWeaponLimit = binary.LittleEndian.Uint16(payload[offset : offset+2])
	offset += 2
	config.Capacity = payload[offset]
	offset++
	// Two metadata bytes follow the public room capacity.
	if offset < len(payload) {
		config.MapSetting = payload[offset]
		offset++
	}
	if offset < len(payload) {
		config.RuleSetting = payload[offset]
		offset++
	}
	// Create-room messages have ten metadata bytes after RuleSetting; the class
	// bounds are the seventh and eighth bytes. IDA shows that channel types 3/4
	// use the first two u16 values in this same region. Keep them without naming
	// their meaning until a client consumer proves it.
	if len(payload)-offset >= 10 {
		if includeChannelExtras {
			config.ChannelExtraU16A = binary.LittleEndian.Uint16(payload[offset : offset+2])
			config.ChannelExtraU16B = binary.LittleEndian.Uint16(payload[offset+2 : offset+4])
			config.channelExtrasSet = true
		}
		if !includeClassLimits {
			return config, nil
		}
		config.ClassLimitMin = payload[offset+6]
		config.ClassLimitMax = payload[offset+7]
		config.classLimitsSet = true
	}
	return config, nil
}

type EnterRoomRequest struct {
	Number   int16
	Password string
}

func ParseEnterRoomRequest(payload []byte) (EnterRoomRequest, error) {
	if len(payload) < 2 {
		return EnterRoomRequest{}, fmt.Errorf("enter-room payload is too short")
	}
	request := EnterRoomRequest{Number: int16(binary.LittleEndian.Uint16(payload[:2]))}
	// Keep accepting the two-byte form used by server-selected quick entry. The
	// extended form appends a counted UTF-16 password and one mode byte.
	if len(payload) == 2 {
		return request, nil
	}
	passwordBytes := int(payload[2])
	if passwordBytes%2 != 0 || 3+passwordBytes > len(payload) {
		return EnterRoomRequest{}, fmt.Errorf("invalid enter-room password length %d", passwordBytes)
	}
	request.Password = decodeUTF16Low(payload[3 : 3+passwordBytes])
	return request, nil
}

func CreateRoomPayload(roomNumber int16, config RoomConfig) []byte {
	classMinimum, classMaximum := roomClassLimits(config)
	// 2013 TR CS_FD_CREATE_ACK (sub_3420E360) skips a u32 before the u16 room
	// number (the 2013 reference layout; 2012 had no u32).
	w := protocol.NewWriter(9).U8(0)
	if !Client2012 {
		w.U32(0)
	}
	return w.U16(uint16(roomNumber)).
		U8(classMinimum).
		U8(classMaximum).
		Clone()
}

func FieldListPayload(rooms []RoomSnapshot) ([]byte, error) {
	if len(rooms) > 255 {
		return nil, fmt.Errorf("room count exceeds one-byte protocol limit")
	}
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].Number < rooms[j].Number })
	// 2013 TR CS_FD_FIELDLIST_ACK (sub_34213320) reads two u8 before the room
	// count (the 2013 reference layout; 2012 read the count directly).
	writer := protocol.NewWriter(256)
	if Client2012 {
		writer.Bytes([]byte{0, byte(len(rooms))})
	} else {
		writer.Bytes([]byte{0, 0, 0, byte(len(rooms))})
	}
	for _, room := range rooms {
		if err := appendFieldEntry(writer, room); err != nil {
			return nil, err
		}
	}
	return writer.Clone(), nil
}

// FieldCreatePayload uses the full room entry format with a leading common
// status byte.
func FieldCreatePayload(room RoomSnapshot) ([]byte, error) {
	return singleFieldPayload(room)
}

func FieldStatePayload(room RoomSnapshot) ([]byte, error) {
	units := utf16.Encode([]rune(room.Config.Title))
	if len(units)*2 > 255 {
		return nil, fmt.Errorf("room title exceeds one-byte UTF-16 length")
	}
	writer := protocol.NewWriter(32).U8(0).U16(uint16(room.Number)).U8(byte(len(units) * 2)).UTF16LE(room.Config.Title).
		Bool(room.Config.Password != "").U8(room.Config.Map).U32(room.Config.Mode).U8(room.Config.Wins).
		U16(room.Config.Time).U16(room.Config.Points).U8(room.Config.GameFlags)
	if !Client2012 {
		writer.U8(room.Config.WeaponFlags)
	}
	writer.U16(room.Config.HeavyWeaponLimit).U8(room.Config.Capacity).U8(room.Config.RuleSetting)
	return writer.Clone(), nil
}

func singleFieldPayload(room RoomSnapshot) ([]byte, error) {
	writer := protocol.NewWriter(32).U8(0)
	if err := appendFieldEntry(writer, room); err != nil {
		return nil, err
	}
	return writer.Clone(), nil
}

func appendFieldEntry(writer *protocol.Writer, room RoomSnapshot) error {
	units := utf16.Encode([]rune(room.Config.Title))
	if len(units)*2 > 255 {
		return fmt.Errorf("room title exceeds one-byte UTF-16 length")
	}
	powerMaster := byte(0)
	if room.PowerMaster {
		powerMaster = 1
	}
	classMinimum, classMaximum := roomClassLimits(room.Config)
	writer.U16(uint16(room.Number)).U8(byte(len(units) * 2)).UTF16LE(room.Config.Title).
		Bool(room.Config.Password != "").Bool(room.GameStarted).
		U8(room.Config.Map).U32(room.Config.Mode).U8(room.Config.Wins).
		U16(room.Config.Time).U16(room.Config.Points).U8(room.Config.GameFlags)
	if !Client2012 {
		writer.U8(room.Config.WeaponFlags)
	}
	writer.U16(room.Config.HeavyWeaponLimit).U8(room.Config.Capacity).U8(room.Users).U8(room.Config.RuleSetting).
		Bytes([]byte{0, powerMaster, classMinimum, classMaximum})
	return nil
}

func FieldExitPayload(roomNumber int16) []byte {
	return protocol.NewWriter(3).U8(0).U16(uint16(roomNumber)).Clone()
}

func GameStatePayload(roomNumber int16, started bool) []byte {
	return protocol.NewWriter(4).U8(0).U16(uint16(roomNumber)).Bool(started).Clone()
}

func FieldUsersPayload(roomNumber int16, users byte) []byte {
	return protocol.NewWriter(4).U8(0).U16(uint16(roomNumber)).U8(users).Clone()
}

func JoinAllowPayload(rooms []RoomSnapshot, level byte) ([]byte, error) {
	allowed := make([]int16, 0, len(rooms))
	for _, room := range rooms {
		if room.Open && (room.Config.Capacity == 0 || room.Users < room.Config.Capacity) &&
			roomAllowsLevel(room.Config, level) {
			allowed = append(allowed, room.Number)
		}
	}
	if len(allowed) > 255 {
		return nil, fmt.Errorf("join-allow room count exceeds one-byte protocol limit")
	}
	sort.Slice(allowed, func(i, j int) bool { return allowed[i] < allowed[j] })
	writer := protocol.NewWriter(2 + len(allowed)*2).U8(0).U8(byte(len(allowed)))
	for _, roomNumber := range allowed {
		writer.U16(uint16(roomNumber))
	}
	return writer.Clone(), nil
}

func ExitRoomPayload(slot byte) []byte {
	// The final byte selects a secondary notification. The server uses the
	// ordinary-exit path.
	return protocol.NewWriter(3).U8(0).U8(slot).U8(0).Clone()
}

func KickPayload() []byte {
	return []byte{0}
}

func ChangeTeamPayload(slot, team, teamSlot byte) []byte {
	return protocol.NewWriter(4).U8(0).U8(slot).U8(team).U8(teamSlot).Clone()
}

func ChangeTeamFailurePayload(status byte) []byte {
	return []byte{status}
}

func ChangeSlotPayload(slot, status byte) []byte {
	return protocol.NewWriter(3).U8(0).U8(slot).U8(status).Clone()
}

func ChangeBossPayload(slot byte) []byte {
	return protocol.NewWriter(3).U8(0).U8(slot).U8(0).Clone()
}

func StartGamePayload(status byte) []byte {
	if status != 0 {
		return []byte{status}
	}
	return []byte{0, 0}
}

type RoundPlayerState struct {
	Slot byte
	Team byte
}

func StartRoundPayload(round byte, seconds, redScore, blueScore uint16, players []RoundPlayerState) []byte {
	writer := protocol.NewWriter(7 + len(players)*2).U8(0).U8(round).U16(roundTimerValue(seconds)).
		U8(byte(redScore)).U8(byte(blueScore)).U8(byte(len(players)))
	for _, player := range players {
		writer.U8(player.Slot).U8(player.Team)
	}
	return writer.Clone()
}

func roundTimerValue(seconds uint16) uint16 {
	if seconds > ^uint16(0)-5 {
		return ^uint16(0)
	}
	return seconds + 5
}

func ChangeRulePayload(request []byte) ([]byte, RoomConfig, error) {
	config, err := parseRoomConfig(request, false, false)
	if err != nil {
		return nil, RoomConfig{}, err
	}
	titleUnits := utf16.Encode([]rune(config.Title))
	passwordUnits := utf16.Encode([]rune(config.Password))
	if len(titleUnits)*2 > 255 || len(passwordUnits)*2 > 255 {
		return nil, RoomConfig{}, fmt.Errorf("change-rule title or password exceeds one-byte UTF-16 length")
	}

	return ChangeRuleConfigPayload(config), config, nil
}

// ChangeRuleConfigPayload encodes CHANGERULE_ACK 0x1615 from a room config.
// The reply is a compact room config rather than an echo of the longer
// request; it omits MapSetting and trailing request metadata.
func ChangeRuleConfigPayload(config RoomConfig) []byte {
	titleUnits := utf16.Encode([]rune(truncateUTF16(config.Title, 127)))
	passwordUnits := utf16.Encode([]rune(truncateUTF16(config.Password, 127)))
	writer := protocol.NewWriter(32 + len(titleUnits)*2 + len(passwordUnits)*2).
		U8(0).U8(byte(len(titleUnits) * 2)).UTF16LE(string(utf16.Decode(titleUnits))).
		U8(byte(len(passwordUnits) * 2)).UTF16LE(string(utf16.Decode(passwordUnits))).
		U8(config.Map).U32(config.Mode).U8(config.Wins).U8(config.EquipmentCount).
		U16(config.Time).U16(config.Points).U8(config.GameFlags)
	if !Client2012 {
		writer.U8(config.WeaponFlags)
	}
	writer.U16(config.HeavyWeaponLimit).U8(config.Capacity).U8(config.RuleSetting).U16(config.Points)
	return writer.Clone()
}

func ReadyPayload(slot, ready byte) []byte {
	return protocol.NewWriter(3).U8(0).U8(slot).U8(ready).Clone()
}

func UDPStartPayload(slot byte) []byte {
	return protocol.NewWriter(2).U8(0).U8(slot).Clone()
}

// EnterGamePayload encodes 2012 ENTERGAME_ACK (sub_341CB490): slot, slot
// status (player +35) and the room's current round (room +42).
func EnterGamePayload(slot, status, round byte) []byte {
	return protocol.NewWriter(4).U8(0).U8(slot).U8(status).U8(round).Clone()
}

func decodeUTF16Low(value []byte) string {
	units := make([]uint16, 0, len(value)/2)
	for offset := 0; offset+1 < len(value); offset += 2 {
		units = append(units, binary.LittleEndian.Uint16(value[offset:offset+2]))
	}
	return string(utf16.Decode(units))
}
