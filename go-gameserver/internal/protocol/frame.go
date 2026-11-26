package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const HeaderSize = 8

var ErrShortHeader = errors.New("protocol header is shorter than 8 bytes")

type Header struct {
	Random0       byte
	MessageID     uint16
	Sequence      uint16
	Random5       byte
	PayloadBlocks byte
	Checksum      byte
}

func NewHeader(random0 byte, messageID, sequence uint16, random5, blocks byte) Header {
	h := Header{
		Random0:       random0,
		MessageID:     messageID,
		Sequence:      sequence,
		Random5:       random5,
		PayloadBlocks: blocks,
	}
	h.Checksum = h.expectedChecksum()
	return h
}

func ParseHeader(data []byte) (Header, error) {
	if len(data) < HeaderSize {
		return Header{}, ErrShortHeader
	}
	return Header{
		Random0:       data[0],
		MessageID:     binary.LittleEndian.Uint16(data[1:3]),
		Sequence:      binary.LittleEndian.Uint16(data[3:5]),
		Random5:       data[5],
		PayloadBlocks: data[6],
		Checksum:      data[7],
	}, nil
}

func (h Header) MarshalBinary() []byte {
	data := make([]byte, HeaderSize)
	data[0] = h.Random0
	binary.LittleEndian.PutUint16(data[1:3], h.MessageID)
	binary.LittleEndian.PutUint16(data[3:5], h.Sequence)
	data[5] = h.Random5
	data[6] = h.PayloadBlocks
	data[7] = h.Checksum
	return data
}

func (h Header) Valid() bool {
	return h.Checksum == h.expectedChecksum()
}

func (h Header) PayloadSize() int {
	return int(h.PayloadBlocks) * 16
}

func (h Header) ValidatePayloadSize(payload []byte) error {
	if len(payload) != h.PayloadSize() {
		return fmt.Errorf("payload length %d does not match header length %d", len(payload), h.PayloadSize())
	}
	return nil
}

func (h Header) expectedChecksum() byte {
	data := h.MarshalBinary()
	var checksum byte
	for _, value := range data[:7] {
		checksum += value
	}
	return checksum
}
