package launcher

import (
	"bytes"
	"testing"
)

func TestChallengeMatchesJava(t *testing.T) {
	want := []byte{0x08, 0x00, 0x00, 0x00, 0x12, 0x13, 0x00, 0x00}
	if got := Challenge(false); !bytes.Equal(got, want) {
		t.Fatalf("challenge = %x, want %x", got, want)
	}
}

func TestStartGameMatchesJava(t *testing.T) {
	want := []byte{0x0c, 0x00, 0x00, 0x00, 0x01, 0x40, 0x00, 0x00, 0x13, 0x00, 0x00, 0x00}
	if got := StartGame(0x13); !bytes.Equal(got, want) {
		t.Fatalf("start game = %x, want %x", got, want)
	}
}

func TestLoginResponses(t *testing.T) {
	responses := Responses(MessageLoginRequest, ProtocolOptions{SendResult10: true})
	if len(responses) != 2 {
		t.Fatalf("got %d responses, want 2", len(responses))
	}
	if messageID, _ := MessageID(responses[1]); messageID != MessageAuthReply {
		t.Fatalf("second response ID = %#x", messageID)
	}
}

func TestChallengePayloadSize(t *testing.T) {
	frame := Challenge(true)
	if len(frame) != 0x2c {
		t.Fatalf("challenge length = %d", len(frame))
	}
	if size, ok := FrameSize(frame); !ok || size != len(frame) {
		t.Fatalf("frame size = %d, valid = %v", size, ok)
	}
}
