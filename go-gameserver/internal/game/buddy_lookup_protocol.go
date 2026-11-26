package game

import (
	"fmt"

	"wtserver-go/internal/protocol"
)

// ParseGetUserIDRequest parses a byte-counted UTF-16LE callsign followed by
// encrypted-frame padding.
func ParseGetUserIDRequest(payload []byte) (string, error) {
	callsign, _, err := readChannelUTF16(payload, 0)
	if err != nil {
		return "", fmt.Errorf("parse get-user-id request: %w", err)
	}
	return callsign, nil
}

// GetUserIDPayload builds a result byte followed by a UTF-16 callsign and an
// ASCII account ID.
func GetUserIDPayload(callsign, username string) ([]byte, error) {
	writer := protocol.NewWriter(3 + len(callsign)*2 + len(username)).U8(0)
	if err := writeChannelUTF16(writer, callsign, 16); err != nil {
		return nil, err
	}
	if err := writeChannelASCII(writer, username, 20); err != nil {
		return nil, err
	}
	return writer.Clone(), nil
}
