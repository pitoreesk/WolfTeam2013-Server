package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	LTToken                  uint32 = 0x0000ffcc
	LTMessageBits                   = 3
	LTMessageQuery                  = 0
	LTMessageQueryResponse          = 1
	LTMessageConnect                = 2
	LTMessageConnectResponse        = 3
	LTMessagePing                   = 4
	LTMessagePingResponse           = 5
	LTPingBits                      = 24
)

var ErrBitstreamExhausted = errors.New("bitstream exhausted")

func IsLTUnconnected(packet []byte) bool {
	return len(packet) >= 4 && binary.LittleEndian.Uint32(packet[:4]) == LTToken
}

// BitWriter appends low-order bits first, as used by the LithTech unconnected
// UDP messages. It writes at most one partial byte per iteration.
type BitWriter struct {
	data   []byte
	bitLen int
}

func (w *BitWriter) Bits(value uint64, count int) {
	if count < 0 || count > 64 {
		panic(fmt.Sprintf("invalid bit count %d", count))
	}
	for consumed := 0; consumed < count; {
		byteIndex := w.bitLen / 8
		bitOffset := w.bitLen % 8
		if byteIndex == len(w.data) {
			w.data = append(w.data, 0)
		}
		chunkBits := min(count-consumed, 8-bitOffset)
		mask := byte((uint16(1) << chunkBits) - 1)
		w.data[byteIndex] |= (byte(value>>consumed) & mask) << bitOffset
		w.bitLen += chunkBits
		consumed += chunkBits
	}
}

func (w *BitWriter) Bool(value bool) {
	if value {
		w.Bits(1, 1)
		return
	}
	w.Bits(0, 1)
}

func (w *BitWriter) U8(value byte)    { w.Bits(uint64(value), 8) }
func (w *BitWriter) U32(value uint32) { w.Bits(uint64(value), 32) }

func (w *BitWriter) String(value string) {
	for _, character := range []byte(value) {
		w.U8(character)
	}
	w.U8(0)
}

func (w *BitWriter) Clone() []byte {
	return append([]byte(nil), w.data...)
}

type BitReader struct {
	data   []byte
	bitPos int
}

func NewBitReader(data []byte) *BitReader {
	return &BitReader{data: data}
}

func (r *BitReader) Bits(count int) (uint64, error) {
	if count < 0 || count > 64 {
		return 0, fmt.Errorf("invalid bit count %d", count)
	}
	if r.bitPos+count > len(r.data)*8 {
		return 0, ErrBitstreamExhausted
	}
	var value uint64
	for produced := 0; produced < count; {
		byteIndex := r.bitPos / 8
		bitOffset := r.bitPos % 8
		chunkBits := min(count-produced, 8-bitOffset)
		mask := byte((uint16(1) << chunkBits) - 1)
		chunk := r.data[byteIndex] >> bitOffset & mask
		value |= uint64(chunk) << produced
		r.bitPos += chunkBits
		produced += chunkBits
	}
	return value, nil
}

func LTConnectResponse(accept, wrongVersion bool) []byte {
	writer := newLTResponse(LTMessageConnectResponse)
	writer.Bool(accept)
	if !accept {
		writer.Bool(!wrongVersion)
	}
	return writer.Clone()
}

func LTQueryResponse(sessionName string, queryNumber byte, password bool, gameType byte) []byte {
	writer := newLTResponse(LTMessageQueryResponse)
	writer.Bool(true)
	writer.String(sessionName)
	writer.U8(queryNumber)
	writer.Bool(password)
	writer.U8(gameType)
	return writer.Clone()
}

func LTPingResponse(pingValue uint32) []byte {
	writer := newLTResponse(LTMessagePingResponse)
	writer.Bits(uint64(pingValue&0x00ffffff), LTPingBits)
	return writer.Clone()
}

func newLTResponse(message int) BitWriter {
	var writer BitWriter
	writer.U32(LTToken)
	writer.Bits(uint64(message), LTMessageBits)
	return writer
}
