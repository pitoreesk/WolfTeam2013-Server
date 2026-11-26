package game

import (
	"encoding/binary"
	"fmt"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
)

type InviteUser struct {
	Level    byte
	UserID   uint32
	Callsign string
}

type InviteSelectRequest struct {
	Targets    []string
	Message    string
	RoomNumber uint16
	RoomToken  uint32
	SenderSlot byte
}

type InviteNotification struct {
	SenderCallsign string
	Message        string
	RoomNumber     uint16
	RoomToken      uint32
	SenderSlot     byte
}

// InviteUserListPayload carries a byte-sized count followed by grade, user ID,
// and a byte-counted UTF-16LE callsign for each inviteable channel user.
func InviteUserListPayload(users []InviteUser) ([]byte, error) {
	if len(users) > 255 {
		return nil, fmt.Errorf("invite user count exceeds one-byte protocol limit")
	}
	writer := protocol.NewWriter(2 + len(users)*40).U8(0).U8(byte(len(users)))
	for _, user := range users {
		writer.U8(user.Level).U32(user.UserID)
		// Callsigns are limited to sixteen UTF-16 code units.
		if err := writeChannelUTF16(writer, user.Callsign, 16); err != nil {
			return nil, fmt.Errorf("invite callsign %q: %w", user.Callsign, err)
		}
	}
	return writer.Clone(), nil
}

// ParseInviteSelectRequest parses a counted target list, sender message, and
// room metadata. The server supplies the authenticated sender identity.
func ParseInviteSelectRequest(payload []byte) (InviteSelectRequest, error) {
	if len(payload) < 1 {
		return InviteSelectRequest{}, fmt.Errorf("invite-select payload is too short")
	}
	count := int(payload[0])
	if count == 0 {
		return InviteSelectRequest{}, fmt.Errorf("invite-select target list is empty")
	}
	offset := 1
	targets := make([]string, 0, count)
	for range count {
		target, next, err := readChannelUTF16(payload, offset)
		if err != nil {
			return InviteSelectRequest{}, fmt.Errorf("invite-select target: %w", err)
		}
		if err := validateInviteCallsign(target); err != nil {
			return InviteSelectRequest{}, fmt.Errorf("invite-select target: %w", err)
		}
		targets = append(targets, target)
		offset = next
	}
	message, next, err := readChannelUTF16(payload, offset)
	if err != nil {
		return InviteSelectRequest{}, fmt.Errorf("invite-select message: %w", err)
	}
	if err := validateInviteMessage(message); err != nil {
		return InviteSelectRequest{}, fmt.Errorf("invite-select message: %w", err)
	}
	offset = next
	metadataBytes := len(payload) - offset
	if metadataBytes < 7 || metadataBytes > 22 {
		return InviteSelectRequest{}, fmt.Errorf("invite-select room metadata has %d bytes, want 7 plus at most 15 padding bytes", metadataBytes)
	}
	// Broker frames expose complete decrypted 16-byte blocks rather than the
	// request's logical byte length. Bytes after the seven room fields are
	// encryption padding and must not be interpreted.
	return InviteSelectRequest{
		Targets:    targets,
		Message:    message,
		RoomNumber: binary.LittleEndian.Uint16(payload[offset : offset+2]),
		RoomToken:  binary.LittleEndian.Uint32(payload[offset+2 : offset+6]),
		SenderSlot: payload[offset+6],
	}, nil
}

// InviteSelectReplyPayload confirms the selected callsign. The server sends
// the corresponding invitation directly to the selected user.
func InviteSelectReplyPayload(targetCallsign string) ([]byte, error) {
	if err := validateInviteCallsign(targetCallsign); err != nil {
		return nil, err
	}
	writer := protocol.NewWriter(34).U8(0)
	if err := writeChannelUTF16(writer, targetCallsign, 16); err != nil {
		return nil, err
	}
	return writer.Clone(), nil
}

// InviteSendReplyPayload carries the authenticated sender callsign, message,
// and room metadata.
func InviteSendReplyPayload(invite InviteNotification) ([]byte, error) {
	if err := validateInviteCallsign(invite.SenderCallsign); err != nil {
		return nil, fmt.Errorf("invite sender: %w", err)
	}
	if err := validateInviteMessage(invite.Message); err != nil {
		return nil, fmt.Errorf("invite message: %w", err)
	}
	writer := protocol.NewWriter(76).U8(0)
	if err := writeChannelUTF16(writer, invite.SenderCallsign, 16); err != nil {
		return nil, err
	}
	if err := writeChannelUTF16(writer, invite.Message, 127); err != nil {
		return nil, err
	}
	writer.U16(invite.RoomNumber).U32(invite.RoomToken).U8(invite.SenderSlot)
	return writer.Clone(), nil
}

func InviteSendFailurePayload() []byte { return []byte{1} }

func validateInviteCallsign(callsign string) error {
	units := utf16.Encode([]rune(callsign))
	if len(units) == 0 {
		return fmt.Errorf("callsign is empty")
	}
	if len(units) > 16 {
		return fmt.Errorf("callsign exceeds 16 UTF-16 code units")
	}
	return nil
}

func validateInviteMessage(message string) error {
	units := utf16.Encode([]rune(message))
	if len(units) == 0 {
		return fmt.Errorf("message is empty")
	}
	if len(units)*2 > 255 {
		return fmt.Errorf("message exceeds one-byte UTF-16 length")
	}
	return nil
}
