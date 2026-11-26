package protocol

import (
	"encoding/binary"
	"unicode/utf16"
)

// Writer builds little-endian packet bodies.
// It grows as needed; capacity hints do not affect the emitted schema.
type Writer struct {
	buf []byte
}

func NewWriter(capacity int) *Writer {
	return &Writer{buf: make([]byte, 0, capacity)}
}

func (w *Writer) U8(value byte) *Writer {
	w.buf = append(w.buf, value)
	return w
}

func (w *Writer) Bool(value bool) *Writer {
	if value {
		return w.U8(1)
	}
	return w.U8(0)
}

func (w *Writer) U16(value uint16) *Writer {
	w.buf = binary.LittleEndian.AppendUint16(w.buf, value)
	return w
}

func (w *Writer) U32(value uint32) *Writer {
	w.buf = binary.LittleEndian.AppendUint32(w.buf, value)
	return w
}

func (w *Writer) Bytes(value []byte) *Writer {
	w.buf = append(w.buf, value...)
	return w
}

// ASCII emits one low byte per UTF-16 code unit, as used by legacy text fields.
func (w *Writer) ASCII(value string) *Writer {
	for _, unit := range utf16.Encode([]rune(value)) {
		w.U8(byte(unit))
	}
	return w
}

// UTF16LE emits little-endian UTF-16 code units without adding a terminator.
func (w *Writer) UTF16LE(value string) *Writer {
	for _, unit := range utf16.Encode([]rune(value)) {
		w.U16(unit)
	}
	return w
}

func (w *Writer) Len() int {
	return len(w.buf)
}

func (w *Writer) Clone() []byte {
	return append([]byte(nil), w.buf...)
}

func Pad16(payload []byte) []byte {
	if len(payload)%16 == 0 {
		return append([]byte(nil), payload...)
	}
	padded := make([]byte, (len(payload)+15)&^15)
	copy(padded, payload)
	return padded
}
