package crypto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
)

//go:generate go run ./internal/genconstants

const (
	brokerScheduleSize = (18 + 4*256) * 4
	brokerPrefixSize   = 8
	brokerBodyBlock    = 16
)

var (
	brokerCompatibilityKey = [...]byte{0x57, 0x6f, 0x6c, 0x66, 0x54, 0x65, 0x61, 0x6d}

	ErrBrokerKey       = errors.New("broker key must contain 1 to 56 bytes")
	ErrBrokerPrefix    = errors.New("broker prefix must contain 8 bytes")
	ErrBrokerIntegrity = errors.New("broker prefix checksum mismatch")
	ErrFrameTooShort   = errors.New("broker frame is shorter than 8 bytes")
	ErrFrameTooLarge   = errors.New("broker frame body exceeds 255 blocks")

	loadDefaultBrokerCodec = sync.OnceValue(func() *BrokerCodec {
		codec, err := NewBrokerCodec(brokerCompatibilityKey[:])
		if err != nil {
			panic(fmt.Sprintf("initialize broker codec: %v", err))
		}
		return codec
	})
)

// BrokerHeader describes the plaintext prefix of one broker frame. Checksum is
// intentionally absent because it is derived from the other seven bytes.
type BrokerHeader struct {
	SaltA      byte
	MessageID  uint16
	Sequence   uint16
	SaltB      byte
	BlockCount byte
}

func (h BrokerHeader) plaintext() [brokerPrefixSize]byte {
	var result [brokerPrefixSize]byte
	result[0] = h.SaltA
	binary.LittleEndian.PutUint16(result[1:3], h.MessageID)
	binary.LittleEndian.PutUint16(result[3:5], h.Sequence)
	result[5] = h.SaltB
	result[6] = h.BlockCount
	for _, value := range result[:7] {
		result[7] += value
	}
	return result
}

func parseBrokerHeader(plain []byte) (BrokerHeader, error) {
	if len(plain) != brokerPrefixSize {
		return BrokerHeader{}, ErrBrokerPrefix
	}
	var checksum byte
	for _, value := range plain[:7] {
		checksum += value
	}
	if checksum != plain[7] {
		return BrokerHeader{}, ErrBrokerIntegrity
	}
	return BrokerHeader{
		SaltA:      plain[0],
		MessageID:  binary.LittleEndian.Uint16(plain[1:3]),
		Sequence:   binary.LittleEndian.Uint16(plain[3:5]),
		SaltB:      plain[5],
		BlockCount: plain[6],
	}, nil
}

// BrokerFrame is the decoded form of one complete broker message. Payload
// includes zero padding through the block boundary.
type BrokerFrame struct {
	Header  BrokerHeader
	Payload []byte
}

// BrokerCodec owns the immutable header-key schedule and the complete framing
// transformation.
type BrokerCodec struct {
	p [18]uint32
	s [4][256]uint32
}

func DefaultBrokerCodec() *BrokerCodec {
	return loadDefaultBrokerCodec()
}

// NewBrokerCodec expands a key using the protocol's header-cipher round
// function. Its grouping is (S0+S1) XOR (S2+S3), with little-endian words.
func NewBrokerCodec(key []byte) (*BrokerCodec, error) {
	if len(key) == 0 || len(key) > 56 {
		return nil, fmt.Errorf("%w: got %d", ErrBrokerKey, len(key))
	}

	codec := &BrokerCodec{
		p: brokerInitialP,
		s: brokerInitialS,
	}

	keyOffset := 0
	for i := range codec.p {
		var word uint32
		for range 4 {
			word = word<<8 | uint32(key[keyOffset])
			keyOffset++
			if keyOffset == len(key) {
				keyOffset = 0
			}
		}
		codec.p[i] ^= word
	}

	var left, right uint32
	for i := 0; i < len(codec.p); i += 2 {
		left, right = codec.forwardWords(left, right)
		codec.p[i], codec.p[i+1] = left, right
	}
	for box := range codec.s {
		for i := 0; i < len(codec.s[box]); i += 2 {
			left, right = codec.forwardWords(left, right)
			codec.s[box][i], codec.s[box][i+1] = left, right
		}
	}

	return codec, nil
}

func (c *BrokerCodec) mix(value uint32) uint32 {
	b3 := byte(value >> 24)
	b2 := byte(value >> 16)
	b1 := byte(value >> 8)
	b0 := byte(value)
	return (c.s[0][b3] + c.s[1][b2]) ^ (c.s[2][b1] + c.s[3][b0])
}

func (c *BrokerCodec) forward(block *[brokerPrefixSize]byte) {
	left := binary.LittleEndian.Uint32(block[:4])
	right := binary.LittleEndian.Uint32(block[4:])
	left, right = c.forwardWords(left, right)
	binary.LittleEndian.PutUint32(block[:4], left)
	binary.LittleEndian.PutUint32(block[4:], right)
}

func (c *BrokerCodec) forwardWords(left, right uint32) (uint32, uint32) {
	for round := 0; round < 16; round++ {
		left ^= c.p[round]
		right ^= c.mix(left)
		left, right = right, left
	}
	left, right = right, left
	right ^= c.p[16]
	left ^= c.p[17]
	return left, right
}

func (c *BrokerCodec) inverse(block *[brokerPrefixSize]byte) {
	left := binary.LittleEndian.Uint32(block[:4])
	right := binary.LittleEndian.Uint32(block[4:])
	for round := 17; round > 1; round-- {
		left ^= c.p[round]
		right ^= c.mix(left)
		left, right = right, left
	}
	left, right = right, left
	right ^= c.p[1]
	left ^= c.p[0]
	binary.LittleEndian.PutUint32(block[:4], left)
	binary.LittleEndian.PutUint32(block[4:], right)
}

func (c *BrokerCodec) wirePrefix(header BrokerHeader) [brokerPrefixSize]byte {
	result := header.plaintext()
	maskBrokerPrefix(&result)
	c.forward(&result)
	return result
}

// ParseWirePrefix authenticates and decodes the first eight wire bytes. It is
// separate from Open so stream readers can determine the following body size.
func (c *BrokerCodec) ParseWirePrefix(wire []byte) (BrokerHeader, error) {
	if len(wire) != brokerPrefixSize {
		return BrokerHeader{}, ErrBrokerPrefix
	}
	var plain [brokerPrefixSize]byte
	copy(plain[:], wire)
	c.inverse(&plain)
	unmaskBrokerPrefix(&plain)
	return parseBrokerHeader(plain[:])
}

// DecodeBody decrypts the body belonging to an already authenticated prefix.
func (c *BrokerCodec) DecodeBody(header BrokerHeader, wire []byte) ([]byte, error) {
	expected := int(header.BlockCount) * brokerBodyBlock
	if len(wire) != expected {
		return nil, fmt.Errorf("broker body length %d does not match prefix length %d", len(wire), expected)
	}
	return transformBrokerBody(wire, header.plaintext(), true)
}

// Seal serializes a frame, zero-pads its body, and updates BlockCount from the
// resulting body length.
func (c *BrokerCodec) Seal(frame BrokerFrame) ([]byte, error) {
	blocks := (len(frame.Payload) + brokerBodyBlock - 1) / brokerBodyBlock
	if blocks > 255 {
		return nil, ErrFrameTooLarge
	}
	frame.Header.BlockCount = byte(blocks)
	wirePrefix := c.wirePrefix(frame.Header)
	padded := make([]byte, blocks*brokerBodyBlock)
	copy(padded, frame.Payload)
	wireBody, err := transformBrokerBody(padded, frame.Header.plaintext(), false)
	if err != nil {
		return nil, err
	}
	result := make([]byte, brokerPrefixSize+len(wireBody))
	copy(result, wirePrefix[:])
	copy(result[brokerPrefixSize:], wireBody)
	return result, nil
}

// Open validates and decodes one complete wire frame.
func (c *BrokerCodec) Open(wire []byte) (BrokerFrame, error) {
	if len(wire) < brokerPrefixSize {
		return BrokerFrame{}, ErrFrameTooShort
	}
	header, err := c.ParseWirePrefix(wire[:brokerPrefixSize])
	if err != nil {
		return BrokerFrame{}, err
	}
	body, err := c.DecodeBody(header, wire[brokerPrefixSize:])
	if err != nil {
		return BrokerFrame{}, err
	}
	return BrokerFrame{Header: header, Payload: body}, nil
}
