package buddy

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"wtserver-go/internal/store"
)

type buddyStoreCall struct {
	OwnerID int
	Name    string
	GroupID uint16
}

type fakeBuddyStore struct {
	addCalls    chan buddyStoreCall
	removeCalls chan buddyStoreCall
}

type mutualBuddyStore struct{}

type mailboxBuddyStore struct {
	mu       sync.Mutex
	nextID   int64
	messages []store.BuddyMailboxMessage
	removes  []buddyStoreCall
	queued   chan struct{}
	deleted  chan struct{}
}

func (mutualBuddyStore) BuddyAccountByUsername(_ context.Context, username string) (store.BuddyAccount, bool, error) {
	switch username {
	case "owner":
		return store.BuddyAccount{UserID: 1, Username: "owner", Callsign: "Owner Wolf"}, true, nil
	case "target":
		return store.BuddyAccount{UserID: 2, Username: "target", Callsign: "Target Wolf"}, true, nil
	default:
		return store.BuddyAccount{}, false, nil
	}
}

func (mutualBuddyStore) Buddies(_ context.Context, userID int) ([]store.BuddyRelation, error) {
	switch userID {
	case 1:
		return []store.BuddyRelation{{Username: "target", Callsign: "Target Wolf", GroupID: 0}}, nil
	case 2:
		return []store.BuddyRelation{{Username: "owner", Callsign: "Owner Wolf", GroupID: 0}}, nil
	default:
		return nil, nil
	}
}

func (mutualBuddyStore) AddBuddy(context.Context, int, string, uint16) (store.BuddyRelation, bool, error) {
	return store.BuddyRelation{}, false, nil
}

func (mutualBuddyStore) RemoveBuddy(context.Context, int, string) (bool, error) {
	return false, nil
}

func (fake *mailboxBuddyStore) BuddyAccountByUsername(_ context.Context, username string) (store.BuddyAccount, bool, error) {
	switch username {
	case "owner":
		return store.BuddyAccount{UserID: 1, Username: "owner", Callsign: "Owner Wolf"}, true, nil
	case "target":
		return store.BuddyAccount{UserID: 2, Username: "target", Callsign: "Target Wolf"}, true, nil
	default:
		return store.BuddyAccount{}, false, nil
	}
}

func (*mailboxBuddyStore) Buddies(context.Context, int) ([]store.BuddyRelation, error) {
	return nil, nil
}

func (*mailboxBuddyStore) AddBuddy(context.Context, int, string, uint16) (store.BuddyRelation, bool, error) {
	return store.BuddyRelation{}, false, nil
}

func (fake *mailboxBuddyStore) RemoveBuddy(_ context.Context, ownerID int, username string) (bool, error) {
	fake.mu.Lock()
	fake.removes = append(fake.removes, buddyStoreCall{OwnerID: ownerID, Name: username})
	fake.mu.Unlock()
	return true, nil
}

func (fake *mailboxBuddyStore) QueueBuddyMessage(_ context.Context, senderUserID int, targetUsername string, messageID uint16, body []byte) (int64, bool, error) {
	if senderUserID != 1 || targetUsername != "target" {
		return 0, false, nil
	}
	fake.mu.Lock()
	fake.nextID++
	message := store.BuddyMailboxMessage{
		ID:        fake.nextID,
		Sender:    store.BuddyAccount{UserID: 1, Username: "owner", Callsign: "Owner Wolf"},
		MessageID: messageID,
		Body:      append([]byte(nil), body...),
	}
	fake.messages = append(fake.messages, message)
	fake.mu.Unlock()
	fake.queued <- struct{}{}
	return message.ID, true, nil
}

func (fake *mailboxBuddyStore) PendingBuddyMessages(context.Context, int) ([]store.BuddyMailboxMessage, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]store.BuddyMailboxMessage(nil), fake.messages...), nil
}

func (fake *mailboxBuddyStore) DeleteBuddyMessage(_ context.Context, _ int, messageRowID int64) error {
	fake.mu.Lock()
	for index, message := range fake.messages {
		if message.ID == messageRowID {
			fake.messages = append(fake.messages[:index], fake.messages[index+1:]...)
			break
		}
	}
	fake.mu.Unlock()
	fake.deleted <- struct{}{}
	return nil
}

func (fake *fakeBuddyStore) BuddyAccountByUsername(_ context.Context, username string) (store.BuddyAccount, bool, error) {
	if username != "owner" {
		return store.BuddyAccount{}, false, nil
	}
	return store.BuddyAccount{UserID: 42, Username: "owner", Callsign: "Owner Wolf"}, true, nil
}

func (fake *fakeBuddyStore) Buddies(context.Context, int) ([]store.BuddyRelation, error) {
	return []store.BuddyRelation{{Username: "saved", Callsign: "Saved Wolf", Guild: "Pack", GroupID: 1}}, nil
}

func (fake *fakeBuddyStore) AddBuddy(_ context.Context, ownerID int, username string, groupID uint16) (store.BuddyRelation, bool, error) {
	fake.addCalls <- buddyStoreCall{OwnerID: ownerID, Name: username, GroupID: groupID}
	return store.BuddyRelation{Username: username, Callsign: "Target Wolf", Guild: "Pack", GroupID: groupID}, true, nil
}

func (fake *fakeBuddyStore) RemoveBuddy(_ context.Context, ownerID int, username string) (bool, error) {
	fake.removeCalls <- buddyStoreCall{OwnerID: ownerID, Name: username}
	return true, nil
}

func TestServerAcceptsConnectionsAndStopsWithContext(t *testing.T) {
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	backend := &fakeBuddyStore{
		addCalls:    make(chan buddyStoreCall, 1),
		removeCalls: make(chan buddyStoreCall, 1),
	}
	server := NewServer(address, backend, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go func() { done <- server.Run(ctx) }()

	var connection net.Conn
	deadline := time.Now().Add(3 * time.Second)
	for connection == nil {
		connection, err = net.DialTimeout("tcp4", address, 50*time.Millisecond)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("dial buddy server: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := writeFrame(connection, RequestPrecredential, nil); err != nil {
		t.Fatal(err)
	}
	credential, err := readFrame(connection)
	if err != nil {
		t.Fatal(err)
	}
	if credential.MessageID != ReplyPrecredential || len(credential.Payload) != 8 {
		t.Fatalf("credential reply = %#04x/%x", credential.MessageID, credential.Payload)
	}
	if capabilities := binary.LittleEndian.Uint32(credential.Payload[4:]); capabilities != buddySupportedCapabilities {
		t.Fatalf("credential capabilities = %#08x, want %#08x", capabilities, buddySupportedCapabilities)
	}
	if err := writeFrame(connection, RequestLogin, encryptedBuddyLogin(t, "owner")); err != nil {
		t.Fatal(err)
	}
	buddyList, err := readFrame(connection)
	if err != nil {
		t.Fatal(err)
	}
	if buddyList.MessageID != ReplyBuddyList || len(buddyList.Payload) != 2+buddyRecordSize {
		t.Fatalf("buddy-list reply = %#04x/%x", buddyList.MessageID, buddyList.Payload)
	}
	if count := binary.LittleEndian.Uint16(buddyList.Payload[:2]); count != 1 {
		t.Fatalf("buddy-list count = %d", count)
	}
	login, err := readFrame(connection)
	if err != nil {
		t.Fatal(err)
	}
	if login.MessageID != ReplyLogin || len(login.Payload) != 6 {
		t.Fatalf("login reply = %#04x/%x", login.MessageID, login.Payload)
	}
	addRequest := make([]byte, 54)
	copy(addRequest, "target")
	if err := writeFrame(connection, RequestAddBuddy, addRequest); err != nil {
		t.Fatal(err)
	}
	addReply, err := readFrame(connection)
	if err != nil {
		t.Fatal(err)
	}
	if addReply.MessageID != ReplyAddBuddy || len(addReply.Payload) != 152 || binary.LittleEndian.Uint16(addReply.Payload[150:]) != 0 {
		t.Fatalf("add-buddy reply = %#04x/%x", addReply.MessageID, addReply.Payload)
	}
	if call := <-backend.addCalls; call != (buddyStoreCall{OwnerID: 42, Name: "target", GroupID: 0}) {
		t.Fatalf("add-buddy store call = %+v", call)
	}
	assertPresenceFrame(t, connection, "target", false)
	removeRequest := make([]byte, 20)
	copy(removeRequest, "target")
	if err := writeFrame(connection, RequestRemoveBuddy, removeRequest); err != nil {
		t.Fatal(err)
	}
	removeReply, err := readFrame(connection)
	if err != nil {
		t.Fatal(err)
	}
	if removeReply.MessageID != ReplyRemoveBuddy || len(removeReply.Payload) != 22 {
		t.Fatalf("remove-buddy reply = %#04x/%x", removeReply.MessageID, removeReply.Payload)
	}
	if call := <-backend.removeCalls; call != (buddyStoreCall{OwnerID: 42, Name: "target"}) {
		t.Fatalf("remove-buddy store call = %+v", call)
	}
	if err := writeFrame(connection, RequestGroupList, nil); err != nil {
		t.Fatal(err)
	}
	groups, err := readFrame(connection)
	if err != nil {
		t.Fatal(err)
	}
	if groups.MessageID != ReplyGroupList || len(groups.Payload) != 4+3*buddyGroupRecordSize {
		t.Fatalf("group-list reply = %#04x/%x", groups.MessageID, groups.Payload)
	}
	if count := binary.LittleEndian.Uint16(groups.Payload[2:4]); count != 3 {
		t.Fatalf("group-list count = %d, want 3", count)
	}
	if err := writeFrame(connection, RequestSetExt, make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	ext, err := readFrame(connection)
	if err != nil {
		t.Fatal(err)
	}
	if ext.MessageID != ReplySetExt || len(ext.Payload) != 2 || ext.Payload[0] != 0 || ext.Payload[1] != 0 {
		t.Fatalf("set-ext reply = %#04x/%x", ext.MessageID, ext.Payload)
	}
	if err := writeFrame(connection, RequestGroupAdd, make([]byte, 44)); err != nil {
		t.Fatal(err)
	}
	groupAdded, err := readFrame(connection)
	if err != nil {
		t.Fatal(err)
	}
	if groupAdded.MessageID != ReplyGroupAdd || len(groupAdded.Payload) != 2 || groupAdded.Payload[0] != 0 || groupAdded.Payload[1] != 0 {
		t.Fatalf("group-add reply = %#04x/%x", groupAdded.MessageID, groupAdded.Payload)
	}
	target := make([]byte, 20)
	copy(target, "testuser")
	if err := writeFrame(connection, RequestUserState, target); err != nil {
		t.Fatal(err)
	}
	state, err := readFrame(connection)
	if err != nil {
		t.Fatal(err)
	}
	if state.MessageID != NotifyUserState || len(state.Payload) != 23 || state.Payload[0] != 1 || state.Payload[22] != 0 {
		t.Fatalf("user-state notification = %#04x/%x", state.MessageID, state.Payload)
	}

	cancel()
	_ = connection.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("buddy server did not stop")
	}
}

func TestServerRoutesEncryptedTunnelBetweenAuthenticatedClients(t *testing.T) {
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	server := NewServer(address, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go func() { done <- server.Run(ctx) }()

	source, sourceKey := loginTunnelClient(t, address, "owner")
	target, targetKey := loginTunnelClient(t, address, "target")
	defer source.Close()
	defer target.Close()

	body := []byte{1, 2, 3, 4, 5}
	plaintext := make([]byte, 24+len(body))
	copy(plaintext[:20], "target")
	binary.LittleEndian.PutUint16(plaintext[20:22], 0xc041)
	binary.LittleEndian.PutUint16(plaintext[22:24], uint16(len(body)))
	copy(plaintext[24:], body)
	encrypted, err := encryptTunnelPayload(plaintext, sourceKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(source, RequestTunnel, encrypted); err != nil {
		t.Fatal(err)
	}

	notification, err := readFrame(target)
	if err != nil {
		t.Fatal(err)
	}
	if notification.MessageID != NotifyTunnel {
		t.Fatalf("tunnel notification message ID = 0x%04x", notification.MessageID)
	}
	notificationPlaintext, err := decryptTunnelPayload(notification.Payload, targetKey)
	if err != nil {
		t.Fatal(err)
	}
	if count := binary.LittleEndian.Uint16(notificationPlaintext[:2]); count != 1 {
		t.Fatalf("tunnel notification count = %d", count)
	}
	record := notificationPlaintext[2:]
	sequence := binary.LittleEndian.Uint32(record[:4])
	if sequence == 0 {
		t.Fatal("tunnel notification sequence is zero")
	}
	if sender, err := decodeFixedUsername(record[4:24]); err != nil || sender != "owner" {
		t.Fatalf("tunnel sender = %q, %v", sender, err)
	}
	if messageID := binary.LittleEndian.Uint16(record[68:70]); messageID != 0xc041 {
		t.Fatalf("tunnel P2P message ID = 0x%04x", messageID)
	}
	if size := binary.LittleEndian.Uint16(record[70:72]); size != uint16(len(body)) ||
		!equalBytes(record[72:72+len(body)], body) {
		t.Fatalf("tunnel P2P body = %d/%x", size, record[72:72+len(body)])
	}

	acknowledgement := make([]byte, 6)
	binary.LittleEndian.PutUint16(acknowledgement[:2], 1)
	binary.LittleEndian.PutUint32(acknowledgement[2:], sequence)
	if err := writeFrame(target, AckTunnel, acknowledgement); err != nil {
		t.Fatal(err)
	}

	cancel()
	_ = source.Close()
	_ = target.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("buddy server did not stop")
	}
}

func TestServerRoutesBulkTunnelBetweenAuthenticatedClients(t *testing.T) {
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	server := NewServer(address, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go func() { done <- server.Run(ctx) }()

	source, _ := loginTunnelClient(t, address, "owner")
	target, _ := loginTunnelClient(t, address, "target")
	defer source.Close()
	defer target.Close()

	stateTarget := make([]byte, buddyUsernameBytes)
	copy(stateTarget, "target")
	if err := writeFrame(source, RequestUserState, stateTarget); err != nil {
		t.Fatal(err)
	}
	state, err := readFrame(source)
	if err != nil {
		t.Fatal(err)
	}
	if state.MessageID != NotifyUserState || len(state.Payload) != 35 || state.Payload[22] != 1 ||
		!equalBytes(state.Payload[23:], make([]byte, 12)) {
		t.Fatalf("online user-state notification = 0x%04x/%x", state.MessageID, state.Payload)
	}

	body := []byte{1, 2, 3, 4, 5}
	payload := make([]byte, 5+len(body)+2+buddyUsernameBytes)
	payload[0] = 1
	binary.LittleEndian.PutUint16(payload[1:3], 0xc041)
	binary.LittleEndian.PutUint16(payload[3:5], uint16(len(body)))
	copy(payload[5:], body)
	countOffset := 5 + len(body)
	binary.LittleEndian.PutUint16(payload[countOffset:countOffset+2], 1)
	copy(payload[countOffset+2:], "target")
	if err := writeFrame(source, RequestBulkTunnel, payload); err != nil {
		t.Fatal(err)
	}

	notification, err := readFrame(target)
	if err != nil {
		t.Fatal(err)
	}
	if notification.MessageID != NotifyBulkTunnel {
		t.Fatalf("bulk tunnel notification message ID = 0x%04x", notification.MessageID)
	}
	if sender, err := decodeFixedUsername(notification.Payload[:20]); err != nil || sender != "owner" {
		t.Fatalf("bulk tunnel sender = %q, %v", sender, err)
	}
	if display := decodeFixedUTF16(notification.Payload[20:60]); display != "owner" {
		t.Fatalf("bulk tunnel display name = %q", display)
	}
	if messageID := binary.LittleEndian.Uint16(notification.Payload[60:62]); messageID != 0xc041 {
		t.Fatalf("bulk tunnel P2P message ID = 0x%04x", messageID)
	}
	if size := binary.LittleEndian.Uint16(notification.Payload[62:64]); size != uint16(len(body)) ||
		!equalBytes(notification.Payload[64:], body) {
		t.Fatalf("bulk tunnel P2P body = %d/%x", size, notification.Payload[64:])
	}

	cancel()
	_ = source.Close()
	_ = target.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("buddy server did not stop")
	}
}

func TestServerPushesBuddyPresenceTransitions(t *testing.T) {
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	server := NewServer(address, mutualBuddyStore{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go func() { done <- server.Run(ctx) }()

	owner, _ := loginTunnelClient(t, address, "owner")
	target, _ := loginTunnelClient(t, address, "target")
	defer owner.Close()
	defer target.Close()

	assertPresenceFrame(t, owner, "target", true)
	assertPresenceFrame(t, target, "owner", true)
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	assertPresenceFrame(t, owner, "target", false)

	cancel()
	_ = owner.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("buddy server did not stop")
	}
}

func TestServerQueuesBulkTunnelForOfflineClient(t *testing.T) {
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	backend := &mailboxBuddyStore{queued: make(chan struct{}, 1), deleted: make(chan struct{}, 1)}
	server := NewServer(address, backend, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go func() { done <- server.Run(ctx) }()

	owner, _ := loginTunnelClient(t, address, "owner")
	defer owner.Close()
	body := []byte{0x52, 0x00, 1, 2, 3}
	payload := make([]byte, 5+len(body)+2+buddyUsernameBytes)
	payload[0] = 1
	binary.LittleEndian.PutUint16(payload[1:3], 0xc041)
	binary.LittleEndian.PutUint16(payload[3:5], uint16(len(body)))
	copy(payload[5:], body)
	countOffset := 5 + len(body)
	binary.LittleEndian.PutUint16(payload[countOffset:countOffset+2], 1)
	copy(payload[countOffset+2:], "target")
	if err := writeFrame(owner, RequestBulkTunnel, payload); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.queued:
	case <-time.After(3 * time.Second):
		t.Fatal("offline buddy message was not queued")
	}

	target, _ := loginTunnelClient(t, address, "target")
	defer target.Close()
	if err := writeFrame(target, RequestGroupList, nil); err != nil {
		t.Fatal(err)
	}
	groups, err := readFrame(target)
	if err != nil {
		t.Fatal(err)
	}
	if groups.MessageID != ReplyGroupList {
		t.Fatalf("group-list message ID = 0x%04x", groups.MessageID)
	}
	notification, err := readFrame(target)
	if err != nil {
		t.Fatal(err)
	}
	if notification.MessageID != NotifyBulkTunnel {
		t.Fatalf("pending notification message ID = 0x%04x", notification.MessageID)
	}
	if sender, err := decodeFixedUsername(notification.Payload[:20]); err != nil || sender != "owner" {
		t.Fatalf("pending notification sender = %q, %v", sender, err)
	}
	if messageID := binary.LittleEndian.Uint16(notification.Payload[60:62]); messageID != 0xc041 {
		t.Fatalf("pending notification P2P message ID = 0x%04x", messageID)
	}
	if !equalBytes(notification.Payload[64:], body) {
		t.Fatalf("pending notification body = %x", notification.Payload[64:])
	}
	select {
	case <-backend.deleted:
	case <-time.After(3 * time.Second):
		t.Fatal("delivered buddy message was not removed from mailbox")
	}

	cancel()
	_ = owner.Close()
	_ = target.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("buddy server did not stop")
	}
}

func TestServerQueuesDeleteTunnelForOfflineClient(t *testing.T) {
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	backend := &mailboxBuddyStore{queued: make(chan struct{}, 1), deleted: make(chan struct{}, 1)}
	server := NewServer(address, backend, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go func() { done <- server.Run(ctx) }()

	owner, _ := loginTunnelClient(t, address, "owner")
	defer owner.Close()
	removeRequest := make([]byte, buddyUsernameBytes)
	copy(removeRequest, "target")
	if err := writeFrame(owner, RequestRemoveBuddy, removeRequest); err != nil {
		t.Fatal(err)
	}
	removeReply, err := readFrame(owner)
	if err != nil {
		t.Fatal(err)
	}
	if removeReply.MessageID != ReplyRemoveBuddy || binary.LittleEndian.Uint16(removeReply.Payload[:2]) != 0 {
		t.Fatalf("remove-buddy reply = 0x%04x/%x", removeReply.MessageID, removeReply.Payload)
	}
	select {
	case <-backend.queued:
	case <-time.After(3 * time.Second):
		t.Fatal("offline buddy delete was not queued")
	}
	backend.mu.Lock()
	removes := append([]buddyStoreCall(nil), backend.removes...)
	backend.mu.Unlock()
	if len(removes) != 2 ||
		removes[0] != (buddyStoreCall{OwnerID: 1, Name: "target"}) ||
		removes[1] != (buddyStoreCall{OwnerID: 2, Name: "owner"}) {
		t.Fatalf("buddy remove calls = %+v", removes)
	}

	target, _ := loginTunnelClient(t, address, "target")
	defer target.Close()
	if err := writeFrame(target, RequestGroupList, nil); err != nil {
		t.Fatal(err)
	}
	groups, err := readFrame(target)
	if err != nil {
		t.Fatal(err)
	}
	if groups.MessageID != ReplyGroupList {
		t.Fatalf("group-list message ID = 0x%04x", groups.MessageID)
	}
	notification, err := readFrame(target)
	if err != nil {
		t.Fatal(err)
	}
	if notification.MessageID != NotifyBulkTunnel {
		t.Fatalf("pending delete notification message ID = 0x%04x", notification.MessageID)
	}
	if sender, err := decodeFixedUsername(notification.Payload[:20]); err != nil || sender != "owner" {
		t.Fatalf("pending delete sender = %q, %v", sender, err)
	}
	if messageID := binary.LittleEndian.Uint16(notification.Payload[60:62]); messageID != buddyRemoveP2PMessageID {
		t.Fatalf("pending delete P2P message ID = 0x%04x", messageID)
	}
	if size := binary.LittleEndian.Uint16(notification.Payload[62:64]); size != 0 {
		t.Fatalf("pending delete body size = %d", size)
	}
	select {
	case <-backend.deleted:
	case <-time.After(3 * time.Second):
		t.Fatal("delivered buddy delete was not removed from mailbox")
	}

	cancel()
	_ = owner.Close()
	_ = target.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("buddy server did not stop")
	}
}

func assertPresenceFrame(t *testing.T, connection net.Conn, username string, online bool) {
	t.Helper()
	state, err := readFrame(connection)
	if err != nil {
		t.Fatal(err)
	}
	wantSize := 23
	if online {
		wantSize = 35
	}
	if state.MessageID != NotifyUserState || len(state.Payload) != wantSize || (state.Payload[22] != 0) != online {
		t.Fatalf("presence notification for %q = 0x%04x/%x", username, state.MessageID, state.Payload)
	}
	gotUsername, err := decodeFixedUsername(state.Payload[2:22])
	if err != nil || gotUsername != username {
		t.Fatalf("presence username = %q, %v; want %q", gotUsername, err, username)
	}
}

func loginTunnelClient(t *testing.T, address, username string) (net.Conn, [16]byte) {
	t.Helper()
	var connection net.Conn
	var err error
	deadline := time.Now().Add(3 * time.Second)
	for connection == nil {
		connection, err = net.DialTimeout("tcp4", address, 50*time.Millisecond)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial buddy server: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(connection, RequestPrecredential, nil); err != nil {
		t.Fatal(err)
	}
	credential, err := readFrame(connection)
	if err != nil {
		t.Fatal(err)
	}
	if credential.MessageID != ReplyPrecredential || len(credential.Payload) != 8 {
		t.Fatalf("credential reply = 0x%04x/%x", credential.MessageID, credential.Payload)
	}
	var precredential [4]byte
	copy(precredential[:], credential.Payload[:4])
	if err := writeFrame(connection, RequestLogin, encryptedBuddyLogin(t, username)); err != nil {
		t.Fatal(err)
	}
	list, err := readFrame(connection)
	if err != nil {
		t.Fatal(err)
	}
	if list.MessageID != ReplyBuddyList || len(list.Payload) < 2 ||
		(len(list.Payload)-2)%buddyRecordSize != 0 ||
		int(binary.LittleEndian.Uint16(list.Payload[:2])) != (len(list.Payload)-2)/buddyRecordSize {
		t.Fatalf("buddy-list reply = 0x%04x/%x", list.MessageID, list.Payload)
	}
	login, err := readFrame(connection)
	if err != nil {
		t.Fatal(err)
	}
	if login.MessageID != ReplyLogin || len(login.Payload) != 6 || binary.LittleEndian.Uint16(login.Payload[:2]) != 0 {
		t.Fatalf("login reply = 0x%04x/%x", login.MessageID, login.Payload)
	}
	key, err := tunnelSessionKey(username, precredential)
	if err != nil {
		t.Fatal(err)
	}
	return connection, key
}

func TestBuddyReverseSubscriptionsTrackRelationChanges(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer("unused", nil, logger)
	serverSide, clientSide := net.Pipe()
	defer serverSide.Close()
	defer clientSide.Close()
	session := &buddySession{connection: serverSide, logger: logger}

	server.registerSession(session, store.BuddyAccount{UserID: 1, Username: "Owner"}, [16]byte{}, []store.BuddyRelation{{Username: "Target"}})
	if _, indexed := server.subscribersByUsername["target"][session]; !indexed {
		t.Fatal("initial buddy subscription was not indexed")
	}
	server.addSessionBuddy(session, "Extra")
	if _, indexed := server.subscribersByUsername["extra"][session]; !indexed {
		t.Fatal("added buddy subscription was not indexed")
	}
	server.removeSessionBuddy(session, "TARGET")
	if _, exists := server.subscribersByUsername["target"]; exists {
		t.Fatal("removed buddy subscription remains indexed")
	}
	server.unregisterSession(session)
	if len(server.subscribersByUsername) != 0 {
		t.Fatalf("subscriptions remain after logout: %d", len(server.subscribersByUsername))
	}
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
