package protocol

import (
	"bytes"
	"testing"
)

func TestHeaderRoundTrip(t *testing.T) {
	want := NewHeader(0x47, 0x1202, 10, 0x55, 2)
	encoded := want.MarshalBinary()
	got, err := ParseHeader(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || !got.Valid() || got.PayloadSize() != 32 {
		t.Fatalf("header round trip: got %+v, want %+v", got, want)
	}
}

func TestWriterEncoding(t *testing.T) {
	w := NewWriter(16)
	w.U8(0xaa)
	w.U16(0x1234)
	w.U32(0x89abcdef)
	w.ASCII("A")
	w.UTF16LE("B")
	want := []byte{0xaa, 0x34, 0x12, 0xef, 0xcd, 0xab, 0x89, 'A', 'B', 0}
	if got := w.Clone(); !bytes.Equal(got, want) {
		t.Fatalf("writer = %x, want %x", got, want)
	}
}

func TestPad16(t *testing.T) {
	got := Pad16([]byte{1, 2, 3})
	if len(got) != 16 || got[0] != 1 || got[2] != 3 || got[3] != 0 {
		t.Fatalf("unexpected padding: %x", got)
	}
}
