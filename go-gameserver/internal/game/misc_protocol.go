package game

import (
	"encoding/binary"
	"fmt"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
)

type ChannelChatRequest struct {
	Type   byte
	Color  uint32
	Prefix string
	Text   string
}

// ParseChannelChatRequest parses CS_CH_CHAT_REQ (0x130E). 2013 TR builders
// (sub_3420DDD0, sub_34248D70, sub_342E1D20): u8 type, u32 color (two u16),
// u8 prefix byte length + UTF-16 prefix (0), u8 byte length + UTF-16 text
// (the 2013 reference layout; 2012 had no color and no prefix).
func ParseChannelChatRequest(request []byte) (ChannelChatRequest, error) {
	if Client2012 {
		if len(request) < 2 {
			return ChannelChatRequest{}, fmt.Errorf("channel chat payload is too short")
		}
		textBytes := int(request[1])
		if textBytes%2 != 0 || 2+textBytes > len(request) {
			return ChannelChatRequest{}, fmt.Errorf("invalid channel chat UTF-16 length %d", textBytes)
		}
		return ChannelChatRequest{
			Type: request[0],
			Text: decodeUTF16Low(request[2 : 2+textBytes]),
		}, nil
	}
	if len(request) < 7 {
		return ChannelChatRequest{}, fmt.Errorf("channel chat payload is too short")
	}
	prefixBytes := int(request[5])
	if prefixBytes%2 != 0 || 6+prefixBytes >= len(request) {
		return ChannelChatRequest{}, fmt.Errorf("invalid channel chat prefix length %d", prefixBytes)
	}
	textOffset := 6 + prefixBytes
	textBytes := int(request[textOffset])
	if textBytes%2 != 0 || textOffset+1+textBytes > len(request) {
		return ChannelChatRequest{}, fmt.Errorf("invalid channel chat UTF-16 length %d", textBytes)
	}
	return ChannelChatRequest{
		Type:   request[0],
		Color:  binary.LittleEndian.Uint32(request[1:5]),
		Prefix: decodeUTF16Low(request[6:textOffset]),
		Text:   decodeUTF16Low(request[textOffset+1 : textOffset+1+textBytes]),
	}, nil
}

// ChannelChatPayload builds CS_CH_CHAT_ACK (2013 TR sub_342105D0/sub_3424B8F0/
// sub_342E4150; 12/13 checked as authority as in 2012): u8 type, u8 sender authority (12/13 = GM styling), u32 color,
// UTF-16 sender, UTF-16 prefix, UTF-16 text.
func ChannelChatPayload(request []byte, callsign string, authority byte, powerUser bool, powerUserColor uint32, powerUserLabel string) ([]byte, error) {
	chat, err := ParseChannelChatRequest(request)
	if err != nil {
		return nil, err
	}
	nameUnits := utf16.Encode([]rune(callsign))
	if len(nameUnits)*2 > 255 {
		return nil, fmt.Errorf("chat sender exceeds one-byte UTF-16 limit")
	}
	textUnits := utf16.Encode([]rune(chat.Text))
	if len(textUnits)*2 > 255 {
		return nil, fmt.Errorf("chat text exceeds one-byte UTF-16 limit")
	}
	if Client2012 {
		return protocol.NewWriter(6 + len(nameUnits)*2 + len(textUnits)*2).
			U8(0).U8(chat.Type).U8(authority).
			U8(byte(len(nameUnits) * 2)).UTF16LE(callsign).
			U8(byte(len(textUnits) * 2)).UTF16LE(chat.Text).
			Clone(), nil
	}
	color := uint32(0)
	prefix := ""
	if powerUser {
		color = chat.Color
		if powerUserColor != 0 {
			color = powerUserColor
		}
		prefix = powerUserLabel
	}
	prefixUnits := utf16.Encode([]rune(prefix))
	if len(prefixUnits)*2 > 255 {
		return nil, fmt.Errorf("chat prefix exceeds one-byte UTF-16 limit")
	}
	writer := protocol.NewWriter(10 + len(nameUnits)*2 + len(prefixUnits)*2 + len(textUnits)*2).
		U8(0).U8(chat.Type).U8(authority).U32(color).
		U8(byte(len(nameUnits) * 2)).UTF16LE(callsign).
		U8(byte(len(prefixUnits) * 2)).UTF16LE(prefix).
		U8(byte(len(textUnits) * 2)).UTF16LE(chat.Text)
	return writer.Clone(), nil
}

func ChannelChatText(request []byte) (string, error) {
	chat, err := ParseChannelChatRequest(request)
	return chat.Text, err
}

func ChannelSystemMessagePayload(text string) ([]byte, error) {
	nameUnits := utf16.Encode([]rune("**"))
	textUnits := utf16.Encode([]rune(text))
	if len(textUnits)*2 > 255 {
		return nil, fmt.Errorf("system chat text exceeds one-byte UTF-16 limit")
	}
	if Client2012 {
		return protocol.NewWriter(6 + len(nameUnits)*2 + len(textUnits)*2).
			U8(0).U8(0).U8(0).
			U8(byte(len(nameUnits) * 2)).UTF16LE("**").
			U8(byte(len(textUnits) * 2)).UTF16LE(text).
			Clone(), nil
	}
	return protocol.NewWriter(11 + len(nameUnits)*2 + len(textUnits)*2).
		U8(0). // common success
		U8(0). // ordinary channel-chat presentation
		U8(0).
		U32(0).
		U8(byte(len(nameUnits) * 2)).UTF16LE("**").
		U8(0). // empty Power User prefix
		U8(byte(len(textUnits) * 2)).UTF16LE(text).
		Clone(), nil
}

func ChangeSheetPayload(request []byte) []byte {
	sheet := byte(0)
	if len(request) > 0 {
		sheet = request[0]
	}
	return protocol.NewWriter(2).U8(0).U8(sheet).Clone()
}

func GamePopupPayload() []byte {
	// A popup kind is followed by counted u32 and UTF-16 collections. No popup
	// is currently advertised.
	return protocol.NewWriter(7).U8(0).U32(0).U8(0).U8(0).Clone()
}
