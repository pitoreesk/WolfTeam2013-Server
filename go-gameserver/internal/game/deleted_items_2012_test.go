package game

import (
	"context"
	"testing"
	"time"

	"wtserver-go/internal/transport"
)

// TestDeletedItems2012ParsesExactly translates CS_IN_DELETEDITEM_ACK
// (sub_341F9750): u8 count (capped at 8), then {u32 skipped, u16 item ID}.
func TestDeletedItems2012ParsesExactly(t *testing.T) {
	payload, err := DeletedItemsPayload([]int{7777, 30011})
	if err != nil {
		t.Fatal(err)
	}
	r := newReader2012(t, payload)
	count := min(int(r.u8()), 8)
	var ids []uint16
	for range count {
		r.need(4)
		ids = append(ids, r.u16())
	}
	r.done()
	if len(ids) != 2 || ids[0] != 7777 || ids[1] != 30011 {
		t.Fatalf("ids = %v", ids)
	}
}

func assertNoReply(t *testing.T, harness failSoftHarness, request uint16) {
	t.Helper()
	if err := harness.server.handleMessage(context.Background(), harness.session, transport.Frame{MessageID: request}); err != nil {
		t.Fatalf("handle %#x: %v", request, err)
	}
	got := make(chan error, 1)
	go func() { _, err := harness.client.ReadFrame(); got <- err }()
	select {
	case err := <-got:
		t.Fatalf("unexpected reply to %#x (read err %v)", request, err)
	case <-time.After(200 * time.Millisecond):
	}
}

// Nothing expired (or the store is unavailable): no 0x1419, because the 2012
// client opens the "Süre Dolumu" message box for every 0x1419, even an empty one.
func TestDeletedItems2012NoReplyWhenNothingExpired(t *testing.T) {
	assertNoReply(t, newFailSoftHarness(t, &fakeUsers{}), RequestDeletedItems)
	assertNoReply(t, newFailSoftHarness(t, newUnavailableUserStore()), RequestDeletedItems)
}
