package game

import (
	"bytes"
	"encoding/binary"
	"testing"

	"wtserver-go/internal/protocol"
)

func TestChannelChatTextForAutowin(t *testing.T) {
	request := []byte{0, 0, 0, 0, 0, 0, 14, 'a', 0, 'u', 0, 't', 0, 'o', 0, 'w', 0, 'i', 0, 'n', 0}
	text, err := ChannelChatText(request)
	if err != nil {
		t.Fatal(err)
	}
	if text != "autowin" {
		t.Fatalf("chat text = %q", text)
	}
}

func TestChannelChatPayloadLayout(t *testing.T) {
	request := make([]byte, 7)
	text := protocol.NewWriter(8)
	text.UTF16LE("hi")
	request[6] = byte(len(text.Clone()))
	request = append(request, text.Clone()...)
	payload, err := ChannelChatPayload(request, "u", 0, false, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if payload[7] != 2 || payload[8] != 'u' || payload[9] != 0 || payload[10] != 0 || payload[11] != 4 || payload[12] != 'h' || payload[14] != 'i' {
		t.Fatalf("chat payload = %x", payload)
	}
}

func TestChannelChatPayloadCarriesPowerUserColorAndLabel(t *testing.T) {
	request := protocol.NewWriter(11).
		U8(6).U32(0xff443322).U8(0).U8(4).UTF16LE("hi").Clone()
	payload, err := ChannelChatPayload(request, "Admin", 0, true, 0xff112233, "VIP")
	if err != nil {
		t.Fatal(err)
	}
	if payload[1] != 6 || binary.LittleEndian.Uint32(payload[3:7]) != 0xff112233 || payload[7] != 10 {
		t.Fatalf("Power User chat header = %x", payload)
	}
	prefixOffset := 8 + int(payload[7])
	if payload[prefixOffset] != 6 || payload[prefixOffset+1] != 'V' {
		t.Fatalf("Power User chat prefix = %x", payload)
	}
	textOffset := prefixOffset + 1 + int(payload[prefixOffset])
	if payload[textOffset] != 4 || payload[textOffset+1] != 'h' {
		t.Fatalf("Power User chat text = %x", payload)
	}
}

func TestChannelSystemMessagePayload(t *testing.T) {
	payload, err := ChannelSystemMessagePayload("Player is offline.")
	if err != nil {
		t.Fatal(err)
	}
	if payload[0] != 0 || payload[1] != 0 || payload[7] != 4 ||
		string(payload[8:12]) != "*\x00*\x00" {
		t.Fatalf("system chat header = %x", payload)
	}
	textOffset := 13
	if payload[textOffset] != byte(len("Player is offline.")*2) ||
		string(payload[textOffset+1:]) != "P\x00l\x00a\x00y\x00e\x00r\x00 \x00i\x00s\x00 \x00o\x00f\x00f\x00l\x00i\x00n\x00e\x00.\x00" {
		t.Fatalf("system chat text = %x", payload[textOffset:])
	}
}

func TestChangeSheetPayloadHasExpectedLength(t *testing.T) {
	tests := []struct {
		name    string
		request []byte
		want    []byte
	}{
		{name: "selected sheet", request: []byte{4}, want: []byte{0, 4}},
		{name: "missing sheet defaults to zero", want: []byte{0, 0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ChangeSheetPayload(test.request); !bytes.Equal(got, test.want) {
				t.Fatalf("payload = %x, want %x", got, test.want)
			}
		})
	}
}

func TestGamePopupPayloadHasExpectedLength(t *testing.T) {
	if got, want := GamePopupPayload(), []byte{0, 0, 0, 0, 0, 0, 0}; !bytes.Equal(got, want) {
		t.Fatalf("payload = %x, want %x", got, want)
	}
}

// 2013 TR CS_CH_CHAT_ACK (sub_342105D0) treats the byte after the type as the
// sender authority (12/13 = GM styling).
func TestChannelChatPayloadCarriesSenderAuthority(t *testing.T) {
	request := protocol.NewWriter(11).U8(0).U32(0).U8(0).U8(4).UTF16LE("hi").Clone()
	payload, err := ChannelChatPayload(request, "GM", authorityStaffGM, false, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if payload[1] != 0 || payload[2] != authorityStaffGM {
		t.Fatalf("chat authority header = %x", payload)
	}
}
