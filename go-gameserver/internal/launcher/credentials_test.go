package launcher

import (
	"encoding/hex"
	"testing"
)

func TestSHA0KnownVector(t *testing.T) {
	want := "0164b8a914cd2a5e74c4f7ff082c4d97f1edf880"
	got := sha0([]byte("abc"))
	if hex.EncodeToString(got[:]) != want {
		t.Fatalf("SHA-0 = %x, want %s", got, want)
	}
}

func TestDecodeCapturedLauncherCredentials(t *testing.T) {
	frame, err := hex.DecodeString(
		"4600000010132638288851f92cab84374588c999a24e6666" +
			"c483a0b6638e4495a361a87397720885ad3a71214c5bad9a" +
			"22af09cadfc5ce68be1d9acfe3aff9a8de8c9acbbbc1",
	)
	if err != nil {
		t.Fatal(err)
	}
	username, password, err := DecodeCredentials(frame)
	if err != nil {
		t.Fatal(err)
	}
	if username != "admin" || password != "test123" {
		t.Fatalf("credentials = %q:%q, want admin:test123", username, password)
	}
}

func TestDecodeCredentialsRejectsHeaderOnlyLogin(t *testing.T) {
	if _, _, err := DecodeCredentials(header(MessageLoginRequest, 0, HeaderSize)); err == nil {
		t.Fatal("expected malformed login to be rejected")
	}
}
