package game

import (
	"testing"
	"time"

	"wtserver-go/internal/store"
)

// Go translation of sub_3420CC80 (0x1325) up to the crest list; checks exact
// consumption with and without a Pride.
func TestCharStdInfoMatchesClientParser(t *testing.T) {
	request, err := ParseCharStdInfoRequest([]byte{3, 'a', 'b', 'c', 4, 'X', 0, 'Y', 0, 0xee})
	if err != nil || request.LoginName != "abc" || request.Callsign != "XY" {
		t.Fatalf("request = %+v %v", request, err)
	}
	info := store.AccountSnapshot{Level: 40, GP: 9000, Kills: 5, Deaths: 2}
	for _, pride := range []store.Pride{{}, {ID: 9, Name: "SUCUKLAR", Master: "Usta", MemberCount: 3, CreatedAt: time.Unix(0, 0)}} {
		payload, err := CharStdInfoPayload("Oyuncu", info, pride, store.PrideMember{}, 0, []store.CrestDelegateSlot{{Slot: 1, CrestID: 3004}})
		if err != nil {
			t.Fatal(err)
		}
		r := newReader2012(t, payload)
		if r.utf16() != "Oyuncu" {
			t.Fatal("callsign")
		}
		r.u8()
		r.u8()
		for i := 0; i < 6; i++ {
			r.u32()
		}
		r.u8()
		if level := r.u8(); level != 40 {
			t.Fatalf("level = %d", level)
		}
		for i := 0; i < 11; i++ {
			r.u32()
		}
		if id := r.u32(); id != pride.ID {
			t.Fatalf("pride id = %d", id)
		}
		if id := pride.ID; id != 0 {
			r.utf16()
			r.utf16()
			r.u32()
			r.u8()
			r.ascii()
			r.u16()
			r.utf16()
			r.utf16()
			if closeStatus := r.u8(); closeStatus >= 10 {
				r.ascii()
			}
			r.u8()
			r.u8()
			r.ascii()
			for n := r.u8(); n > 0; n-- {
				r.ascii()
			}
			for n := r.u8(); n > 0; n-- {
				r.u16()
			}
		}
		if n := r.u8(); n != 1 {
			t.Fatalf("crest count = %d", n)
		}
		if slot, crest := r.u8(), r.u16(); slot != 1 || crest != 3004 {
			t.Fatalf("crest = %d/%d", slot, crest)
		}
		r.u16()
		r.done()
	}
}
