package game

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"testing"
)

func TestRelayProbeFrameRoundTrip(t *testing.T) {
	plain := make([]byte, relayProbePlainSize)
	binary.LittleEndian.PutUint16(plain[0:2], relayProbeMessageID)
	binary.LittleEndian.PutUint32(plain[2:6], 0x12345678)
	copy(plain[6:], []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})

	frame, err := encodeRelayProbeFrame(plain, 0x2468)
	if err != nil {
		t.Fatal(err)
	}
	decoded, ok := decodeRelayProbeFrame(frame)
	if !ok || !bytes.Equal(decoded, plain) {
		t.Fatalf("decoded relay probe = %x, %v; want %x", decoded, ok, plain)
	}
	frame[19] ^= 0x80
	if _, ok := decodeRelayProbeFrame(frame); ok {
		t.Fatal("relay probe with corrupt checksum was accepted")
	}
}

func TestRelayProbeEndpointReply(t *testing.T) {
	request := make([]byte, relayProbePlainSize)
	binary.LittleEndian.PutUint16(request[0:2], relayProbeMessageID)
	binary.LittleEndian.PutUint32(request[2:6], 0x89abcdef)

	frame, err := relayProbeEndpointReply(request, netip.MustParseAddr("203.0.113.9"), 43210)
	if err != nil {
		t.Fatal(err)
	}
	plain, ok := decodeRelayProbeFrame(frame)
	if !ok || binary.LittleEndian.Uint16(plain[0:2]) != relayProbeMessageID ||
		binary.LittleEndian.Uint32(plain[2:6]) != 0x89abcdef ||
		!bytes.Equal(plain[6:10], []byte{203, 0, 113, 9}) ||
		binary.LittleEndian.Uint16(plain[10:12]) != 43210 {
		t.Fatalf("relay endpoint reply = %x, decoded=%v", plain, ok)
	}
}
