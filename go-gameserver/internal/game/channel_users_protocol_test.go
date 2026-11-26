package game

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"

	"wtserver-go/internal/transport"
)

func TestChannelUsersPayloadLayout(t *testing.T) {
	user := ChannelUser{
		MemberID: 42, SessionID: 0x1234, Authority: 12, UnresolvedByte: 3, Level: 7,
		UnresolvedUint32: 0x11223344, PrideMarkID: 0x55667788,
		LoginName: "user", Callsign: "P", PrideName: "Clan",
	}
	payload, err := ChannelUsersPayload(3, []ChannelUser{user})
	if err != nil {
		t.Fatal(err)
	}
	if payload[0] != 0 || payload[1] != 3 || payload[2] != 1 || payload[3] != 42 || payload[8] != 7 {
		t.Fatalf("channel header/member prefix = %x", payload)
	}
	if binary.LittleEndian.Uint16(payload[4:6]) != 0x1234 || payload[6] != 12 || payload[7] != 3 {
		t.Fatalf("channel member/session IDs = %x", payload[3:8])
	}
	if binary.LittleEndian.Uint32(payload[9:13]) != 0x11223344 || binary.LittleEndian.Uint32(payload[13:17]) != 0x55667788 {
		t.Fatalf("channel identity fields = %x", payload)
	}
	if payload[17] != 4 || string(payload[18:22]) != "user" || payload[22] != 2 || payload[23] != 'P' || payload[24] != 0 {
		t.Fatalf("channel strings = %x", payload)
	}
	if payload[25] != 8 || string(payload[26:34]) != "C\x00l\x00a\x00n\x00" {
		t.Fatalf("channel pride name = %x", payload)
	}
	enter, err := ChannelEnterPayload(user)
	if err != nil {
		t.Fatal(err)
	}
	if enter[0] != 0 || enter[1] != 42 || binary.LittleEndian.Uint16(enter[2:4]) != 0x1234 {
		t.Fatalf("channel-enter record = %x", enter)
	}
	if exit := ChannelExitPayload(42); len(exit) != 2 || exit[0] != 0 || exit[1] != 42 {
		t.Fatalf("channel-exit record = %x", exit)
	}
}

func TestAdvertAllPayloadHasExpectedLength(t *testing.T) {
	want := make([]byte, 13)
	if got := AdvertAllPayload(); !bytes.Equal(got, want) {
		t.Fatalf("payload = %x, want %x", got, want)
	}
}

func TestAdvertAllBroadcastsCompleteRosterToExistingLobbyUsers(t *testing.T) {
	firstServerNet, firstClientNet := net.Pipe()
	secondServerNet, secondClientNet := net.Pipe()
	defer firstServerNet.Close()
	defer firstClientNet.Close()
	defer secondServerNet.Close()
	defer secondClientNet.Close()
	_ = firstClientNet.SetDeadline(time.Now().Add(3 * time.Second))
	_ = secondClientNet.SetDeadline(time.Now().Add(3 * time.Second))

	loopback := netip.MustParseAddr("127.0.0.1")
	server := NewServer("unused", 40707, 40708, loopback, loopback, &fakeUsers{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	first, err := newSession(transport.NewEncryptedConn(firstServerNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newSession(transport.NewEncryptedConn(secondServerNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	first.setAuthenticated("first", 1, "First", 5)
	second.setAuthenticated("second", 2, "Second", 6)
	server.addSession(first)
	server.addSession(second)
	firstClient := transport.NewEncryptedConn(firstClientNet, nil)
	secondClient := transport.NewEncryptedConn(secondClientNet, nil)

	done := make(chan error, 1)
	go func() {
		done <- server.handleMessage(context.Background(), second, transport.Frame{MessageID: RequestAdvertAll})
	}()
	firstRoster := assertFrameID(t, firstClient, ReplyChannelChars)
	secondRoster := assertFrameID(t, secondClient, ReplyChannelChars)
	assertFrameID(t, secondClient, ReplyAdvertAll)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if firstRoster.Payload[2] != 2 || secondRoster.Payload[2] != 2 {
		t.Fatalf("live channel roster counts = %d/%d", firstRoster.Payload[2], secondRoster.Payload[2])
	}
}
