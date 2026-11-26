package crypto

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

func TestBrokerGeneratedScheduleMatchesCompatibilityVector(t *testing.T) {
	codec, err := NewBrokerCodec(brokerCompatibilityKey[:])
	if err != nil {
		t.Fatal(err)
	}

	schedule := make([]byte, brokerScheduleSize)
	offset := 0
	for _, word := range codec.p {
		binary.LittleEndian.PutUint32(schedule[offset:offset+4], word)
		offset += 4
	}
	for box := range codec.s {
		for _, word := range codec.s[box] {
			binary.LittleEndian.PutUint32(schedule[offset:offset+4], word)
			offset += 4
		}
	}

	sum := sha256.Sum256(schedule)
	const want = "889ac0e7b4548b5f4e61db884d801c9bca5cff44b5114f5484068f675a3f9aa0"
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("generated schedule SHA-256 = %s, want %s", got, want)
	}
}

func TestBrokerPrefixCompatibilityVector(t *testing.T) {
	codec := DefaultBrokerCodec()
	wire := []byte{0xc9, 0x72, 0x9f, 0x69, 0xce, 0xd9, 0xbb, 0x70}
	want := BrokerHeader{SaltA: 0x47, MessageID: 0x1202, Sequence: 2, SaltB: 0x0a}
	got, err := codec.ParseWirePrefix(wire)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("decoded prefix = %#v, want %#v", got, want)
	}
	encoded := codec.wirePrefix(got)
	if !bytes.Equal(encoded[:], wire) {
		t.Fatalf("encoded prefix = %x, want %x", encoded, wire)
	}
}

func TestBrokerCodecRoundTrip(t *testing.T) {
	codec := DefaultBrokerCodec()
	want := BrokerFrame{
		Header:  BrokerHeader{SaltA: 0x47, MessageID: 0x1101, Sequence: 5, SaltB: 0x5a},
		Payload: []byte{0xaa, 0xbb, 0xcc},
	}
	wire, err := codec.Seal(want)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) != 24 {
		t.Fatalf("wire length = %d, want 24", len(wire))
	}
	got, err := codec.Open(wire)
	if err != nil {
		t.Fatal(err)
	}
	if got.Header.BlockCount != 1 || got.Header.SaltB != 0x5a {
		t.Fatalf("decoded prefix = %#v", got.Header)
	}
	if !bytes.Equal(got.Payload[:len(want.Payload)], want.Payload) {
		t.Fatalf("decoded body = %x, want prefix %x", got.Payload, want.Payload)
	}
	if !bytes.Equal(got.Payload[len(want.Payload):], make([]byte, 13)) {
		t.Fatalf("decoded padding is not zero: %x", got.Payload[len(want.Payload):])
	}
}

func TestDefaultBrokerCodecIsSharedAndConcurrent(t *testing.T) {
	shared := DefaultBrokerCodec()
	header := BrokerHeader{SaltA: 0x47, MessageID: 0x1202, Sequence: 2, SaltB: 0x0a}
	wantWire := [8]byte{0xc9, 0x72, 0x9f, 0x69, 0xce, 0xd9, 0xbb, 0x70}

	for range 32 {
		t.Run("worker", func(t *testing.T) {
			t.Parallel()
			codec := DefaultBrokerCodec()
			if codec != shared {
				t.Fatal("DefaultBrokerCodec returned a different schedule instance")
			}
			for range 100 {
				wire := codec.wirePrefix(header)
				if wire != wantWire {
					t.Fatalf("concurrent encode = %x, want %x", wire, wantWire)
				}
				decoded, err := codec.ParseWirePrefix(wire[:])
				if err != nil {
					t.Fatal(err)
				}
				if decoded != header {
					t.Fatalf("concurrent decode = %#v, want %#v", decoded, header)
				}
			}
		})
	}
}
