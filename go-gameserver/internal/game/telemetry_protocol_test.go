package game

import (
	"bytes"
	"testing"
)

func TestApexClientInfoLayout(t *testing.T) {
	payload := []byte{1, 3, 0, 0, 0, 0xaa, 0xbb, 0xcc}
	info, err := ParseApexClientInfo(payload)
	if err != nil {
		t.Fatal(err)
	}
	if info.Type != 1 || !bytes.Equal(info.Data, []byte{0xaa, 0xbb, 0xcc}) {
		t.Fatalf("Apex client info = %+v", info)
	}
	reply, err := ApexServerInfoPayload(1, []byte{0x10, 0x20})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reply, []byte{0, 1, 2, 0, 0, 0, 0x10, 0x20}) {
		t.Fatalf("Apex server info = %x", reply)
	}
}

func TestApexClientInfoRejectsInvalidLength(t *testing.T) {
	for _, payload := range [][]byte{
		{},
		{1, 1, 0, 0},
		{1, 2, 0, 0, 0, 0xaa},
		{1, 0xff, 0xff, 0xff, 0xff},
	} {
		if _, err := ParseApexClientInfo(payload); err == nil {
			t.Fatalf("accepted malformed Apex payload %x", payload)
		}
	}
}

func TestLegacyAntiCheatACKsAreBodyless(t *testing.T) {
	if len(GameGuardPayload()) != 0 || len(XTrapPayload()) != 0 {
		t.Fatal("legacy anti-cheat ACK gained an unsupported body")
	}
}
