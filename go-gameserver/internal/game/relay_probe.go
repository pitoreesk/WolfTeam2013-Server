package game

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/netip"
)

const (
	relayProbeMessageID    = 0x1902
	relayProbePlainSize    = 16
	relayProbeFrameSize    = 20
	relayProbeChecksumSeed = 0x3039
)

// decodeRelayProbeFrame unwraps a relay-discovery datagram containing a nonce,
// eight cumulative little-endian words, and a checksum.
func decodeRelayProbeFrame(frame []byte) ([]byte, bool) {
	if len(frame) != relayProbeFrameSize {
		return nil, false
	}
	words := [10]uint16{}
	for index := range words {
		words[index] = binary.LittleEndian.Uint16(frame[index*2 : index*2+2])
	}
	checksum := uint32(relayProbeChecksumSeed)
	for index := 0; index < 9; index++ {
		checksum += uint32(words[index])
	}
	if uint16(checksum) != words[9] {
		return nil, false
	}
	plain := make([]byte, relayProbePlainSize)
	for index := 0; index < 8; index++ {
		binary.LittleEndian.PutUint16(plain[index*2:index*2+2], words[index+1]-words[index])
	}
	return plain, true
}

func encodeRelayProbeFrame(plain []byte, nonce uint16) ([]byte, error) {
	if len(plain) != relayProbePlainSize {
		return nil, fmt.Errorf("relay probe plaintext must be %d bytes", relayProbePlainSize)
	}
	frame := make([]byte, relayProbeFrameSize)
	previous := nonce
	binary.LittleEndian.PutUint16(frame[0:2], previous)
	checksum := uint32(relayProbeChecksumSeed) + uint32(previous)
	for index := 0; index < 8; index++ {
		previous += binary.LittleEndian.Uint16(plain[index*2 : index*2+2])
		binary.LittleEndian.PutUint16(frame[(index+1)*2:(index+2)*2], previous)
		checksum += uint32(previous)
	}
	binary.LittleEndian.PutUint16(frame[18:20], uint16(checksum))
	return frame, nil
}

func relayProbeEndpointReply(request []byte, observedIP netip.Addr, observedPort uint16) ([]byte, error) {
	if len(request) != relayProbePlainSize || binary.LittleEndian.Uint16(request[0:2]) != relayProbeMessageID {
		return nil, fmt.Errorf("invalid relay discovery request")
	}
	if !observedIP.Is4() || observedPort == 0 {
		return nil, fmt.Errorf("relay discovery requires an IPv4 endpoint")
	}
	plain := make([]byte, relayProbePlainSize)
	if _, err := rand.Read(plain); err != nil {
		return nil, fmt.Errorf("randomize relay discovery reply: %w", err)
	}
	binary.LittleEndian.PutUint16(plain[0:2], relayProbeMessageID)
	copy(plain[2:6], request[2:6])
	ip := observedIP.As4()
	copy(plain[6:10], ip[:])
	binary.LittleEndian.PutUint16(plain[10:12], observedPort)
	var nonceBytes [2]byte
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		return nil, fmt.Errorf("randomize relay discovery nonce: %w", err)
	}
	return encodeRelayProbeFrame(plain, binary.LittleEndian.Uint16(nonceBytes[:]))
}
