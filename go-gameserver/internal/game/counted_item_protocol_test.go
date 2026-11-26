package game

import (
	"bytes"
	"context"
	"encoding/binary"
	"slices"
	"testing"

	"wtserver-go/internal/store"
)

func TestUseCountItemPacketLayoutsMatchProtocol(t *testing.T) {
	requestPayload := make([]byte, 9)
	requestPayload[0] = 2
	binary.LittleEndian.PutUint32(requestPayload[1:5], 2_000_019_001)
	binary.LittleEndian.PutUint32(requestPayload[5:9], 2_000_019_502)
	uniqueIDs, err := ParseUseCountItemRequest(requestPayload)
	if err != nil || !slices.Equal(uniqueIDs, []int{2_000_019_001, 2_000_019_502}) {
		t.Fatalf("use-count-item request = %v, %v", uniqueIDs, err)
	}

	reply, err := UseCountItemReplyPayload(0, []store.CountedItemUse{
		{UniqueID: 2_000_019_001, Remaining: 4},
		{UniqueID: 2_000_019_502, Remaining: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 2, 0x39, 0xde, 0x35, 0x77, 4, 0, 0x2e, 0xe0, 0x35, 0x77, 0, 0}
	if !bytes.Equal(reply, want) {
		t.Fatalf("use-count-item reply = %x, want %x", reply, want)
	}
	if failed, err := UseCountItemReplyPayload(1, nil); err != nil || !bytes.Equal(failed, []byte{1}) {
		t.Fatalf("failed use-count-item reply = %x, %v", failed, err)
	}
}

func TestUseCountItemRequestRejectsMalformedRecords(t *testing.T) {
	for _, payload := range [][]byte{
		nil,
		{0},
		{2, 1, 2, 3, 4},
		{1, 1, 0, 0, 0},
		{2, 0x39, 0x7f, 0x38, 0x77, 0x39, 0x7f, 0x38, 0x77},
	} {
		if _, err := ParseUseCountItemRequest(payload); err == nil {
			t.Fatalf("accepted malformed use-count-item payload %x", payload)
		}
	}
}

type countedItemUsers struct {
	*fakeUsers
	requested []int
}

func (users *countedItemUsers) ConsumeInventoryItem(context.Context, int, int) (bool, error) {
	return false, nil
}

func (users *countedItemUsers) ConsumeCountedItems(_ context.Context, _ int, uniqueIDs []int) ([]store.CountedItemUse, error) {
	users.requested = append([]int(nil), uniqueIDs...)
	return []store.CountedItemUse{{UniqueID: uniqueIDs[0], Remaining: 2}}, nil
}

func TestServerHandlesInGameCountedItemUse(t *testing.T) {
	users := &countedItemUsers{fakeUsers: &fakeUsers{}}
	harness := newFailSoftHarness(t, users)
	payload := make([]byte, 5)
	payload[0] = 1
	binary.LittleEndian.PutUint32(payload[1:5], 2_000_019_001)
	frame := dispatchAndRead(t, harness, RequestUseCountItem, ReplyUseCountItem, payload)
	want := []byte{0, 1, 0x39, 0xde, 0x35, 0x77, 2, 0}
	if !slices.Equal(users.requested, []int{2_000_019_001}) ||
		len(frame.Payload) < len(want) || !bytes.Equal(frame.Payload[:len(want)], want) {
		t.Fatalf("counted item dispatch = request %v, reply %x", users.requested, frame.Payload)
	}
}

func TestServerHandlesGenericInGameCountedItemUse(t *testing.T) {
	users := &countedItemUsers{fakeUsers: &fakeUsers{}}
	harness := newFailSoftHarness(t, users)
	payload := make([]byte, 11)
	binary.LittleEndian.PutUint32(payload[0:4], 2_000_019_501)
	binary.LittleEndian.PutUint16(payload[4:6], 19_501)

	frame := dispatchAndRead(t, harness, RequestUseItem, ReplyUseItem, payload)
	if !slices.Equal(users.requested, []int{2_000_019_501}) {
		t.Fatalf("generic counted item request = %v", users.requested)
	}
	if len(frame.Payload) < 39 || frame.Payload[0] != 0 {
		t.Fatalf("generic counted item common status = %x, want success", frame.Payload)
	}
	if remaining := binary.LittleEndian.Uint16(frame.Payload[26:28]); remaining != 2 {
		t.Fatalf("generic counted item remaining = %d, want 2; payload=%x", remaining, frame.Payload)
	}
}
