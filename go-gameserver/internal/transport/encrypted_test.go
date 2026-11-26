package transport

import (
	"bytes"
	"net"
	"testing"
)

func TestEncryptedConnectionExchange(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	defer serverNet.Close()
	defer clientNet.Close()
	server := NewEncryptedConn(serverNet, nil)
	client := NewEncryptedConn(clientNet, nil)
	server.random = bytes.NewReader([]byte{1, 2, 3, 4})
	client.random = bytes.NewReader([]byte{5, 6, 7, 8})

	writeDone := make(chan error, 1)
	go func() {
		_, err := client.WriteMessage(0x1300, []byte{0xaa, 0xbb})
		writeDone <- err
	}()
	frame, err := server.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if frame.MessageID != 0x1300 || frame.Sequence != 0 || !bytes.Equal(frame.Payload[:2], []byte{0xaa, 0xbb}) {
		t.Fatalf("unexpected frame: %+v payload=%x", frame, frame.Payload)
	}

	go func() {
		_, err := server.WriteMessage(0x1301, []byte{0})
		writeDone <- err
	}()
	reply, err := client.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if reply.MessageID != 0x1301 || reply.Sequence != 0 || len(reply.Payload) != 16 {
		t.Fatalf("unexpected reply: %+v", reply)
	}
}

func TestEncryptedConnectionWritesMultipleFramesInOneCall(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	counted := &writeCountingConn{Conn: serverNet}
	defer serverNet.Close()
	defer clientNet.Close()

	server := NewEncryptedConn(counted, nil)
	client := NewEncryptedConn(clientNet, nil)
	server.random = bytes.NewReader([]byte{1, 2, 3, 4, 5, 6})
	messages := []OutboundMessage{
		{MessageID: 0x1300, Payload: []byte{0x10}},
		{MessageID: 0x1301, Payload: []byte{0x20, 0x21}},
		{MessageID: 0x1302, Payload: []byte{0x30, 0x31, 0x32}},
	}

	writeDone := make(chan error, 1)
	go func() { writeDone <- server.WriteMessages(messages) }()
	for index, message := range messages {
		frame, err := client.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		if frame.MessageID != message.MessageID || frame.Sequence != uint16(index) ||
			!bytes.Equal(frame.Payload[:len(message.Payload)], message.Payload) {
			t.Fatalf("frame %d = id %#x sequence %d payload %x", index, frame.MessageID, frame.Sequence, frame.Payload)
		}
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if counted.writes != 1 {
		t.Fatalf("underlying writes = %d, want 1", counted.writes)
	}
}

type writeCountingConn struct {
	net.Conn
	writes int
}

func (connection *writeCountingConn) Write(data []byte) (int, error) {
	connection.writes++
	return connection.Conn.Write(data)
}
