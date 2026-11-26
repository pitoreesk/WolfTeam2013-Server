package crypto

import (
	"bytes"
	"testing"
)

func TestBrokerPrefixMaskIsReversible(t *testing.T) {
	want := [8]byte{0x47, 0x02, 0x12, 0x02, 0x00, 0x0a, 0x00, 0x67}
	got := want
	maskBrokerPrefix(&got)
	unmaskBrokerPrefix(&got)
	if got != want {
		t.Fatalf("unmasked prefix = %x, want %x", got, want)
	}
}

func TestBrokerBodyTransformationRoundTrip(t *testing.T) {
	prefix := [8]byte{0x47, 0x02, 0x12, 0x02, 0x00, 0x0a, 0x01, 0x68}
	body := []byte("0123456789abcdef")
	wire, err := transformBrokerBody(body, prefix, false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(wire, body) {
		t.Fatal("body was not transformed")
	}
	got, err := transformBrokerBody(wire, prefix, true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("decoded body = %x, want %x", got, body)
	}
}
