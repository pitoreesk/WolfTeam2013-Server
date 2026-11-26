package buddy

import (
	"bytes"
	"crypto/aes"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"io"
	"testing"
	"unicode/utf16"

	"wtserver-go/internal/store"
)

func TestFrameRoundTrip(t *testing.T) {
	var wire bytes.Buffer
	payload := []byte{1, 2, 3, 4}
	if err := writeFrame(&wire, ReplyPrecredential, payload); err != nil {
		t.Fatal(err)
	}
	got, err := readFrame(&wire)
	if err != nil {
		t.Fatal(err)
	}
	if got.MessageID != ReplyPrecredential || !bytes.Equal(got.Payload, payload) {
		t.Fatalf("buddy frame = %#04x/%x", got.MessageID, got.Payload)
	}
}

func TestSetExtReplyPayload(t *testing.T) {
	payload := setExtReplyPayload()
	if len(payload) != 2 || payload[0] != 0 || payload[1] != 0 {
		t.Fatalf("set-ext reply = %x", payload)
	}
}

func TestLoginUsernameDecryptsFixture(t *testing.T) {
	payload := make([]byte, buddyLoginPayloadSize)
	ciphertext, err := hex.DecodeString("345b5a43fe7d5306fdd05b6b5bb7f7cb")
	if err != nil {
		t.Fatal(err)
	}
	copy(payload, ciphertext)
	username, err := loginUsername(payload)
	if err != nil {
		t.Fatal(err)
	}
	if username != "admin" {
		t.Fatalf("login username = %q, want admin", username)
	}
}

func TestTunnelKeyAndPacketFormats(t *testing.T) {
	precredential := [4]byte{0x78, 0x56, 0x34, 0x12}
	key, err := tunnelSessionKey("owner", precredential)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := sha1.Sum(append([]byte("owner"), precredential[:]...))
	wantKey := []byte{
		wantDigest[3], wantDigest[2], wantDigest[1], wantDigest[0],
		wantDigest[7], wantDigest[6], wantDigest[5], wantDigest[4],
		wantDigest[11], wantDigest[10], wantDigest[9], wantDigest[8],
		wantDigest[15], wantDigest[14], wantDigest[13], wantDigest[12],
	}
	if !bytes.Equal(key[:], wantKey) {
		t.Fatalf("tunnel key = %x, want %x", key, wantKey)
	}

	requestPlaintext := make([]byte, 24+5)
	copy(requestPlaintext[:20], "target")
	binary.LittleEndian.PutUint16(requestPlaintext[20:22], 0xc041)
	binary.LittleEndian.PutUint16(requestPlaintext[22:24], 5)
	copy(requestPlaintext[24:], "hello")
	requestPayload, err := encryptTunnelPayload(requestPlaintext, key)
	if err != nil {
		t.Fatal(err)
	}
	request, err := parseTunnelRequest(requestPayload, key)
	if err != nil {
		t.Fatal(err)
	}
	if request.TargetUsername != "target" || request.MessageID != 0xc041 || !bytes.Equal(request.Body, []byte("hello")) {
		t.Fatalf("tunnel request = %+v", request)
	}

	notification, err := tunnelNotificationPayload(0x10203040, store.BuddyAccount{
		Username: "owner", Callsign: "Owner Wolf",
	}, request.MessageID, request.Body, key)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := decryptTunnelPayload(notification, key)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint16(plaintext[:2]) != 1 {
		t.Fatalf("tunnel notification count = %d", binary.LittleEndian.Uint16(plaintext[:2]))
	}
	record := plaintext[2:]
	if binary.LittleEndian.Uint32(record[:4]) != 0x10203040 {
		t.Fatalf("tunnel sequence = 0x%08x", binary.LittleEndian.Uint32(record[:4]))
	}
	if sender, err := decodeFixedUsername(record[4:24]); err != nil || sender != "owner" {
		t.Fatalf("tunnel sender = %q, %v", sender, err)
	}
	if display := decodeFixedUTF16(record[24:64]); display != "Owner Wolf" {
		t.Fatalf("tunnel display name = %q", display)
	}
	if messageID := binary.LittleEndian.Uint16(record[68:70]); messageID != 0xc041 {
		t.Fatalf("tunnel message ID = 0x%04x", messageID)
	}
	if size := binary.LittleEndian.Uint16(record[70:72]); size != 5 || !bytes.Equal(record[72:77], []byte("hello")) {
		t.Fatalf("tunnel body = %d/%x", size, record[72:77])
	}

	acknowledgement := make([]byte, 6)
	binary.LittleEndian.PutUint16(acknowledgement[:2], 1)
	binary.LittleEndian.PutUint32(acknowledgement[2:], 0x10203040)
	sequences, err := parseTunnelAcknowledgement(acknowledgement)
	if err != nil || len(sequences) != 1 || sequences[0] != 0x10203040 {
		t.Fatalf("tunnel acknowledgement = %x, %v", sequences, err)
	}
}

func TestBulkTunnelPacketFormats(t *testing.T) {
	body := []byte{0x52, 0x00, 1, 2, 3}
	requestPayload := make([]byte, 5+len(body)+2+2*buddyUsernameBytes)
	requestPayload[0] = 1
	binary.LittleEndian.PutUint16(requestPayload[1:3], 0xc041)
	binary.LittleEndian.PutUint16(requestPayload[3:5], uint16(len(body)))
	copy(requestPayload[5:], body)
	countOffset := 5 + len(body)
	binary.LittleEndian.PutUint16(requestPayload[countOffset:countOffset+2], 2)
	copy(requestPayload[countOffset+2:countOffset+2+buddyUsernameBytes], "target")
	copy(requestPayload[countOffset+2+buddyUsernameBytes:], "other")

	request, err := parseBulkTunnelRequest(requestPayload)
	if err != nil {
		t.Fatal(err)
	}
	if request.MessageID != 0xc041 || !bytes.Equal(request.Body, body) ||
		len(request.Targets) != 2 || request.Targets[0] != "target" || request.Targets[1] != "other" {
		t.Fatalf("bulk tunnel request = %+v", request)
	}

	notification, err := bulkTunnelNotificationPayload(store.BuddyAccount{
		Username: "owner", Callsign: "Owner Wolf",
	}, request.MessageID, request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(notification) != bulkTunnelHeaderSize+len(body) {
		t.Fatalf("bulk tunnel notification size = %d", len(notification))
	}
	if sender, err := decodeFixedUsername(notification[:20]); err != nil || sender != "owner" {
		t.Fatalf("bulk tunnel sender = %q, %v", sender, err)
	}
	if display := decodeFixedUTF16(notification[20:60]); display != "Owner Wolf" {
		t.Fatalf("bulk tunnel display name = %q", display)
	}
	if messageID := binary.LittleEndian.Uint16(notification[60:62]); messageID != 0xc041 {
		t.Fatalf("bulk tunnel message ID = 0x%04x", messageID)
	}
	if size := binary.LittleEndian.Uint16(notification[62:64]); size != uint16(len(body)) ||
		!bytes.Equal(notification[64:], body) {
		t.Fatalf("bulk tunnel body = %d/%x", size, notification[64:])
	}
}

func TestBuddyListAndMutationPayloads(t *testing.T) {
	relation := store.BuddyRelation{
		Username: "friend_1",
		Callsign: "Friendly Wolf",
		Guild:    "Moon Pack",
		GroupID:  0,
	}
	list, err := buddyListReplyPayload([]store.BuddyRelation{relation})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2+buddyRecordSize || binary.LittleEndian.Uint16(list[:2]) != 1 {
		t.Fatalf("buddy-list header/size = %x/%d", list[:2], len(list))
	}
	record := list[2:]
	if username, err := decodeFixedUsername(record[:20]); err != nil || username != relation.Username {
		t.Fatalf("buddy username = %q, %v", username, err)
	}
	if callsign := decodeFixedUTF16(record[20:60]); callsign != relation.Callsign {
		t.Fatalf("buddy callsign = %q", callsign)
	}
	if guild := decodeFixedUTF16(record[60:100]); guild != relation.Guild {
		t.Fatalf("buddy guild = %q", guild)
	}
	if present := binary.LittleEndian.Uint16(record[116:118]); present != 1 {
		t.Fatalf("buddy group presence = %d", present)
	}
	if groupID := binary.LittleEndian.Uint16(record[118:120]); groupID != relation.GroupID {
		t.Fatalf("buddy group ID = %d", groupID)
	}

	addRequest := make([]byte, 54)
	copy(addRequest, relation.Username)
	binary.LittleEndian.PutUint16(addRequest[52:], relation.GroupID)
	username, groupID, err := parseAddBuddyRequest(addRequest)
	if err != nil || username != relation.Username || groupID != relation.GroupID {
		t.Fatalf("parse add buddy = %q/%d, %v", username, groupID, err)
	}
	addReply, err := addBuddyReplyPayload(0, relation)
	if err != nil {
		t.Fatal(err)
	}
	if len(addReply) != 152 || binary.LittleEndian.Uint16(addReply[150:]) != relation.GroupID {
		t.Fatalf("add-buddy reply size/group = %d/%x", len(addReply), addReply[len(addReply)-2:])
	}

	removeRequest := make([]byte, 20)
	copy(removeRequest, relation.Username)
	username, err = parseRemoveBuddyRequest(removeRequest)
	if err != nil || username != relation.Username {
		t.Fatalf("parse remove buddy = %q, %v", username, err)
	}
	removeReply, err := removeBuddyReplyPayload(0, username)
	if err != nil {
		t.Fatal(err)
	}
	if len(removeReply) != 22 || !bytes.Equal(removeReply[2:10], []byte("friend_1")) {
		t.Fatalf("remove-buddy reply = %x", removeReply)
	}
}

func TestGroupListReplyPayload(t *testing.T) {
	payload, err := groupListReplyPayload(defaultBuddyGroups[:])
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != 4+3*buddyGroupRecordSize {
		t.Fatalf("group-list reply has %d bytes", len(payload))
	}
	if result := binary.LittleEndian.Uint16(payload[:2]); result != 0 {
		t.Fatalf("group-list result = %d", result)
	}
	if count := binary.LittleEndian.Uint16(payload[2:4]); count != 3 {
		t.Fatalf("group-list count = %d", count)
	}

	want := []buddyGroup{
		{ID: 0, Name: "Default", Kind: 0},
		{ID: 1, Name: "Clan", Kind: 1},
		{ID: 2, Name: "Rival", Kind: 2},
	}
	for index, expected := range want {
		offset := 4 + index*buddyGroupRecordSize
		record := payload[offset : offset+buddyGroupRecordSize]
		if id := binary.LittleEndian.Uint16(record[:2]); id != expected.ID {
			t.Errorf("group %d id = %d, want %d", index, id, expected.ID)
		}
		if name := decodeBuddyGroupName(record[2:42]); name != expected.Name {
			t.Errorf("group %d name = %q, want %q", index, name, expected.Name)
		}
		if kind := binary.LittleEndian.Uint16(record[42:44]); kind != expected.Kind {
			t.Errorf("group %d kind = %d, want %d", index, kind, expected.Kind)
		}
	}
}

func TestGroupListReplyPayloadRejectsInvalidNamesAndCounts(t *testing.T) {
	groups := make([]buddyGroup, maxBuddyGroups+1)
	if _, err := groupListReplyPayload(groups); err == nil {
		t.Fatal("expected oversized group list to fail")
	}
	if _, err := groupListReplyPayload([]buddyGroup{{ID: 1, Name: "12345678901234567890"}}); err == nil {
		t.Fatal("expected unterminated group name to fail")
	}
	if _, err := groupListReplyPayload([]buddyGroup{{ID: 1, Name: "bad\x00name"}}); err == nil {
		t.Fatal("expected group name containing NUL to fail")
	}
}

func decodeBuddyGroupName(data []byte) string {
	return decodeFixedUTF16(data)
}

func decodeFixedUTF16(data []byte) string {
	units := make([]uint16, 0, len(data)/2)
	for offset := 0; offset+1 < len(data); offset += 2 {
		unit := binary.LittleEndian.Uint16(data[offset : offset+2])
		if unit == 0 {
			break
		}
		units = append(units, unit)
	}
	return string(utf16.Decode(units))
}

func encryptedBuddyLogin(t *testing.T, username string) []byte {
	t.Helper()
	plaintext := make([]byte, 2*aes.BlockSize)
	if err := encodeFixedUsername(plaintext[:buddyUsernameBytes], username); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(buddyLoginAESKey[:])
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, buddyLoginPayloadSize)
	block.Encrypt(payload[:aes.BlockSize], plaintext[:aes.BlockSize])
	block.Encrypt(payload[aes.BlockSize:2*aes.BlockSize], plaintext[aes.BlockSize:])
	return payload
}

func TestReadFrameRejectsInvalidSize(t *testing.T) {
	_, err := readFrame(bytes.NewReader([]byte{3, 0, 0, 0}))
	if err == nil {
		t.Fatal("expected invalid buddy frame size")
	}
}

func TestReadFrameRequiresCompletePayload(t *testing.T) {
	_, err := readFrame(bytes.NewReader([]byte{8, 0, 0, 0, 1}))
	if err != io.ErrUnexpectedEOF {
		t.Fatalf("incomplete frame error = %v", err)
	}
}

func TestUserStatePayload(t *testing.T) {
	request := make([]byte, 20)
	copy(request, "testuser")
	offline, err := userStatePayload(request, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(offline) != 23 || offline[0] != 1 || offline[1] != 0 || !bytes.Equal(offline[2:22], request) || offline[22] != 0 {
		t.Fatalf("offline user-state payload = %x", offline)
	}

	online, err := userStatePayload(request, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(online) != 35 || online[0] != 1 || online[1] != 0 ||
		!bytes.Equal(online[2:22], request) || online[22] != 1 ||
		!bytes.Equal(online[23:], make([]byte, 12)) {
		t.Fatalf("online user-state payload = %x", online)
	}
}
