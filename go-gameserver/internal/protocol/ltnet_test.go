package protocol

import (
	"bytes"
	"errors"
	"testing"
)

func TestLTConnectAcceptKnownBytes(t *testing.T) {
	want := []byte{0xcc, 0xff, 0x00, 0x00, 0x0b}
	if got := LTConnectResponse(true, false); !bytes.Equal(got, want) {
		t.Fatalf("connect response = %x, want %x", got, want)
	}
}

func TestLTBitstreamRoundTrip(t *testing.T) {
	packet := LTPingResponse(0x123456)
	reader := NewBitReader(packet)
	token, err := reader.Bits(32)
	if err != nil || token != uint64(LTToken) {
		t.Fatalf("token = %#x, err = %v", token, err)
	}
	message, err := reader.Bits(LTMessageBits)
	if err != nil || message != LTMessagePingResponse {
		t.Fatalf("message = %d, err = %v", message, err)
	}
	ping, err := reader.Bits(LTPingBits)
	if err != nil || ping != 0x123456 {
		t.Fatalf("ping = %#x, err = %v", ping, err)
	}
}

func TestLTBitstreamCrossesByteBoundaries(t *testing.T) {
	var writer BitWriter
	writer.Bits(5, 3)
	writer.Bits(0x2ab, 10)
	writer.Bool(true)
	writer.U8(0xc7)

	reader := NewBitReader(writer.Clone())
	for _, expected := range []struct {
		bits  int
		value uint64
	}{{3, 5}, {10, 0x2ab}, {1, 1}, {8, 0xc7}} {
		value, err := reader.Bits(expected.bits)
		if err != nil || value != expected.value {
			t.Fatalf("read %d bits = %#x, err=%v; want %#x", expected.bits, value, err, expected.value)
		}
	}
	if _, err := reader.Bits(3); !errors.Is(err, ErrBitstreamExhausted) {
		t.Fatalf("read beyond bitstream = %v", err)
	}
}

func TestLTUnconnectedToken(t *testing.T) {
	if !IsLTUnconnected([]byte{0xcc, 0xff, 0, 0, 1}) {
		t.Fatal("expected token to be recognized")
	}
	if IsLTUnconnected([]byte{0xcc, 0xff, 0}) {
		t.Fatal("short packet must not be recognized")
	}
}
