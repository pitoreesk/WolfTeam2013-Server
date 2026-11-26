package crypto

import (
	"errors"
	"net/netip"
	"testing"
)

func TestMatchEndpointKeyLayout(t *testing.T) {
	key, err := MatchEndpointKey(netip.MustParseAddr("127.0.0.1"), 40707)
	if err != nil {
		t.Fatal(err)
	}
	want := [8]byte{0x9f, 0x03, 0x7f, 0x00, 0x00, 0x01, 0x03, 0x9f}
	if key != want {
		t.Fatalf("endpoint key = %x, want %x", key, want)
	}
}

func TestMatchEndpointKeyRejectsInvalidEndpoint(t *testing.T) {
	if _, err := MatchEndpointKey(netip.MustParseAddr("::1"), 40707); !errors.Is(err, ErrMatchAddress) {
		t.Fatalf("IPv6 key error = %v, want %v", err, ErrMatchAddress)
	}
	if _, err := MatchEndpointKey(netip.MustParseAddr("127.0.0.1"), 0); !errors.Is(err, ErrMatchPort) {
		t.Fatalf("zero-port key error = %v, want %v", err, ErrMatchPort)
	}
}

func TestMatchCodecAdaptsLittleEndianWords(t *testing.T) {
	codec, err := newMatchCodec([]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef})
	if err != nil {
		t.Fatal(err)
	}

	plain := [8]byte{0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11}
	wantWire := [8]byte{0x80, 0xc3, 0xf9, 0x61, 0x96, 0xb0, 0x81, 0x22}
	wire := codec.applyBlock(plain, codec.block.Encrypt)
	if wire != wantWire {
		t.Fatalf("encoded block = %x, want %x", wire, wantWire)
	}
	decoded := codec.applyBlock(wire, codec.block.Decrypt)
	if decoded != plain {
		t.Fatalf("decoded block = %x, want %x", decoded, plain)
	}
}

func TestMatchCodecDatagramRoundTrip(t *testing.T) {
	codec, err := NewMatchCodec(netip.MustParseAddr("127.0.0.1"), 40707)
	if err != nil {
		t.Fatal(err)
	}
	want := MatchDatagram{
		Header: MatchHeader{
			Flags:     1,
			SessionID: 1,
			Kind:      MatchKindOpening,
			Tail:      0xaa,
		},
	}
	wire := codec.Marshal(want)
	got, err := codec.Unmarshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("decoded datagram = %#v, want %#v", got, want)
	}
	if plain := want.Header.plaintext(); plain != [8]byte{0xee, 0x01, 0x01, 0x00, 0x00, 0x00, 0x08, 0xaa} {
		t.Fatalf("plaintext header = %x", plain)
	}
}

func TestMatchHeaderStateTransition(t *testing.T) {
	opening := MatchHeader{Sequence: 0xc000}
	if !opening.IsOpening() {
		t.Fatal("opening state was not recognized")
	}
	acknowledged := opening.AcknowledgeOpening()
	if acknowledged.Sequence != 0xc001 || acknowledged.IsOpening() {
		t.Fatalf("acknowledged sequence = 0x%04x", acknowledged.Sequence)
	}
}
