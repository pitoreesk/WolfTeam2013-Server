package transport

import (
	crand "crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	wirecrypto "wtserver-go/internal/crypto"
)

const MaxEncryptedPayload = 255 * 16

type Frame struct {
	MessageID uint16
	Sequence  uint16
	Payload   []byte
}

// OutboundMessage is one independently framed protocol message. WriteMessages
// preserves this framing while combining the resulting wire bytes into one
// connection write.
type OutboundMessage struct {
	MessageID uint16
	Payload   []byte
}

// EncryptedConn owns framing, broker crypto, and the server-side transmit
// sequence for one broker/game TCP connection.
type EncryptedConn struct {
	connection net.Conn
	codec      *wirecrypto.BrokerCodec
	random     io.Reader
	writeMu    sync.Mutex
	sequence   uint16
}

func NewEncryptedConn(connection net.Conn, codec *wirecrypto.BrokerCodec) *EncryptedConn {
	if codec == nil {
		codec = wirecrypto.DefaultBrokerCodec()
	}
	return &EncryptedConn{connection: connection, codec: codec, random: crand.Reader}
}

func (c *EncryptedConn) ReadFrame() (Frame, error) {
	var frame Frame
	wirePrefix := make([]byte, 8)
	if _, err := io.ReadFull(c.connection, wirePrefix); err != nil {
		return frame, err
	}
	header, err := c.codec.ParseWirePrefix(wirePrefix)
	if err != nil {
		return frame, fmt.Errorf("decode frame prefix: %w", err)
	}
	payloadSize := int(header.BlockCount) * 16
	if payloadSize > MaxEncryptedPayload {
		return frame, fmt.Errorf("payload %d exceeds limit", payloadSize)
	}
	wireBody := make([]byte, payloadSize)
	if _, err := io.ReadFull(c.connection, wireBody); err != nil {
		return frame, err
	}
	payload, err := c.codec.DecodeBody(header, wireBody)
	if err != nil {
		return frame, fmt.Errorf("decode frame body: %w", err)
	}
	frame.MessageID = header.MessageID
	frame.Sequence = header.Sequence
	frame.Payload = payload
	return frame, nil
}

func (c *EncryptedConn) WriteMessage(messageID uint16, payload []byte) (uint16, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	sequence := c.sequence
	wire, err := c.encodeMessage(messageID, payload, sequence)
	if err != nil {
		return sequence, err
	}
	if err := WriteFull(c.connection, wire); err != nil {
		return sequence, err
	}
	c.sequence++
	return sequence, nil
}

// WriteMessages encrypts each message with its own sequential frame header and
// writes all frames together. The connection sequence is committed only after
// the combined write succeeds.
func (c *EncryptedConn) WriteMessages(messages []OutboundMessage) error {
	if len(messages) == 0 {
		return nil
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	capacity := 0
	for _, message := range messages {
		capacity += 8 + ((len(message.Payload)+15)/16)*16
	}
	wireBatch := make([]byte, 0, capacity)
	sequence := c.sequence
	for _, message := range messages {
		wire, err := c.encodeMessage(message.MessageID, message.Payload, sequence)
		if err != nil {
			return err
		}
		wireBatch = append(wireBatch, wire...)
		sequence++
	}
	if err := WriteFull(c.connection, wireBatch); err != nil {
		return err
	}
	c.sequence = sequence
	return nil
}

func (c *EncryptedConn) encodeMessage(messageID uint16, payload []byte, sequence uint16) ([]byte, error) {
	var randomness [2]byte
	if _, err := io.ReadFull(c.random, randomness[:]); err != nil {
		return nil, fmt.Errorf("generate header randomness: %w", err)
	}
	wire, err := c.codec.Seal(wirecrypto.BrokerFrame{
		Header: wirecrypto.BrokerHeader{
			SaltA:     randomness[0],
			MessageID: messageID,
			Sequence:  sequence,
			SaltB:     randomness[1],
		},
		Payload: payload,
	})
	if err != nil {
		return nil, err
	}
	return wire, nil
}

func (c *EncryptedConn) Close() error         { return c.connection.Close() }
func (c *EncryptedConn) RemoteAddr() net.Addr { return c.connection.RemoteAddr() }
func (c *EncryptedConn) LocalAddr() net.Addr  { return c.connection.LocalAddr() }

// WriteFull writes all data or returns the first write error.
func WriteFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func IsClosed(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed)
}
