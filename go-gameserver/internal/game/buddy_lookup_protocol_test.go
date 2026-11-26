package game

import (
	"bytes"
	"testing"
)

func TestGetUserIDProtocol(t *testing.T) {
	request := []byte{12, 'T', 0, 'a', 0, 'r', 0, 'g', 0, 'e', 0, 't', 0}
	callsign, err := ParseGetUserIDRequest(request)
	if err != nil || callsign != "Target" {
		t.Fatalf("parse get-user-id = %q, %v", callsign, err)
	}

	payload, err := GetUserIDPayload("Target", "target_user")
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte{0}, request...), 11)
	want = append(want, []byte("target_user")...)
	if !bytes.Equal(payload, want) {
		t.Fatalf("get-user-id payload = %x, want %x", payload, want)
	}
}

func TestGetUserIDNotFoundPayload(t *testing.T) {
	payload, err := GetUserIDPayload("", "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, []byte{0, 0, 0}) {
		t.Fatalf("not-found payload = %x", payload)
	}
}
