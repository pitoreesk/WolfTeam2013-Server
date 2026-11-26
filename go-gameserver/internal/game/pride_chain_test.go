package game

import (
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"unicode/utf16"

	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

func TestPrideChainLevelFollowsTheChainPage(t *testing.T) {
	for _, test := range []struct{ position, count, want int }{
		{1, 3, 5}, {5, 5, 5}, // up to 5 members: chain off
		{1, 6, 4}, {5, 6, 4}, {6, 6, 3},
		{13, 20, 3}, {14, 20, 2}, {23, 30, 2}, {24, 30, 1}, {35, 35, 1},
		{1, 36, 0}, // above 35 members everyone is "Bingo!!"
	} {
		if got := prideChainLevel(test.position, test.count); got != test.want {
			t.Fatalf("level(position %d of %d) = %d, want %d", test.position, test.count, got, test.want)
		}
	}
	if prideChainLevelCode(5) != 0 || prideChainLevelCode(0) != 100 || prideChainLevelCode(3) != 3 {
		t.Fatal("level codes do not match sub_34329610")
	}
}

// Go translation of sub_343020F0's record loop.
func TestPrideChainListMatchesTheClientParser(t *testing.T) {
	payload := PrideChainListPayload([]PrideChainEntry{{Callsign: "Kurt", Order: 1}, {Callsign: "Avcı", Order: 2}})
	pos := 1 // the pump consumed the result byte
	count := int(payload[pos])
	pos++
	var names []string
	for range count {
		narrow := int(payload[pos])
		pos += 1 + narrow
		wide := int(payload[pos])
		pos++
		units := make([]uint16, wide/2)
		for i := range units {
			units[i] = binary.LittleEndian.Uint16(payload[pos+2*i:])
		}
		pos += wide
		names = append(names, string(utf16.Decode(units)))
		pos += 4
	}
	if pos != len(payload) || count != 2 || names[0] != "Kurt" || names[1] != "Avcı" {
		t.Fatalf("parsed %d members %q, consumed %d of %d", count, names, pos, len(payload))
	}
}

func TestPrideChainListsOnlineMembersInLoginOrder(t *testing.T) {
	server := newRankingTestServer(&rankingUsers{fakeUsers: &fakeUsers{}})
	loopback := netip.MustParseAddr("127.0.0.1")
	add := func(login string, userID int, prideID uint32, order uint64) *Session {
		client, peer := net.Pipe()
		t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
		session, err := newSession(transport.NewEncryptedConn(peer, nil), loopback, loopback)
		if err != nil {
			t.Fatal(err)
		}
		session.setAuthenticated(login, userID, login, 10)
		session.setPrideDisplay("Klan", prideID)
		session.setLoginOrder(order)
		server.mu.Lock()
		server.sessions[session] = struct{}{}
		server.mu.Unlock()
		return session
	}
	late := add("late", 1, 7, 9)
	add("first", 2, 7, 3)
	add("stranger", 3, 8, 1)
	add("second", 4, 7, 5)
	loner := add("loner", 5, 0, 2)
	chain, position := server.prideChain(late)
	if len(chain) != 3 || chain[0].Callsign != "first" || chain[1].Callsign != "second" || chain[2].Callsign != "late" || position != 3 {
		t.Fatalf("chain = %+v, position %d", chain, position)
	}
	if chain[0].Order != 1 || chain[2].Order != 3 {
		t.Fatalf("keys = %+v", chain)
	}
	if chain, position := server.prideChain(loner); chain != nil || position != 0 {
		t.Fatalf("Pride-less chain = %+v, %d", chain, position)
	}
}

// sub_34328EF0: grade 0 shows PrideClose (master), 8 shows PrideLeave.
func TestPrideGradePolarity(t *testing.T) {
	if prideGrade(store.Pride{}, 8) != 0 || prideGrade(store.Pride{}, 0) != 8 || prideGrade(store.Pride{CloseStatus: 10}, 8) != 10 {
		t.Fatal("grade polarity does not match the 2013 client")
	}
}
