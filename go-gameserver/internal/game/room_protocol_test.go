package game

import (
	"bytes"
	"testing"
)

func TestRoomControlRepliesHaveExpectedLength(t *testing.T) {
	tests := []struct {
		name string
		got  []byte
		want []byte
	}{
		{name: "exit room", got: ExitRoomPayload(2), want: []byte{0, 2, 0}},
		{name: "kick", got: KickPayload(), want: []byte{0}},
		{name: "change team", got: ChangeTeamPayload(2, 1, 3), want: []byte{0, 2, 1, 3}},
		{name: "change team failure", got: ChangeTeamFailurePayload(182), want: []byte{182}},
		{name: "change slot", got: ChangeSlotPayload(2, 4), want: []byte{0, 2, 4}},
		{name: "change boss", got: ChangeBossPayload(2), want: []byte{0, 2, 0}},
		{name: "start game", got: StartGamePayload(0), want: []byte{0, 0}},
		{name: "start game failure", got: StartGamePayload(0xab), want: []byte{0xab}},
		{name: "UDP ready", got: ReadyPayload(2, 1), want: []byte{0, 2, 1}},
		{name: "UDP start", got: UDPStartPayload(2), want: []byte{0, 2}},
		{name: "enter game", got: EnterGamePayload(2, 3, 1), want: []byte{0, 2, 3, 1}}, // slot, status, round
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !bytes.Equal(test.got, test.want) {
				t.Fatalf("payload = %x, want %x", test.got, test.want)
			}
		})
	}
}

func TestFieldEntryMarksPowerMasterRoom(t *testing.T) {
	payload, err := FieldCreatePayload(RoomSnapshot{
		Number: 1, PowerMaster: true,
		Config: RoomConfig{Title: "R", Capacity: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	if payload[len(payload)-3] != 1 {
		t.Fatalf("field entry does not mark Power Master: %x", payload)
	}
}
