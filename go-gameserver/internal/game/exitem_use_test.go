package game

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestExItemEffectsFollowClientUseTypes(t *testing.T) {
	if e, ok := exItemEffect(GenericUseItemRequest{ItemID: 10019, Text: "Yeni"}); !ok || e.NewCallsign != "Yeni" || e.Type != 1 {
		t.Fatalf("rename = %+v %v", e, ok)
	}
	if _, ok := exItemEffect(GenericUseItemRequest{ItemID: 10019}); ok {
		t.Fatal("rename without a name was accepted")
	}
	if e, ok := exItemEffect(GenericUseItemRequest{ItemID: 10012}); !ok || !e.ResetKills {
		t.Fatalf("K/D reset = %+v", e)
	}
	if e, ok := exItemEffect(GenericUseItemRequest{ItemID: 10026}); !ok || !e.ResetWins {
		t.Fatalf("W/L reset = %+v", e)
	}
	if e, ok := exItemEffect(GenericUseItemRequest{ItemID: 10014, Text: "KLAN"}); !ok || e.NewPrideName != "KLAN" {
		t.Fatalf("pride rename = %+v", e)
	}
	if e, ok := exItemEffect(GenericUseItemRequest{ItemID: 15001}); !ok || e.Gold != 5000 {
		t.Fatalf("gold coupon = %+v", e)
	}
	if e, ok := exItemEffect(GenericUseItemRequest{ItemID: 15024}); !ok || e.WC != 100 {
		t.Fatalf("KP coupon = %+v", e)
	}
	if e, ok := exItemEffect(GenericUseItemRequest{ItemID: 15114}); !ok || e.GP != 1000000 {
		t.Fatalf("GP coupon = %+v", e)
	}
	if _, ok := exItemEffect(GenericUseItemRequest{ItemID: 10001}); ok {
		t.Fatal("a passive booster is not usable")
	}
}

func TestFullUpLayouts(t *testing.T) {
	payload := make([]byte, 18)
	binary.LittleEndian.PutUint32(payload[0:], 2_000_001_001)
	for i := 0; i < 4; i++ {
		binary.LittleEndian.PutUint16(payload[4+2*i:], uint16(10+i))
	}
	binary.LittleEndian.PutUint32(payload[12:], 2_000_010_020)
	binary.LittleEndian.PutUint16(payload[16:], 10020)
	request, err := ParseFullUpRequest(payload)
	if err != nil || request.WeaponUnique != 2_000_001_001 || request.Values != [4]uint16{10, 11, 12, 13} || request.KitID != 10020 {
		t.Fatalf("full-up request = %+v %v", request, err)
	}
	reply := FullUpReplyPayload(request, "20991231235959", 0xffff, 2)
	want := []byte{0, 0xe9, 0x97, 0x35, 0x77, 14}
	want = append(want, []byte("20991231235959")...)
	want = append(want, 0xff, 0xff, 0x24, 0xbb, 0x35, 0x77, 2, 0)
	if !bytes.Equal(reply, want) {
		t.Fatalf("full-up reply = %x\nwant %x", reply, want)
	}
}
