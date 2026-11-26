package game

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/transport"
)

func TestInviteUserListPayloadLayout(t *testing.T) {
	payload, err := InviteUserListPayload([]InviteUser{{Level: 7, UserID: 0x12345678, Callsign: "Player"}})
	if err != nil {
		t.Fatal(err)
	}
	if payload[0] != 0 || payload[1] != 1 || payload[2] != 7 || binary.LittleEndian.Uint32(payload[3:7]) != 0x12345678 {
		t.Fatalf("invite user prefix = %x", payload)
	}
	callsign, offset, err := readChannelUTF16(payload, 7)
	if err != nil {
		t.Fatal(err)
	}
	if callsign != "Player" || offset != len(payload) {
		t.Fatalf("invite callsign = %q offset=%d payload=%x", callsign, offset, payload)
	}
}

func TestInviteUserListRejectsCallsignThatOverflowsClientRecord(t *testing.T) {
	_, err := InviteUserListPayload([]InviteUser{{Callsign: "12345678901234567"}})
	if err == nil {
		t.Fatal("expected overlong invite callsign to be rejected")
	}
}

func TestInviteUserListReturnsOnlyAuthenticatedChannelPeers(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	harness.server.addSession(harness.session)
	harness.server.addSession(&Session{LoginName: "guest", Callsign: "Guest", UserID: 77, UserLevel: 9, RoomNumber: -1})
	harness.server.addSession(&Session{LoginName: "duplicate", Callsign: "Guest2", UserID: 77, UserLevel: 10, RoomNumber: -1})
	harness.server.addSession(&Session{LoginName: "busy", Callsign: "Busy", UserID: 88, UserLevel: 11, RoomNumber: 4})
	harness.server.addSession(&Session{RoomNumber: -1})

	frame := dispatchAndRead(t, harness, RequestInviteUserList, ReplyInviteUserList, nil)
	if frame.Payload[0] != 0 || frame.Payload[1] != 1 || frame.Payload[2] != 9 || binary.LittleEndian.Uint32(frame.Payload[3:7]) != 77 {
		t.Fatalf("filtered invite list = %x", frame.Payload)
	}
	callsign, offset, err := readChannelUTF16(frame.Payload, 7)
	if err != nil {
		t.Fatal(err)
	}
	if callsign != "Guest" || !allZero(frame.Payload[offset:]) {
		t.Fatalf("invite peer = %q offset=%d payload=%x", callsign, offset, frame.Payload)
	}
}

func TestInviteUserListMessageIDs(t *testing.T) {
	pairs := [][2]uint16{
		{RequestInviteUserList, ReplyInviteUserList},
		{RequestInviteSelect, ReplyInviteSelect},
		{RequestInviteSend, ReplyInviteSend},
	}
	want := [][2]uint16{{0x1522, 0x1523}, {0x1524, 0x1525}, {0x1526, 0x1527}}
	for index, pair := range pairs {
		if pair != want[index] {
			t.Errorf("invite message IDs = %#v, want %#v", pair, want[index])
		}
	}
}

func TestInviteSelectAndSendPayloadLayoutsMatchProtocol(t *testing.T) {
	requestPayload := protocol.NewWriter(64)
	requestPayload.U8(2)
	if err := writeChannelUTF16(requestPayload, "First", 16); err != nil {
		t.Fatal(err)
	}
	if err := writeChannelUTF16(requestPayload, "Second", 16); err != nil {
		t.Fatal(err)
	}
	if err := writeChannelUTF16(requestPayload, "Join my room", 127); err != nil {
		t.Fatal(err)
	}
	requestPayload.U16(12)
	requestPayload.U32(0x10203040)
	requestPayload.U8(3)
	padding := []byte{0xa4, 0x6f, 0x13, 0xdd, 0x72, 0xb8, 0x09, 0xfe, 0x41, 0x35, 0xc7, 0x88}
	request, err := ParseInviteSelectRequest(append(requestPayload.Clone(), padding...))
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Targets) != 2 || request.Targets[0] != "First" || request.Targets[1] != "Second" ||
		request.Message != "Join my room" || request.RoomNumber != 12 || request.RoomToken != 0x10203040 || request.SenderSlot != 3 {
		t.Fatalf("invite-select request = %+v", request)
	}
	invalidRequest := append(requestPayload.Clone(), make([]byte, 16)...)
	if _, err := ParseInviteSelectRequest(invalidRequest); err == nil {
		t.Fatal("invite-select request accepted more than one block of trailing padding")
	}

	selectReply, err := InviteSelectReplyPayload("First")
	if err != nil {
		t.Fatal(err)
	}
	selected, offset, err := readChannelUTF16(selectReply, 1)
	if err != nil || selected != "First" || offset != len(selectReply) || selectReply[0] != 0 {
		t.Fatalf("invite-select reply = %x selected=%q offset=%d err=%v", selectReply, selected, offset, err)
	}

	sendReply, err := InviteSendReplyPayload(InviteNotification{
		SenderCallsign: "Sender", Message: "Join my room", RoomNumber: 12, RoomToken: 0x10203040, SenderSlot: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	sender, offset, err := readChannelUTF16(sendReply, 1)
	if err != nil {
		t.Fatal(err)
	}
	message, offset, err := readChannelUTF16(sendReply, offset)
	if err != nil {
		t.Fatal(err)
	}
	if sender != "Sender" || message != "Join my room" || offset+7 != len(sendReply) ||
		binary.LittleEndian.Uint16(sendReply[offset:offset+2]) != 12 ||
		binary.LittleEndian.Uint32(sendReply[offset+2:offset+6]) != 0x10203040 || sendReply[offset+6] != 3 {
		t.Fatalf("invite-send reply = %x sender=%q message=%q offset=%d", sendReply, sender, message, offset)
	}
	if failure := InviteSendFailurePayload(); len(failure) != 1 || failure[0] != 1 {
		t.Fatalf("invite-send failure = %x", failure)
	}
}

func TestInviteSelectionAcknowledgesSenderAndImmediatelyNotifiesTarget(t *testing.T) {
	senderServerNet, senderClientNet := net.Pipe()
	targetServerNet, targetClientNet := net.Pipe()
	defer senderServerNet.Close()
	defer senderClientNet.Close()
	defer targetServerNet.Close()
	defer targetClientNet.Close()
	_ = senderClientNet.SetDeadline(time.Now().Add(3 * time.Second))
	_ = targetClientNet.SetDeadline(time.Now().Add(3 * time.Second))

	loopback := netip.MustParseAddr("127.0.0.1")
	server := NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	sender, err := newSession(transport.NewEncryptedConn(senderServerNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	target, err := newSession(transport.NewEncryptedConn(targetServerNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	sender.setAuthenticated("sender", 10, "Sender", 7)
	target.setAuthenticated("target", 20, "Target", 8)
	room := newRoom(12, RoomConfig{Capacity: 8})
	if _, added := room.Add(sender); !added {
		t.Fatal("add invitation sender to room")
	}
	server.rooms[room.Number] = room
	server.addSession(sender)
	server.addSession(target)
	senderClient := transport.NewEncryptedConn(senderClientNet, nil)
	targetClient := transport.NewEncryptedConn(targetClientNet, nil)

	selection := protocol.NewWriter(48)
	selection.U8(1)
	if err := writeChannelUTF16(selection, "target", 16); err != nil {
		t.Fatal(err)
	}
	if err := writeChannelUTF16(selection, "Join my room", 127); err != nil {
		t.Fatal(err)
	}
	selection.U16(12)
	selection.U32(0x10203040)
	selection.U8(3)
	done := make(chan error, 1)
	go func() {
		done <- server.handleMessage(context.Background(), sender, transport.Frame{
			MessageID: RequestInviteSelect,
			Payload:   selection.Clone(),
		})
	}()
	selectFrame := assertFrameID(t, senderClient, ReplyInviteSelect)
	selected, _, err := readChannelUTF16(selectFrame.Payload, 1)
	if err != nil || selectFrame.Payload[0] != 0 || selected != "Target" {
		t.Fatalf("routed invite-select = %x selected=%q err=%v", selectFrame.Payload, selected, err)
	}
	sendFrame := assertFrameID(t, targetClient, ReplyInviteSend)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	senderCallsign, offset, err := readChannelUTF16(sendFrame.Payload, 1)
	if err != nil {
		t.Fatal(err)
	}
	message, offset, err := readChannelUTF16(sendFrame.Payload, offset)
	if err != nil {
		t.Fatal(err)
	}
	if sendFrame.Payload[0] != 0 || senderCallsign != "Sender" || message != "Join my room" ||
		binary.LittleEndian.Uint16(sendFrame.Payload[offset:offset+2]) != 12 ||
		binary.LittleEndian.Uint32(sendFrame.Payload[offset+2:offset+6]) != 0x10203040 || sendFrame.Payload[offset+6] != 3 {
		t.Fatalf("routed invite-send = %x sender=%q message=%q", sendFrame.Payload, senderCallsign, message)
	}
}
