package game

import (
	"fmt"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
)

type ChannelUser struct {
	MemberID  byte
	SessionID uint16
	Authority byte
	// These fields are reserved by the wire layout.
	UnresolvedByte   byte
	Level            byte
	UnresolvedUint32 uint32
	PrideMarkID      uint32
	LoginName        string
	Callsign         string
	PrideName        string
}

func ChannelUsersPayload(sheet byte, users []ChannelUser) ([]byte, error) {
	if len(users) > 255 {
		return nil, fmt.Errorf("channel user count exceeds one-byte protocol limit")
	}
	writer := protocol.NewWriter(64 + len(users)*48)
	writer.Bytes([]byte{0, sheet, byte(len(users))})
	for _, user := range users {
		if err := appendChannelUser(writer, user); err != nil {
			return nil, err
		}
	}
	return writer.Clone(), nil
}

// ChannelUsersPayloads partitions a logical sheet roster on record boundaries.
// The first payload replaces the roster; continuation payloads append records.
func ChannelUsersPayloads(sheet byte, users []ChannelUser, maximumPayload int) ([][]byte, error) {
	if len(users) > 255 {
		return nil, fmt.Errorf("channel user count exceeds one-byte protocol limit")
	}
	if maximumPayload < 3 {
		return nil, fmt.Errorf("channel roster payload limit %d is too small", maximumPayload)
	}

	capacity := min(maximumPayload, 64+len(users)*48)
	result := make([][]byte, 0, 1)
	writer := protocol.NewWriter(capacity).U8(0).U8(sheet).U8(0)
	count := 0
	flush := func() {
		payload := writer.Clone()
		payload[2] = byte(count)
		result = append(result, payload)
		writer = protocol.NewWriter(capacity).U8(0).U8(sheet).U8(0)
		count = 0
	}
	for _, user := range users {
		recordWriter := protocol.NewWriter(64)
		if err := appendChannelUser(recordWriter, user); err != nil {
			return nil, err
		}
		record := recordWriter.Clone()
		if len(record)+3 > maximumPayload {
			return nil, fmt.Errorf("channel user %d record is %d bytes; payload maximum is %d", user.MemberID, len(record), maximumPayload)
		}
		if count == 255 || writer.Len()+len(record) > maximumPayload {
			flush()
		}
		writer.Bytes(record)
		count++
	}
	flush()
	return result, nil
}

// ChannelEnterPayload carries one channel-user record without the sheet/count
// prefix used by roster payloads.
func ChannelEnterPayload(user ChannelUser) ([]byte, error) {
	writer := protocol.NewWriter(64).U8(0)
	if err := appendChannelUser(writer, user); err != nil {
		return nil, err
	}
	return writer.Clone(), nil
}

// ChannelExitPayload carries one member ID.
func ChannelExitPayload(memberID byte) []byte { return []byte{0, memberID} }

func AdvertAllPayload() []byte {
	writer := protocol.NewWriter(13).U8(0)
	for range 4 {
		writer.U16(0).U8(0)
	}
	return writer.Clone()
}

func appendChannelUser(writer *protocol.Writer, user ChannelUser) error {
	if len(user.LoginName) > 255 {
		return fmt.Errorf("login name exceeds one-byte protocol limit")
	}
	callsignUnits := utf16.Encode([]rune(user.Callsign))
	if len(callsignUnits)*2 > 255 {
		return fmt.Errorf("callsign exceeds one-byte UTF-16 limit")
	}
	prideUnits := utf16.Encode([]rune(user.PrideName))
	if len(prideUnits)*2 > 255 {
		return fmt.Errorf("pride name exceeds one-byte UTF-16 limit")
	}
	writer.U8(user.MemberID).U16(user.SessionID).U8(user.Authority).U8(user.UnresolvedByte).U8(user.Level).
		U32(user.UnresolvedUint32).U32(user.PrideMarkID).
		U8(byte(len(user.LoginName))).ASCII(user.LoginName).
		U8(byte(len(callsignUnits) * 2)).UTF16LE(user.Callsign).
		U8(byte(len(prideUnits) * 2)).UTF16LE(user.PrideName)
	return nil
}
