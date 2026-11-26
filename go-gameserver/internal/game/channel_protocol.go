package game

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

type LoginCredentials struct {
	Username string
	Password string
}

// channelKind is the persistent channel presentation/gameplay category.
type channelKind = store.ChannelType

const (
	channelKindNormal        = store.ChannelTypeNormal
	channelKindSpat          = store.ChannelTypeSpat
	channelKindPrideBattle   = store.ChannelTypePrideBattle
	channelKindLadder        = store.ChannelTypeLadder
	channelKindPrivateLadder = store.ChannelTypePrivateLadder
)

func ParseLoginCredentials(payload []byte) (LoginCredentials, error) {
	if len(payload) < 5 {
		return LoginCredentials{}, fmt.Errorf("login payload is too short")
	}
	hashLength := int(payload[2])
	nameLengthOffset := 3 + hashLength
	if nameLengthOffset >= len(payload) {
		return LoginCredentials{}, fmt.Errorf("login hash length %d exceeds payload", hashLength)
	}
	nameLength := int(payload[nameLengthOffset])
	nameOffset := nameLengthOffset + 1
	passwordLengthOffset := nameOffset + nameLength
	if passwordLengthOffset >= len(payload) {
		return LoginCredentials{}, fmt.Errorf("login name length %d exceeds payload", nameLength)
	}
	passwordLength := int(payload[passwordLengthOffset])
	passwordOffset := passwordLengthOffset + 1
	if passwordOffset+passwordLength > len(payload) {
		return LoginCredentials{}, fmt.Errorf("login password length %d exceeds payload", passwordLength)
	}
	return LoginCredentials{
		Username: lowBytesString(payload[nameOffset:passwordLengthOffset]),
		Password: lowBytesString(payload[passwordOffset : passwordOffset+passwordLength]),
	}, nil
}

// EventTopBanner is the CS_CH_LOGIN_ACK byte that 2013 TR (sub_3408DA10)
// passes to the interface manager setter (vtable +228, field +0x45E58); a
// non-zero value N shows WT_Interface\all\event_topN.dtx as the lobby and
// server-list top banner ("EventTop", sub_34161530 / sub_3408E1B0). The same
// setter is driven by CS_CH_YEAREVENT_ACK (0x133D). Set from WT_EVENT_TOP.
// Client2012 selects the September 2012 TR wire layouts instead of 2013 TR.
var Client2012 bool = false

var EventTopBanner byte = 0

func LoginReplyPayload(status byte, sessionID uint16, udpKey uint32, channelType store.ChannelType, info store.AccountSnapshot) ([]byte, error) {
	if !channelType.Valid() {
		return nil, fmt.Errorf("invalid channel type %d", channelType)
	}
	// The fixed account block is followed by mute/ban interval and reason
	// fields.
	writer := protocol.NewWriter(96).
		U8(status).
		U16(sessionID).
		U32(udpKey).
		U32(uint32(info.Gold)).
		U32(0).                   // reserved
		U16(uint16(channelType)). // configured channel category
		U32(uint32(info.WC))
	writer.Bool(info.Muted).
		U8(EventTopBanner). // event_topN.dtx banner index (0 = hidden)
		U32(uint32(info.Cash))
	if err := writeMuteBanMetadata(
		writer, info.Muted, info.MuteStartedAt, info.MuteExpiresAt, info.MuteReason,
	); err != nil {
		return nil, err
	}
	return writer.Clone(), nil
}

func UDPAddressPayload() []byte { return []byte{0} }

func UDPSuccessPayload(address netip.Addr, port uint16) ([]byte, error) {
	if !address.Is4() {
		return nil, fmt.Errorf("match address must be IPv4")
	}
	payload := make([]byte, 13)
	ip := address.As4()
	copy(payload[1:5], ip[:])
	binary.BigEndian.PutUint16(payload[5:7], port)
	copy(payload[7:11], ip[:])
	binary.BigEndian.PutUint16(payload[11:13], port)
	return payload, nil
}

func SelectCharPayload(address netip.Addr, port uint16) ([]byte, error) {
	if !address.Is4() {
		return nil, fmt.Errorf("match address must be IPv4")
	}
	// The final byte is a counted list of u16 values. The server currently
	// advertises an empty list.
	payload := make([]byte, 8)
	ip := address.As4()
	copy(payload[1:5], ip[:])
	binary.BigEndian.PutUint16(payload[5:7], port)
	return payload, nil
}

func DisconnectPayload() []byte { return []byte{0} }

func LogoutPayload() []byte { return []byte{0} }

func UserInfoPayload(loginName string, info store.AccountSnapshot) ([]byte, error) {
	return UserInfoWithPridePayload(loginName, info, store.Pride{}, store.PrideMember{})
}

// UserInfoWithPridePayload serializes the account and pride state used during
// login. Standalone pride messages update the same state later.
func UserInfoWithPridePayload(loginName string, info store.AccountSnapshot, pride store.Pride, member store.PrideMember) ([]byte, error) {
	return userInfoWithPrideAndPlayTimePayload(loginName, info, pride, member, 0, info.PlayTimeSeconds)
}

func userInfoWithPrideAndPlayTimePayload(loginName string, info store.AccountSnapshot, pride store.Pride, member store.PrideMember, schoolID, playTimeSeconds uint32) ([]byte, error) {
	units := utf16.Encode([]rune(loginName))
	if len(units)*2 > 255 {
		return nil, fmt.Errorf("login name exceeds one-byte UTF-16 length")
	}
	writer := protocol.NewWriter(128).U8(0).U8(byte(len(units) * 2)).UTF16LE(loginName)
	// Fields are written separately so the layout remains explicit instead of
	// being retained as an opaque byte template.
	writer.
		U8(info.Authority). // known special values include 10, 12, and 13
		U8(0).              // reserved
		U32(schoolID).
		U32(uint32(info.Gold)).
		U32(uint32(info.WC)).
		U32(uint32(info.Cash)).
		U32(uint32(store.LevelBaseThreshold(info.Level))). // gauge base (+2156)
		U32(uint32(info.GP)).
		U32(uint32(store.NextLevelThreshold(info.Level))).
		U8(0). // reserved
		U8(byte(info.Level)).
		// 2013 TR CS_CH_USERINFO_ACK (sub_34188610) reads 12 u32 here, then a
		// u8 P.T. bonus index (the 2013 reference layout; 2012 had 13 u32 and
		// no bonus byte). #1..#11 keep the order verified on the 2012 client:
		// #1 rank, #7/#8 kills/deaths, #10 headshots; #9/#11 order unverified.
		U32(0). // #1 overall ranking position; the current store does not track it
		U32(0). // #2 reserved
		U32(nonNegativeU32(info.Draws)).
		U32(nonNegativeU32(info.Wins)).
		U32(nonNegativeU32(info.Losses)).
		U32(pride.ID). // #6 PrideMarkID / Clan mark
		U32(nonNegativeU32(info.Kills)).
		U32(nonNegativeU32(info.Deaths)).
		U32(nonNegativeU32(info.SpecialWeaponKills)). // #9 (unverified)
		U32(nonNegativeU32(info.HeadshotKills)).      // #10
		U32(nonNegativeU32(info.WolfKills)).          // #11 (unverified)
		U32(playTimeSeconds)                          // #12 play time
	if Client2012 {
		writer.U32(0) // #13 (2012 had 13 u32 and no P.T. bonus byte)
	} else {
		writer.U8(0) // P.T. bonus entry index (2013 TR)
	}
	writer.
		U32(0).                                       // pending Pride application ID
		U8(0).                                        // empty pending Pride name
		U32(pride.ID)
	if pride.ID == 0 {
		// The client stops reading when the Pride ID is zero.
		return writer.Clone(), nil
	}
	appendUserInfoPrideBlock(writer, pride, member)
	return writer.Clone(), nil
}

// appendUserInfoPrideBlock is the Pride record that USERINFO (0x1335,
// sub_34188610) and CHARSTDINFO (0x1325, sub_3420CC80) both parse into the
// same object offsets (+664 .. +1572) after a non-zero Pride ID.
func appendUserInfoPrideBlock(writer *protocol.Writer, pride store.Pride, member store.PrideMember) {
	writePrideUTF16(writer, pride.Name, 31)
	writePrideUTF16(writer, pride.Master, 31)
	memberCountByte := byte(min(max(pride.MemberCount, 1), 255))
	writer.U32(nonNegativeU32(pride.RankPoints)).U8(memberCountByte)
	writePrideASCII(writer, pride.CreatedAt.UTC().Format("20060102"), 21)
	writer.U16(uint16(min(max(pride.MemberCount, 0), 65535)))
	writePrideUTF16(writer, pride.Intro, 127)
	writePrideUTF16(writer, pride.Notice, 127)
	grade := prideGrade(pride, member.Role)
	writer.U8(grade)
	if grade > 9 {
		writePrideASCII(writer, prideDate(pride.CloseAt), 31)
	}
	// +1594/+1595 are stored but never read by the 2013 client.
	writer.U8(member.Role).U8(0)
	writePrideASCII(writer, "", 31)
	writer.U8(0).U8(0) // empty pride-chain names and crest IDs
}

// CharStdInfoRequest is CS_CH_GETCHARSTDINFO_REQ (0x1324, sub_342073E0): u8 n
// + ASCII login, u8 n + UTF-16 callsign (the login is empty when the panel is
// opened from a room roster, which carries no login).
type CharStdInfoRequest struct {
	LoginName string
	Callsign  string
}

func ParseCharStdInfoRequest(payload []byte) (CharStdInfoRequest, error) {
	if len(payload) < 2 {
		return CharStdInfoRequest{}, fmt.Errorf("char-std-info payload is too short")
	}
	loginBytes := int(payload[0])
	if 1+loginBytes >= len(payload) {
		return CharStdInfoRequest{}, fmt.Errorf("char-std-info login exceeds payload")
	}
	callsignBytes := int(payload[1+loginBytes])
	start := 2 + loginBytes
	if callsignBytes%2 != 0 || start+callsignBytes > len(payload) {
		return CharStdInfoRequest{}, fmt.Errorf("char-std-info callsign exceeds payload")
	}
	return CharStdInfoRequest{
		LoginName: lowBytesString(payload[1 : 1+loginBytes]),
		Callsign:  decodeUTF16Low(payload[start : start+callsignBytes]),
	}, nil
}

// CharStdInfoPayload encodes CS_CH_GETCHARSTDINFO_ACK (0x1325, sub_3420CC80,
// the other-player information panel). It is USERINFO without WC, play time,
// P.T. bonus and pending Pride fields, followed by u8 n x {u8 slot, u16 crest,
// u16} worn crests.
func CharStdInfoPayload(callsign string, info store.AccountSnapshot, pride store.Pride, member store.PrideMember, schoolID uint32, crests []store.CrestDelegateSlot) ([]byte, error) {
	units := utf16.Encode([]rune(callsign))
	if len(units)*2 > 255 {
		return nil, fmt.Errorf("callsign exceeds one-byte UTF-16 length")
	}
	writer := protocol.NewWriter(160).U8(0).U8(byte(len(units) * 2)).UTF16LE(callsign).
		U8(info.Authority).U8(0).U32(schoolID).
		U32(uint32(info.Gold)).U32(uint32(info.Cash)).
		U32(uint32(store.LevelBaseThreshold(info.Level))).U32(uint32(info.GP)).
		U32(uint32(store.NextLevelThreshold(info.Level))).U8(0).U8(byte(info.Level)).
		U32(0).U32(0). // overall rank position, reserved (as USERINFO #1/#2)
		U32(nonNegativeU32(info.Draws)).U32(nonNegativeU32(info.Wins)).U32(nonNegativeU32(info.Losses)).
		U32(pride.ID).
		U32(nonNegativeU32(info.Kills)).U32(nonNegativeU32(info.Deaths)).
		U32(nonNegativeU32(info.SpecialWeaponKills)).U32(nonNegativeU32(info.HeadshotKills)).
		U32(nonNegativeU32(info.WolfKills)).
		U32(pride.ID)
	if pride.ID != 0 {
		appendUserInfoPrideBlock(writer, pride, member)
	}
	crests = crests[:min(len(crests), 3)]
	writer.U8(byte(len(crests)))
	for _, crest := range crests {
		writer.U8(crest.Slot).U16(crest.CrestID).U16(0)
	}
	return writer.Clone(), nil
}

func lowBytesString(value []byte) string {
	runes := make([]rune, len(value))
	for index, b := range value {
		runes[index] = rune(b)
	}
	return string(runes)
}
