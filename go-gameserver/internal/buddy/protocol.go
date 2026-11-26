package buddy

import (
	"bytes"
	"crypto/aes"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"unicode/utf16"

	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

const (
	headerSize              = 4
	maxFrameSize            = 0xcb20
	buddyGroupRecordSize    = 44
	buddyGroupNameCodeUnits = 20
	maxBuddyGroups          = 50
	buddyLoginPayloadSize   = 0xd0
	buddyUsernameBytes      = 20
	buddyDisplayCodeUnits   = 20
	buddyExtUserBytes       = 16
	buddyMembershipBytes    = 32
	buddyRecordSize         = buddyUsernameBytes + 2*buddyDisplayCodeUnits + 2*buddyDisplayCodeUnits + buddyExtUserBytes + buddyMembershipBytes
	maxBuddyList            = (maxFrameSize - headerSize - 2) / buddyRecordSize
	tunnelPlainBlockSize    = 12
	tunnelRecordHeaderSize  = 72
	maxTunnelBodySize       = 0xff
	tunnelBlockMarker       = 0x2dbabe65
	bulkTunnelHeaderSize    = buddyUsernameBytes + 2*buddyDisplayCodeUnits + 4
	userStateOfflineSize    = buddyUsernameBytes + 1
	userStateEndpointSize   = 6

	RequestPrecredential uint16 = 0x1000
	ReplyPrecredential   uint16 = 0x1001
	RequestLogin         uint16 = 0x1010
	ReplyLogin           uint16 = 0x1011
	ReplyBuddyList       uint16 = 0x1012
	RequestTunnel        uint16 = 0x2000
	NotifyTunnel         uint16 = 0x2010
	AckTunnel            uint16 = 0x2011
	RequestBulkTunnel    uint16 = 0x2020
	NotifyBulkTunnel     uint16 = 0x2021
	RequestAddBuddy      uint16 = 0x3000
	ReplyAddBuddy        uint16 = 0x3001
	RequestRemoveBuddy   uint16 = 0x3002
	ReplyRemoveBuddy     uint16 = 0x3003
	RequestUserState     uint16 = 0x3010
	RequestSetExt        uint16 = 0x3104
	ReplySetExt          uint16 = 0x3105
	RequestGroupList     uint16 = 0x3150
	ReplyGroupList       uint16 = 0x3151
	RequestGroupAdd      uint16 = 0x3152
	ReplyGroupAdd        uint16 = 0x3153
	NotifyUserState      uint16 = 0x3fff
)

var buddyLoginAESKey = [aes.BlockSize]byte{
	0x2c, 0x45, 0x92, 0x6c, 0xf3, 0x39, 0x66, 0x42,
	0xb6, 0x70, 0xd0, 0x06, 0xa1, 0xfa, 0x81, 0x82,
}

type frame struct {
	MessageID uint16
	Payload   []byte
}

type buddyGroup struct {
	ID   uint16
	Name string
	Kind uint16
}

type tunnelRequest struct {
	TargetUsername string
	MessageID      uint16
	Body           []byte
}

type bulkTunnelRequest struct {
	MessageID uint16
	Body      []byte
	Targets   []string
}

var defaultBuddyGroups = [...]buddyGroup{
	{ID: 0, Name: "Default", Kind: 0},
	{ID: 1, Name: "Clan", Kind: 1},
	{ID: 2, Name: "Rival", Kind: 2},
}

func readFrame(reader io.Reader) (frame, error) {
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(reader, header); err != nil {
		return frame{}, err
	}
	size := int(binary.LittleEndian.Uint16(header[:2]))
	if size < headerSize || size > maxFrameSize {
		return frame{}, fmt.Errorf("buddy frame size %d is outside %d..%d", size, headerSize, maxFrameSize)
	}
	payload := make([]byte, size-headerSize)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return frame{}, err
	}
	return frame{MessageID: binary.LittleEndian.Uint16(header[2:4]), Payload: payload}, nil
}

func writeFrame(writer io.Writer, messageID uint16, payload []byte) error {
	size := headerSize + len(payload)
	if size > maxFrameSize {
		return fmt.Errorf("buddy frame size %d exceeds %d", size, maxFrameSize)
	}
	data := make([]byte, size)
	binary.LittleEndian.PutUint16(data[:2], uint16(size))
	binary.LittleEndian.PutUint16(data[2:4], messageID)
	copy(data[4:], payload)
	return transport.WriteFull(writer, data)
}

func loginReplyPayload(result uint16) []byte {
	// The login reply is a uint16 result followed by four optional UDP
	// registration bytes. Zero is success.
	payload := make([]byte, 6)
	binary.LittleEndian.PutUint16(payload[:2], result)
	return payload
}

func loginUsername(payload []byte) (string, error) {
	// The fixed 20-byte account ID is encrypted with the static login key.
	// Remaining fields carry credential and session material.
	if len(payload) != buddyLoginPayloadSize {
		return "", fmt.Errorf("buddy login payload has %d bytes, want %d", len(payload), buddyLoginPayloadSize)
	}
	block, err := aes.NewCipher(buddyLoginAESKey[:])
	if err != nil {
		return "", fmt.Errorf("initialize buddy login cipher: %w", err)
	}
	plaintext := make([]byte, 2*aes.BlockSize)
	block.Decrypt(plaintext[:aes.BlockSize], payload[:aes.BlockSize])
	block.Decrypt(plaintext[aes.BlockSize:], payload[aes.BlockSize:2*aes.BlockSize])
	return decodeFixedUsername(plaintext[:buddyUsernameBytes])
}

func tunnelSessionKey(username string, precredential [4]byte) ([aes.BlockSize]byte, error) {
	var key [aes.BlockSize]byte
	field := make([]byte, buddyUsernameBytes)
	if err := encodeFixedUsername(field, username); err != nil {
		return key, err
	}
	terminator := bytes.IndexByte(field, 0)
	if terminator < 0 {
		terminator = len(field)
	}
	digestInput := make([]byte, 0, terminator+len(precredential))
	digestInput = append(digestInput, field[:terminator]...)
	// Hash the account ID followed by the four precredential bytes.
	digestInput = append(digestInput, precredential[:]...)
	digest := sha1.Sum(digestInput)
	// Reverse each SHA-1 word to obtain the little-endian session key.
	for offset := 0; offset < aes.BlockSize; offset += 4 {
		key[offset] = digest[offset+3]
		key[offset+1] = digest[offset+2]
		key[offset+2] = digest[offset+1]
		key[offset+3] = digest[offset]
	}
	return key, nil
}

func encryptTunnelPayload(plaintext []byte, key [aes.BlockSize]byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, fmt.Errorf("buddy tunnel plaintext is empty")
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("initialize buddy tunnel cipher: %w", err)
	}
	blockCount := (len(plaintext) + tunnelPlainBlockSize - 1) / tunnelPlainBlockSize
	output := make([]byte, blockCount*aes.BlockSize)
	for index := 0; index < blockCount; index++ {
		framed := make([]byte, aes.BlockSize)
		binary.LittleEndian.PutUint32(framed[:4], tunnelBlockMarker)
		start := index * tunnelPlainBlockSize
		end := min(start+tunnelPlainBlockSize, len(plaintext))
		copy(framed[4:], plaintext[start:end])
		block.Encrypt(output[index*aes.BlockSize:], framed)
	}
	return output, nil
}

func decryptTunnelPayload(ciphertext []byte, key [aes.BlockSize]byte) ([]byte, error) {
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("buddy tunnel ciphertext has %d bytes, want a nonzero multiple of %d", len(ciphertext), aes.BlockSize)
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("initialize buddy tunnel cipher: %w", err)
	}
	plaintext := make([]byte, len(ciphertext)/aes.BlockSize*tunnelPlainBlockSize)
	for index := 0; index < len(ciphertext)/aes.BlockSize; index++ {
		framed := make([]byte, aes.BlockSize)
		block.Decrypt(framed, ciphertext[index*aes.BlockSize:(index+1)*aes.BlockSize])
		if marker := binary.LittleEndian.Uint32(framed[:4]); marker != tunnelBlockMarker {
			return nil, fmt.Errorf("buddy tunnel block %d marker is 0x%08x, want 0x%08x", index, marker, tunnelBlockMarker)
		}
		copy(plaintext[index*tunnelPlainBlockSize:], framed[4:])
	}
	return plaintext, nil
}

func parseTunnelRequest(payload []byte, key [aes.BlockSize]byte) (tunnelRequest, error) {
	plaintext, err := decryptTunnelPayload(payload, key)
	if err != nil {
		return tunnelRequest{}, err
	}
	if len(plaintext) < buddyUsernameBytes+4 {
		return tunnelRequest{}, fmt.Errorf("buddy tunnel plaintext has %d bytes, want at least 24", len(plaintext))
	}
	target, err := decodeFixedUsername(plaintext[:buddyUsernameBytes])
	if err != nil {
		return tunnelRequest{}, fmt.Errorf("decode buddy tunnel target: %w", err)
	}
	bodySize := int(binary.LittleEndian.Uint16(plaintext[22:24]))
	if bodySize > maxTunnelBodySize {
		return tunnelRequest{}, fmt.Errorf("buddy tunnel body has %d bytes, max %d", bodySize, maxTunnelBodySize)
	}
	if bodySize > len(plaintext)-24 {
		return tunnelRequest{}, fmt.Errorf("buddy tunnel body declares %d bytes with %d available", bodySize, len(plaintext)-24)
	}
	body := append([]byte(nil), plaintext[24:24+bodySize]...)
	return tunnelRequest{TargetUsername: target, MessageID: binary.LittleEndian.Uint16(plaintext[20:22]), Body: body}, nil
}

func tunnelNotificationPayload(sequence uint32, sender store.BuddyAccount, messageID uint16, body []byte, key [aes.BlockSize]byte) ([]byte, error) {
	if len(body) > maxTunnelBodySize {
		return nil, fmt.Errorf("buddy tunnel body has %d bytes, max %d", len(body), maxTunnelBodySize)
	}
	plaintext := make([]byte, 2+tunnelRecordHeaderSize+len(body))
	binary.LittleEndian.PutUint16(plaintext[:2], 1)
	record := plaintext[2:]
	binary.LittleEndian.PutUint32(record[:4], sequence)
	if err := encodeFixedUsername(record[4:24], sender.Username); err != nil {
		return nil, fmt.Errorf("encode buddy tunnel sender: %w", err)
	}
	displayName := sender.Callsign
	if displayName == "" {
		displayName = sender.Username
	}
	if err := encodeFixedUTF16(record[24:64], displayName); err != nil {
		return nil, fmt.Errorf("encode buddy tunnel display name: %w", err)
	}
	// record[64:68] is the optional direct-peer registration value. Login
	// advertises zero, so routed server-tunnel notifications use zero too.
	binary.LittleEndian.PutUint16(record[68:70], messageID)
	binary.LittleEndian.PutUint16(record[70:72], uint16(len(body)))
	copy(record[72:], body)
	return encryptTunnelPayload(plaintext, key)
}

func parseTunnelAcknowledgement(payload []byte) ([]uint32, error) {
	if len(payload) < 2 {
		return nil, fmt.Errorf("buddy tunnel acknowledgement has %d bytes, want at least 2", len(payload))
	}
	count := int(binary.LittleEndian.Uint16(payload[:2]))
	if len(payload) != 2+4*count {
		return nil, fmt.Errorf("buddy tunnel acknowledgement has %d bytes for %d sequences", len(payload), count)
	}
	sequences := make([]uint32, count)
	for index := range sequences {
		sequences[index] = binary.LittleEndian.Uint32(payload[2+4*index:])
	}
	return sequences, nil
}

func parseBulkTunnelRequest(payload []byte) (bulkTunnelRequest, error) {
	// The cleartext bulk envelope selects server delivery, followed by a
	// message ID, length-prefixed body, and fixed 20-byte account IDs.
	if len(payload) < 7 {
		return bulkTunnelRequest{}, fmt.Errorf("buddy bulk tunnel payload has %d bytes, want at least 7", len(payload))
	}
	bodySize := int(binary.LittleEndian.Uint16(payload[3:5]))
	if bodySize > maxFrameSize-headerSize-bulkTunnelHeaderSize {
		return bulkTunnelRequest{}, fmt.Errorf("buddy bulk tunnel body has %d bytes, max %d",
			bodySize, maxFrameSize-headerSize-bulkTunnelHeaderSize)
	}
	countOffset := 5 + bodySize
	if countOffset+2 > len(payload) {
		return bulkTunnelRequest{}, fmt.Errorf("buddy bulk tunnel body declares %d bytes with %d available", bodySize, len(payload)-5)
	}
	targetCount := int(binary.LittleEndian.Uint16(payload[countOffset : countOffset+2]))
	if targetCount == 0 {
		return bulkTunnelRequest{}, fmt.Errorf("buddy bulk tunnel target list is empty")
	}
	wantSize := countOffset + 2 + targetCount*buddyUsernameBytes
	if wantSize != len(payload) {
		return bulkTunnelRequest{}, fmt.Errorf("buddy bulk tunnel payload has %d bytes for %d targets, want %d",
			len(payload), targetCount, wantSize)
	}
	targets := make([]string, targetCount)
	for index := range targets {
		offset := countOffset + 2 + index*buddyUsernameBytes
		target, err := decodeFixedUsername(payload[offset : offset+buddyUsernameBytes])
		if err != nil {
			return bulkTunnelRequest{}, fmt.Errorf("decode buddy bulk tunnel target %d: %w", index, err)
		}
		targets[index] = target
	}
	return bulkTunnelRequest{
		MessageID: binary.LittleEndian.Uint16(payload[1:3]),
		Body:      append([]byte(nil), payload[5:countOffset]...),
		Targets:   targets,
	}, nil
}

func bulkTunnelNotificationPayload(sender store.BuddyAccount, messageID uint16, body []byte) ([]byte, error) {
	if len(body) > maxFrameSize-headerSize-bulkTunnelHeaderSize {
		return nil, fmt.Errorf("buddy bulk tunnel body has %d bytes, max %d",
			len(body), maxFrameSize-headerSize-bulkTunnelHeaderSize)
	}
	payload := make([]byte, bulkTunnelHeaderSize+len(body))
	if err := encodeFixedUsername(payload[:buddyUsernameBytes], sender.Username); err != nil {
		return nil, fmt.Errorf("encode buddy bulk tunnel sender: %w", err)
	}
	displayName := sender.Callsign
	if displayName == "" {
		displayName = sender.Username
	}
	if err := encodeFixedUTF16(payload[20:60], displayName); err != nil {
		return nil, fmt.Errorf("encode buddy bulk tunnel display name: %w", err)
	}
	binary.LittleEndian.PutUint16(payload[60:62], messageID)
	binary.LittleEndian.PutUint16(payload[62:64], uint16(len(body)))
	copy(payload[64:], body)
	return payload, nil
}

func buddyListReplyPayload(relations []store.BuddyRelation) ([]byte, error) {
	if len(relations) > maxBuddyList {
		return nil, fmt.Errorf("buddy list has %d entries, max %d", len(relations), maxBuddyList)
	}
	payload := make([]byte, 2+len(relations)*buddyRecordSize)
	binary.LittleEndian.PutUint16(payload[:2], uint16(len(relations)))
	for index, relation := range relations {
		record, err := buddyRecordPayload(relation)
		if err != nil {
			return nil, fmt.Errorf("encode buddy %d: %w", index, err)
		}
		copy(payload[2+index*buddyRecordSize:], record)
	}
	return payload, nil
}

func buddyRecordPayload(relation store.BuddyRelation) ([]byte, error) {
	record := make([]byte, buddyRecordSize)
	if err := encodeFixedUsername(record[:buddyUsernameBytes], relation.Username); err != nil {
		return nil, err
	}
	if err := encodeFixedUTF16(record[20:60], relation.Callsign); err != nil {
		return nil, fmt.Errorf("encode callsign: %w", err)
	}
	if err := encodeFixedUTF16(record[60:100], relation.Guild); err != nil {
		return nil, fmt.Errorf("encode guild: %w", err)
	}
	// Eight {present, group ID} pairs occupy the final 32 bytes. The first pair
	// carries local group membership; group zero is the default group.
	binary.LittleEndian.PutUint16(record[116:118], 1)
	binary.LittleEndian.PutUint16(record[118:120], relation.GroupID)
	return record, nil
}

func parseAddBuddyRequest(payload []byte) (string, uint16, error) {
	if len(payload) != buddyUsernameBytes+buddyMembershipBytes+2 {
		return "", 0, fmt.Errorf("buddy add payload has %d bytes, want 54", len(payload))
	}
	username, err := decodeFixedUsername(payload[:buddyUsernameBytes])
	if err != nil {
		return "", 0, err
	}
	groupID := binary.LittleEndian.Uint16(payload[buddyUsernameBytes+buddyMembershipBytes:])
	return username, groupID, nil
}

func parseRemoveBuddyRequest(payload []byte) (string, error) {
	if len(payload) != buddyUsernameBytes {
		return "", fmt.Errorf("buddy remove payload has %d bytes, want %d", len(payload), buddyUsernameBytes)
	}
	return decodeFixedUsername(payload)
}

func addBuddyReplyPayload(result uint16, relation store.BuddyRelation) ([]byte, error) {
	if result != 0 {
		payload := make([]byte, 2)
		binary.LittleEndian.PutUint16(payload, result)
		return payload, nil
	}
	record, err := buddyRecordPayload(relation)
	if err != nil {
		return nil, err
	}
	payload := make([]byte, 2+buddyRecordSize+2)
	copy(payload[2:], record)
	binary.LittleEndian.PutUint16(payload[2+buddyRecordSize:], relation.GroupID)
	return payload, nil
}

func removeBuddyReplyPayload(result uint16, username string) ([]byte, error) {
	if result != 0 {
		payload := make([]byte, 2)
		binary.LittleEndian.PutUint16(payload, result)
		return payload, nil
	}
	payload := make([]byte, 2+buddyUsernameBytes)
	if err := encodeFixedUsername(payload[2:], username); err != nil {
		return nil, err
	}
	return payload, nil
}

func decodeFixedUsername(field []byte) (string, error) {
	if len(field) != buddyUsernameBytes {
		return "", fmt.Errorf("buddy username field has %d bytes, want %d", len(field), buddyUsernameBytes)
	}
	if terminator := bytes.IndexByte(field, 0); terminator >= 0 {
		field = field[:terminator]
	}
	if len(field) == 0 {
		return "", fmt.Errorf("buddy username is empty")
	}
	for _, value := range field {
		if !((value >= 'A' && value <= 'Z') || (value >= 'a' && value <= 'z') ||
			(value >= '0' && value <= '9') || value == '_') {
			return "", fmt.Errorf("buddy username contains invalid byte 0x%02x", value)
		}
	}
	return string(field), nil
}

func encodeFixedUsername(field []byte, username string) error {
	if len(field) != buddyUsernameBytes {
		return fmt.Errorf("buddy username field has %d bytes, want %d", len(field), buddyUsernameBytes)
	}
	encoded := []byte(username)
	if len(encoded) == 0 || len(encoded) > len(field) {
		return fmt.Errorf("buddy username uses %d bytes, want 1..%d", len(encoded), len(field))
	}
	for _, value := range encoded {
		if !((value >= 'A' && value <= 'Z') || (value >= 'a' && value <= 'z') ||
			(value >= '0' && value <= '9') || value == '_') {
			return fmt.Errorf("buddy username contains invalid byte 0x%02x", value)
		}
	}
	copy(field, encoded)
	return nil
}

func encodeFixedUTF16(field []byte, value string) error {
	if len(field) != 2*buddyDisplayCodeUnits {
		return fmt.Errorf("buddy display field has %d bytes, want %d", len(field), 2*buddyDisplayCodeUnits)
	}
	units := make([]uint16, 0, buddyDisplayCodeUnits-1)
	for _, character := range value {
		if character == 0 {
			return fmt.Errorf("value contains NUL")
		}
		encoded := utf16.Encode([]rune{character})
		if len(units)+len(encoded) >= buddyDisplayCodeUnits {
			break
		}
		units = append(units, encoded...)
	}
	for index, unit := range units {
		binary.LittleEndian.PutUint16(field[index*2:], unit)
	}
	return nil
}

func groupListReplyPayload(groups []buddyGroup) ([]byte, error) {
	// The group-list reply starts with a uint16 result and count followed by
	// fixed 44-byte records: id, WCHAR name[20], and group kind.
	if len(groups) > maxBuddyGroups {
		return nil, fmt.Errorf("buddy group list has %d groups, max %d", len(groups), maxBuddyGroups)
	}
	payload := make([]byte, 4+len(groups)*buddyGroupRecordSize)
	binary.LittleEndian.PutUint16(payload[2:4], uint16(len(groups)))
	for index, group := range groups {
		name := utf16.Encode([]rune(group.Name))
		// Group names are NUL-terminated within the fixed-width WCHAR field.
		if len(name) >= buddyGroupNameCodeUnits {
			return nil, fmt.Errorf("buddy group %d name uses %d UTF-16 code units, max %d",
				group.ID, len(name), buddyGroupNameCodeUnits-1)
		}
		offset := 4 + index*buddyGroupRecordSize
		binary.LittleEndian.PutUint16(payload[offset:offset+2], group.ID)
		for unitIndex, unit := range name {
			if unit == 0 {
				return nil, fmt.Errorf("buddy group %d name contains NUL", group.ID)
			}
			binary.LittleEndian.PutUint16(payload[offset+2+unitIndex*2:], unit)
		}
		binary.LittleEndian.PutUint16(payload[offset+42:offset+44], group.Kind)
	}
	return payload, nil
}

func setExtReplyPayload() []byte {
	// The reply is a uint16 result; zero means success.
	return make([]byte, 2)
}

func groupAddReplyPayload() []byte {
	// The reply is a uint16 result; zero means success.
	return make([]byte, 2)
}

func userStatePayload(request []byte, online bool) ([]byte, error) {
	// The request carries one fixed 20-byte account ID. The reply starts with a
	// uint16 count and then ID[20], uint8 online. Online records append
	// two {IPv4 uint32, port uint16} endpoints. Zero endpoints truthfully expose
	// server presence without enabling direct peer transport.
	if len(request) != 20 {
		return nil, fmt.Errorf("buddy user-state request has %d bytes, want 20", len(request))
	}
	recordSize := userStateOfflineSize
	if online {
		recordSize += 2 * userStateEndpointSize
	}
	payload := make([]byte, 2+recordSize)
	binary.LittleEndian.PutUint16(payload[:2], 1)
	copy(payload[2:22], request)
	if online {
		payload[22] = 1
	}
	return payload, nil
}
