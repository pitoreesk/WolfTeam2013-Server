package game

import (
	"encoding/binary"
	"fmt"

	"wtserver-go/internal/protocol"
)

// The telemetry envelope leaves 8179 bytes for provider data.
const maximumApexPayload = 8179

type ApexClientInfo struct {
	Type byte
	Data []byte
}

func ParseApexClientInfo(payload []byte) (ApexClientInfo, error) {
	if len(payload) < 5 {
		return ApexClientInfo{}, fmt.Errorf("Apex client-info payload is too short")
	}
	size := binary.LittleEndian.Uint32(payload[1:5])
	if size > maximumApexPayload || uint64(size)+5 > uint64(len(payload)) {
		return ApexClientInfo{}, fmt.Errorf("invalid Apex client-info length %d", size)
	}
	return ApexClientInfo{Type: payload[0], Data: append([]byte(nil), payload[5:5+int(size)]...)}, nil
}

// ApexServerInfoPayload uses the same type/length/blob envelope as the request.
// It does not echo untrusted request bytes into the provider SDK.
func ApexServerInfoPayload(messageType byte, data []byte) ([]byte, error) {
	if len(data) > maximumApexPayload {
		return nil, fmt.Errorf("Apex server-info payload exceeds %d bytes", maximumApexPayload)
	}
	// The common ACK layer consumes the leading success byte.
	writer := protocol.NewWriter(6 + len(data)).U8(0).U8(messageType).U32(uint32(len(data))).Bytes(data)
	return writer.Clone(), nil
}

func GameGuardPayload() []byte { return nil }

func XTrapPayload() []byte { return nil }
