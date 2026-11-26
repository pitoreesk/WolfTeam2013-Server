package game

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// BuddyServerEndpointPayload carries a zero result byte, IPv4 address, and
// network-byte-order TCP port.
func BuddyServerEndpointPayload(address netip.Addr, port uint16) ([]byte, error) {
	if !address.Is4() {
		return nil, fmt.Errorf("buddy address must be IPv4")
	}
	payload := make([]byte, 7)
	ip := address.As4()
	copy(payload[1:5], ip[:])
	binary.BigEndian.PutUint16(payload[5:7], port)
	return payload, nil
}

func (s *Server) buddyServerEndpointPayload() ([]byte, error) {
	return BuddyServerEndpointPayload(s.advertisedIP, s.buddyPort)
}
