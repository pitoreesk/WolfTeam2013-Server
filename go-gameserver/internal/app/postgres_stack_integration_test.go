package app

import (
	"encoding/binary"
	"net"
	"os"
	"testing"
	"time"

	"wtserver-go/internal/game"
	"wtserver-go/internal/transport"
)

func TestRunningPostgresStackAuthentication(t *testing.T) {
	address := os.Getenv("WT_TEST_GAME_ADDRESS")
	if address == "" {
		t.Skip("WT_TEST_GAME_ADDRESS is not set")
	}
	tests := []struct {
		name       string
		password   string
		wantStatus byte
		wantUserID bool
	}{
		{name: "invalid password", password: "definitely-wrong", wantStatus: 23},
		{name: "empty password", password: "", wantStatus: 23},
		{name: "correct password", password: "admin", wantStatus: 0, wantUserID: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connection, err := net.DialTimeout("tcp", address, 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
			wire := transport.NewEncryptedConn(connection, nil)
			payload := []byte{0, 0, 0, 5, 'a', 'd', 'm', 'i', 'n', byte(len(test.password))}
			payload = append(payload, test.password...)
			if _, err := wire.WriteMessage(game.RequestChannelLogin, payload); err != nil {
				t.Fatal(err)
			}
			reply, err := wire.ReadFrame()
			if err != nil {
				t.Fatal(err)
			}
			if reply.MessageID != game.ReplyChannelLogin || len(reply.Payload) < 7 || reply.Payload[0] != test.wantStatus {
				t.Fatalf("login reply = id %#x payload %x", reply.MessageID, reply.Payload)
			}
			userID := binary.LittleEndian.Uint32(reply.Payload[3:7])
			if test.wantUserID != (userID > 0) {
				t.Fatalf("user ID = %d, want positive = %v", userID, test.wantUserID)
			}
		})
	}
}

func TestRunningPostgresStackInventoryControlPackets(t *testing.T) {
	address := os.Getenv("WT_TEST_GAME_ADDRESS")
	if address == "" {
		t.Skip("WT_TEST_GAME_ADDRESS is not set")
	}
	connection, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	wire := transport.NewEncryptedConn(connection, nil)
	login := []byte{0, 0, 0, 5, 'a', 'd', 'm', 'i', 'n', 5, 'a', 'd', 'm', 'i', 'n'}
	if _, err := wire.WriteMessage(game.RequestChannelLogin, login); err != nil {
		t.Fatal(err)
	}
	if reply, err := wire.ReadFrame(); err != nil || reply.MessageID != game.ReplyChannelLogin || reply.Payload[0] != 0 {
		t.Fatalf("login reply = %+v, %v", reply, err)
	}

	if _, err := wire.WriteMessage(game.RequestDeletedItems, nil); err != nil {
		t.Fatal(err)
	}
	deleted, err := wire.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if deleted.MessageID != game.ReplyDeletedItems || len(deleted.Payload) < 2 || deleted.Payload[0] != 0 || deleted.Payload[1] != 0 {
		t.Fatalf("deleted-item sync reply = id %#x payload %x", deleted.MessageID, deleted.Payload)
	}

	use := make([]byte, 11)
	binary.LittleEndian.PutUint32(use[0:4], 2_000_007_778)
	binary.LittleEndian.PutUint16(use[4:6], 7778)
	if _, err := wire.WriteMessage(game.RequestUseItem, use); err != nil {
		t.Fatal(err)
	}
	used, err := wire.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if used.MessageID != game.ReplyUseItem || len(used.Payload) < 40 || used.Payload[0] != 0 || used.Payload[1] != 1 || binary.LittleEndian.Uint16(used.Payload[29:31]) != 7778 {
		t.Fatalf("unsupported use-item reply = id %#x payload %x", used.MessageID, used.Payload)
	}
}
