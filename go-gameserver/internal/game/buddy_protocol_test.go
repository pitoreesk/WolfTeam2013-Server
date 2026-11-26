package game

import (
	"bytes"
	"net/netip"
	"testing"
)

func TestBuddyServerEndpointPayload(t *testing.T) {
	payload, err := BuddyServerEndpointPayload(netip.MustParseAddr("127.0.0.1"), 40709)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x00, 0x7f, 0x00, 0x00, 0x01, 0x9f, 0x05}
	if !bytes.Equal(payload, want) {
		t.Fatalf("buddy endpoint payload = %x, want %x", payload, want)
	}
}

func TestBuddyServerEndpointRejectsIPv6(t *testing.T) {
	if _, err := BuddyServerEndpointPayload(netip.MustParseAddr("::1"), 40709); err == nil {
		t.Fatal("expected IPv6 buddy endpoint to fail")
	}
}

func TestBuddyServerEndpointUsesConfiguredAddress(t *testing.T) {
	server := &Server{
		advertisedIP: netip.MustParseAddr("127.0.0.1"),
		buddyPort:    40709,
	}
	payload, err := server.buddyServerEndpointPayload()
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x00, 0x7f, 0x00, 0x00, 0x01, 0x9f, 0x05}
	if !bytes.Equal(payload, want) {
		t.Fatalf("configured buddy endpoint payload = %x, want %x", payload, want)
	}
}
