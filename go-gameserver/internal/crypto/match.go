package crypto

import (
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"

	"golang.org/x/crypto/blowfish"
)

const (
	matchHeaderSize    = 8
	matchDatagramSize  = 16
	matchIntegrityMask = 0x4c

	MatchKindOpening = 0x08
	MatchKindPing    = 0x0c
	MatchKindPong    = 0x0d
)

var (
	ErrMatchAddress       = errors.New("match endpoint address must be IPv4")
	ErrMatchPort          = errors.New("match endpoint port must be non-zero")
	ErrMatchDatagramSize  = errors.New("match datagram must contain 16 bytes")
	ErrMatchHeaderSize    = errors.New("match header must contain 8 bytes")
	ErrMatchHeaderInvalid = errors.New("match header integrity byte is invalid")
)

// MatchHeader is the meaningful portion of the eight-byte plaintext control
// header. The leading integrity byte is derived when the value is serialized.
type MatchHeader struct {
	Flags     byte
	SessionID uint16
	Sequence  uint16
	Kind      byte
	Tail      byte
}

// IsOpening reports whether the low six state bits identify the first
// handshake message.
func (h MatchHeader) IsOpening() bool {
	return h.Sequence&0x003f == 0
}

// AcknowledgeOpening advances an opening header to its acknowledgement state
// while preserving the two high state bits and all other fields.
func (h MatchHeader) AcknowledgeOpening() MatchHeader {
	h.Sequence = h.Sequence&0xffc0 | 1
	return h
}

func (h MatchHeader) plaintext() [matchHeaderSize]byte {
	var result [matchHeaderSize]byte
	result[1] = h.Flags
	binary.LittleEndian.PutUint16(result[2:4], h.SessionID)
	binary.LittleEndian.PutUint16(result[4:6], h.Sequence)
	result[6] = h.Kind
	result[7] = h.Tail
	result[0] = matchIntegrity(result[1:])
	return result
}

// ParseMatchHeader validates and decodes an eight-byte plaintext header.
func ParseMatchHeader(plain []byte) (MatchHeader, error) {
	if len(plain) != matchHeaderSize {
		return MatchHeader{}, ErrMatchHeaderSize
	}
	if plain[0] != matchIntegrity(plain[1:]) {
		return MatchHeader{}, ErrMatchHeaderInvalid
	}
	return MatchHeader{
		Flags:     plain[1],
		SessionID: binary.LittleEndian.Uint16(plain[2:4]),
		Sequence:  binary.LittleEndian.Uint16(plain[4:6]),
		Kind:      plain[6],
		Tail:      plain[7],
	}, nil
}

func matchIntegrity(fields []byte) byte {
	result := byte(matchIntegrityMask)
	for _, field := range fields {
		result ^= field
	}
	return result
}

// MatchDatagram is a fixed-size control message. Only its header is encrypted
// on the wire; Body remains an opaque eight-byte value.
type MatchDatagram struct {
	Header MatchHeader
	Body   [8]byte
}

// MatchEndpointKey derives the transport key from the endpoint advertised to
// the peer: network-order port, IPv4 address, then little-endian port.
func MatchEndpointKey(address netip.Addr, port uint16) ([8]byte, error) {
	var key [8]byte
	address = address.Unmap()
	if !address.Is4() {
		return key, ErrMatchAddress
	}
	if port == 0 {
		return key, ErrMatchPort
	}
	binary.BigEndian.PutUint16(key[0:2], port)
	ip := address.As4()
	copy(key[2:6], ip[:])
	binary.LittleEndian.PutUint16(key[6:8], port)
	return key, nil
}

// MatchCodec converts between typed match datagrams and their wire form.
type MatchCodec struct {
	block cipher.Block
}

func NewMatchCodec(address netip.Addr, port uint16) (*MatchCodec, error) {
	key, err := MatchEndpointKey(address, port)
	if err != nil {
		return nil, err
	}
	return newMatchCodec(key[:])
}

func newMatchCodec(key []byte) (*MatchCodec, error) {
	block, err := blowfish.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create match transport cipher: %w", err)
	}
	return &MatchCodec{block: block}, nil
}

// Marshal returns the 16-byte wire representation of a match datagram.
func (c *MatchCodec) Marshal(datagram MatchDatagram) []byte {
	plainHeader := datagram.Header.plaintext()
	wireHeader := c.applyBlock(plainHeader, c.block.Encrypt)
	result := make([]byte, matchDatagramSize)
	copy(result[:matchHeaderSize], wireHeader[:])
	copy(result[matchHeaderSize:], datagram.Body[:])
	return result
}

// Unmarshal validates and decodes one fixed-size wire datagram.
func (c *MatchCodec) Unmarshal(wire []byte) (MatchDatagram, error) {
	if len(wire) != matchDatagramSize {
		return MatchDatagram{}, ErrMatchDatagramSize
	}
	var wireHeader [matchHeaderSize]byte
	copy(wireHeader[:], wire[:matchHeaderSize])
	plainHeader := c.applyBlock(wireHeader, c.block.Decrypt)
	header, err := ParseMatchHeader(plainHeader[:])
	if err != nil {
		return MatchDatagram{}, err
	}
	result := MatchDatagram{Header: header}
	copy(result.Body[:], wire[matchHeaderSize:])
	return result, nil
}

// applyBlock adapts the protocol's little-endian 32-bit halves to the block
// cipher package's conventional byte order.
func (c *MatchCodec) applyBlock(input [matchHeaderSize]byte, operation func(dst, src []byte)) [matchHeaderSize]byte {
	var conventional [matchHeaderSize]byte
	swapWordBytes(&conventional, &input)
	operation(conventional[:], conventional[:])
	var result [matchHeaderSize]byte
	swapWordBytes(&result, &conventional)
	return result
}

func swapWordBytes(destination, source *[matchHeaderSize]byte) {
	for offset := 0; offset < matchHeaderSize; offset += 4 {
		destination[offset] = source[offset+3]
		destination[offset+1] = source[offset+2]
		destination[offset+2] = source[offset+1]
		destination[offset+3] = source[offset]
	}
}
