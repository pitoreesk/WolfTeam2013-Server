package game

import (
	"bytes"
	"testing"
	"time"
)

func TestGMCommandOpcodes(t *testing.T) {
	if RequestGMCommand != 0x134a || ReplyGMCommand != 0x134b || ReplyMuteBan != 0x133b {
		t.Fatalf("GM command opcodes = %04x/%04x, mute=%04x",
			RequestGMCommand, ReplyGMCommand, ReplyMuteBan)
	}
}

func TestParseGMCommandRequest(t *testing.T) {
	payload := []byte{
		byte(GMCommandBan),
		6, 'B', 0, 'o', 0, 'b', 0,
		14, '7', 0, 'd', 0, ' ', 0, 't', 0, 'e', 0, 's', 0, 't', 0,
	}
	request, err := ParseGMCommandRequest(payload)
	if err != nil {
		t.Fatal(err)
	}
	if request.Command != GMCommandBan || request.Target != "Bob" ||
		request.Duration != 7*24*time.Hour || request.Permanent || request.Reason != "test" {
		t.Fatalf("GM command = %+v", request)
	}
	request, err = ParseGMCommandRequest([]byte{byte(GMCommandSilenceOn)})
	if err != nil || request.Command != GMCommandSilenceOn {
		t.Fatalf("one-byte silence command = %+v, %v", request, err)
	}
}

func TestParseGMChatCommands(t *testing.T) {
	tests := []struct {
		text      string
		command   GMCommand
		target    string
		duration  time.Duration
		permanent bool
		reason    string
	}{
		{text: "/silence on", command: GMCommandSilenceOn},
		{text: "/SILENCE OFF", command: GMCommandSilenceOff},
		{text: "/muteban Player 30m spam", command: GMCommandMuteBan, target: "Player", duration: 30 * time.Minute, reason: "spam"},
		{text: "/kick Player", command: GMCommandKick, target: "Player"},
		{text: "/ban Player 7d repeated abuse", command: GMCommandBan, target: "Player", duration: 7 * 24 * time.Hour, reason: "repeated abuse"},
		{text: "/ban Player permanent cheating", command: GMCommandBan, target: "Player", permanent: true, reason: "cheating"},
		{text: "/search Player", command: GMCommandSearch, target: "Player"},
	}
	for _, test := range tests {
		request, ok, err := ParseGMChatCommand(test.text)
		if !ok {
			t.Fatalf("%q was not recognized", test.text)
		}
		if err != nil {
			t.Fatalf("%q failed: %v", test.text, err)
		}
		if request.Command != test.command || request.Target != test.target ||
			request.Duration != test.duration || request.Permanent != test.permanent ||
			request.Reason != test.reason {
			t.Errorf("%q = %+v", test.text, request)
		}
	}
	if _, ok, _ := ParseGMChatCommand("hello /kick Player"); ok {
		t.Fatal("ordinary chat was recognized as a GM command")
	}
	if request, ok, err := ParseGMChatCommand("/kick"); !ok || err == nil ||
		request.Command != GMCommandKick || request.Target != "" {
		t.Fatalf("argument-free kick command = %+v, %v, %v", request, ok, err)
	}
	if _, ok, err := ParseGMChatCommand("/muteban Player reason"); !ok || err == nil {
		t.Fatalf("duration-free muteban = recognized %v, error %v", ok, err)
	}
}

func TestGMCommandAckPayload(t *testing.T) {
	payload, err := GMCommandAckPayload(GMCommandKick, "reason", "Player")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0, 4,
		12, 'r', 0, 'e', 0, 'a', 0, 's', 0, 'o', 0, 'n', 0,
		12, 'P', 0, 'l', 0, 'a', 0, 'y', 0, 'e', 0, 'r', 0,
	}
	if !bytes.Equal(payload, want) {
		t.Fatalf("GM ACK payload = %x, want %x", payload, want)
	}
}

func TestMuteBanPayloadCarriesClientInterval(t *testing.T) {
	startedAt := time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)
	expiresAt := startedAt.Add(30 * time.Minute)
	payload, err := MuteBanPayload(true, startedAt, expiresAt, "spam")
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte{0, 1, 13}, []byte("202607291300")...)
	want = append(want, 0, 13)
	want = append(want, []byte("202607291330")...)
	want = append(want, 0)
	want = append(want, 8, 's', 0, 'p', 0, 'a', 0, 'm', 0)
	if !bytes.Equal(payload, want) {
		t.Fatalf("mute-ban payload = %x, want %x", payload, want)
	}
}
