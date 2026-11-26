package game

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"

	"wtserver-go/internal/shopdata"
	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

type giftLookupUsers struct {
	*fakeUsers
	identity  store.BuddyIdentity
	found     bool
	requested string
	sent      store.GiftSendRequest
}

func (u *giftLookupUsers) BuddyIdentityByCallsign(_ context.Context, callsign string) (store.BuddyIdentity, bool, error) {
	u.requested = callsign
	return u.identity, u.found, nil
}

func (u *giftLookupUsers) SendGift(_ context.Context, request store.GiftSendRequest) (store.GiftSendResult, error) {
	u.sent = request
	return store.GiftSendResult{Success: true, Gold: 90, WC: 200, Cash: 250}, nil
}

func testGiftSendPacket() []byte {
	payload := []byte{0x2e, 0x2f, 30, 0, 12, 'T', 0, 'a', 0, 'r', 0, 'g', 0, 'e', 0, 't', 0, 4, 'H', 0, 'i', 0, 0, 0xf4, 1, 0, 0, 0, 0, 0, 0}
	return append(payload, make([]byte, 48-len(payload))...)
}

func TestGiftPacketLayoutsMatchProtocol(t *testing.T) {
	parsedSend, err := ParseGiftSendRequest(testGiftSendPacket())
	if err != nil || parsedSend.ItemID != 12078 || parsedSend.Days != 30 || parsedSend.ReceiverCallsign != "Target" ||
		parsedSend.Message != "Hi" || parsedSend.ItemType != 0 || parsedSend.Price != 500 || parsedSend.WolfCoinReward != 0 {
		t.Fatalf("send gift request = %+v, %v", parsedSend, err)
	}
	if currency, price, err := GiftSendPayment(parsedSend); err != nil || currency != 4 || price != 500 {
		t.Fatalf("gold gift payment = %d/%d, %v", currency, price, err)
	}
	apSend := parsedSend
	apSend.Price, apSend.WolfCoinReward = 750, 75
	if currency, price, err := GiftSendPayment(apSend); err != nil || currency != 1 || price != 750 {
		t.Fatalf("AP gift payment = %d/%d, %v", currency, price, err)
	}
	sendReply := SendGiftPayload(0, store.GiftSendResult{Success: true, Gold: 90, Cash: 250})
	if len(sendReply) != 9 || sendReply[0] != 0 || binary.LittleEndian.Uint32(sendReply[1:5]) != 90 || binary.LittleEndian.Uint32(sendReply[5:9]) != 250 {
		t.Fatalf("send gift reply = %x", sendReply)
	}

	request, err := ParseGiftListRequest([]byte{0, GiftPageSize, 2})
	if err != nil || request.Kind != 0 || request.PageSize != GiftPageSize || request.Page != 2 {
		t.Fatalf("gift list request = %+v, %v", request, err)
	}
	gift := store.Gift{
		ID: 0x11223344, Status: 1, ItemID: 12078, Days: 30, InboxDays: 90,
		Counterparty: "A", Message: "Hi",
		CreatedAt: time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC),
	}
	list, err := GiftItemListPayload(request, store.GiftPage{Gifts: []store.Gift{gift}, Total: 1, TotalPages: 1})
	if err != nil {
		t.Fatal(err)
	}
	expectedList := []byte{0, 0, 2, 1, 1, 0, 1, 0x44, 0x33, 0x22, 0x11, 0x2e, 0x2f, 90, 0, 2, 'A', 0, 1, 8}
	expectedList = append(expectedList, []byte("20260718")...)
	if !bytes.Equal(list, expectedList) {
		t.Fatalf("gift list = %x, want %x", list, expectedList)
	}

	detailRequest, err := ParseGiftDetailRequest([]byte{0x44, 0x33, 0x22, 0x11, 0})
	if err != nil || detailRequest.GiftID != gift.ID || detailRequest.Kind != 0 {
		t.Fatalf("gift detail request = %+v, %v", detailRequest, err)
	}
	detail, err := GiftDetailPayload(0, gift, true)
	if err != nil {
		t.Fatal(err)
	}
	if detail[0] != 0 || detail[1] != 0 || binary.LittleEndian.Uint32(detail[2:6]) != gift.ID ||
		binary.LittleEndian.Uint16(detail[6:8]) != 12078 || binary.LittleEndian.Uint16(detail[8:10]) != 30 ||
		detail[10] != 1 || detail[11] != 2 || !bytes.Equal(detail[12:14], []byte{'A', 0}) ||
		detail[14] != 8 || !bytes.Equal(detail[15:23], []byte("20260718")) ||
		detail[23] != 4 || !bytes.Equal(detail[24:28], []byte{'H', 0, 'i', 0}) {
		t.Fatalf("gift detail = %x", detail)
	}
	if tail := detail[len(detail)-8:]; !bytes.Equal(tail, make([]byte, 8)) {
		t.Fatalf("gift detail without conversion value = %x", tail)
	}
	convertible, err := GiftDetailPayload(0, gift, true, 0, 1500)
	if err != nil {
		t.Fatal(err)
	}
	if len(convertible) != len(detail) || binary.LittleEndian.Uint32(convertible[len(convertible)-8:]) != 0 ||
		binary.LittleEndian.Uint32(convertible[len(convertible)-4:]) != 1500 {
		t.Fatalf("convertible gift detail = %x", convertible)
	}
	if sent, _ := GiftDetailPayload(1, gift, true, 10, 20); !bytes.Equal(sent[len(sent)-8:], make([]byte, 8)) {
		t.Fatalf("sent gift must not be convertible: %x", sent)
	}
	if got := GiftRefundReplyPayload(7, 9); !bytes.Equal(got, []byte{0, 7, 0, 0, 0, 9, 0, 0, 0}) {
		t.Fatalf("gift refund reply = %x", got)
	}
	quantityGift := gift
	quantityGift.ItemID = 15005
	quantityGift.Days = 0
	quantityGift.Quantity = 7
	quantityDetail, err := GiftDetailPayload(0, quantityGift, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint16(quantityDetail[8:10]); got != 7 {
		t.Fatalf("quantity gift detail count = %d, want 7; payload = %x", got, quantityDetail)
	}
	legacyQuantityGift := quantityGift
	legacyQuantityGift.Quantity = 0
	legacyQuantityDetail, err := GiftDetailPayload(0, legacyQuantityGift, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint16(legacyQuantityDetail[8:10]); got != 1 {
		t.Fatalf("legacy quantity gift detail count = %d, want 1; payload = %x", got, legacyQuantityDetail)
	}

	claimID, err := ParseGiftClaimRequest([]byte{0x44, 0x33, 0x22, 0x11})
	if err != nil || claimID != gift.ID {
		t.Fatalf("gift claim request = %x, %v", claimID, err)
	}
	use := GiftUsePayload(store.GiftClaimResult{Success: true, Gift: gift, Expiry: "20300102030405", Gold: 100, Cash: 300})
	if use[0] != 0 || binary.LittleEndian.Uint32(use[1:5]) != 100 || binary.LittleEndian.Uint32(use[5:9]) != 300 ||
		binary.LittleEndian.Uint32(use[9:13]) != 2_000_012_078 || binary.LittleEndian.Uint16(use[13:15]) != 12078 ||
		use[15] != 14 || !bytes.Equal(use[16:30], []byte("20300102030405")) ||
		binary.LittleEndian.Uint16(use[30:32]) != 30 || use[33] != 2 {
		t.Fatalf("gift-use reply = %x", use)
	}
	if failed := GiftUsePayload(store.GiftClaimResult{Gold: 100, Cash: 300}); len(failed) != 21 || failed[0] != 0 || !bytes.Equal(failed[9:], make([]byte, 12)) {
		t.Fatalf("failed gift-use reply = %x", failed)
	}
	quantityUse := GiftUsePayload(store.GiftClaimResult{
		Success: true,
		Gift:    store.Gift{ItemID: 15005, ItemType: 0, Quantity: 7},
		Expiry:  unlimitedExpiryText,
	})
	if binary.LittleEndian.Uint16(quantityUse[30:32]) != 7 {
		t.Fatalf("quantity gift-use count = %d, want 7; payload = %x",
			binary.LittleEndian.Uint16(quantityUse[30:32]), quantityUse)
	}
	defaultQuantityUse := GiftUsePayload(store.GiftClaimResult{
		Success: true,
		Gift:    store.Gift{ItemID: 15005, ItemType: 0},
		Expiry:  unlimitedExpiryText,
	})
	if binary.LittleEndian.Uint16(defaultQuantityUse[30:32]) != 1 {
		t.Fatalf("legacy quantity gift-use count = %d, want 1; payload = %x",
			binary.LittleEndian.Uint16(defaultQuantityUse[30:32]), defaultQuantityUse)
	}

	receiver, err := ParseGiftReceiverCheckRequest([]byte{12, 'T', 0, 'a', 0, 'r', 0, 'g', 0, 'e', 0, 't', 0})
	if err != nil || receiver != "Target" {
		t.Fatalf("gift receiver check = %q, %v", receiver, err)
	}
	if !bytes.Equal(GiftReceiverCheckPayload(true), []byte{0, 1}) || !bytes.Equal(GiftReceiverCheckPayload(false), []byte{0, 0}) {
		t.Fatal("gift receiver reply booleans do not match the protocol")
	}
	if periodEnd := GiftPeriodEndPayload(); !bytes.Equal(periodEnd, []byte{0, 0, 0, 0}) {
		t.Fatalf("gift period-end reply = %x", periodEnd)
	}
	if _, err := ParseGiftReceiverCheckRequest([]byte{3, 'B', 0, 'a'}); err == nil {
		t.Fatal("odd gift receiver UTF-16 length was accepted")
	}
}

func TestGiftReceiveDetailAndClaimLifecycle(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	defer serverNet.Close()
	defer clientNet.Close()
	_ = clientNet.SetDeadline(time.Now().Add(3 * time.Second))
	loopback := netip.MustParseAddr("127.0.0.1")
	users := &giftLookupUsers{
		fakeUsers: &fakeUsers{gifts: []store.Gift{{
			ID: 7, ItemID: 12078, Days: 30, InboxDays: 90, Counterparty: "Sender", Message: "Enjoy",
			CreatedAt: time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC),
		}}},
		identity: store.BuddyIdentity{Username: "target_user", Callsign: "Target"},
		found:    true,
	}
	server := NewServer("unused", 40707, 40708, loopback, loopback, users, slog.New(slog.NewTextHandler(io.Discard, nil)))
	catalogRecord := make([]byte, shopdata.RecordSize)
	binary.LittleEndian.PutUint32(catalogRecord[4:8], 12078)
	catalogRecord[8], catalogRecord[9], catalogRecord[13] = 1, 1, 1
	binary.LittleEndian.PutUint32(catalogRecord[0x49:0x4d], 500) // 30-day Gold slot
	catalog, err := shopdata.Parse(catalogRecord)
	if err != nil {
		t.Fatal(err)
	}
	server.ConfigureShopCatalog(catalog)
	session, err := newSession(transport.NewEncryptedConn(serverNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	session.setAuthenticated("recipient", 42, "Recipient", 7)
	client := transport.NewEncryptedConn(clientNet, nil)

	invoke := func(messageID, replyID uint16, payload []byte) transport.Frame {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			done <- server.handleMessage(context.Background(), session, transport.Frame{MessageID: messageID, Payload: payload})
		}()
		frame := assertFrameID(t, client, replyID)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		return frame
	}

	receiverCheck := invoke(RequestGiftNickCheck, ReplyGiftNickCheck, []byte{12, 'T', 0, 'a', 0, 'r', 0, 'g', 0, 'e', 0, 't', 0})
	if receiverCheck.Payload[0] != 0 || receiverCheck.Payload[1] != 1 || users.requested != "Target" {
		t.Fatalf("gift receiver check = %x, requested %q", receiverCheck.Payload, users.requested)
	}
	send := invoke(RequestSendGift, ReplySendGift, testGiftSendPacket())
	if send.Payload[0] != 0 || binary.LittleEndian.Uint32(send.Payload[1:5]) != 90 ||
		binary.LittleEndian.Uint32(send.Payload[5:9]) != 250 || users.sent.ReceiverCallsign != "Target" ||
		users.sent.Currency != 4 || users.sent.Price != 500 || users.sent.ItemType != 0 {
		t.Fatalf("send gift = %x, request %+v", send.Payload, users.sent)
	}

	count := invoke(RequestGiftItemCount, ReplyGiftItemCount, nil)
	if count.Payload[0] != 0 || binary.LittleEndian.Uint16(count.Payload[1:3]) != GiftMailboxCapacity || binary.LittleEndian.Uint16(count.Payload[3:5]) != 1 {
		t.Fatalf("gift count = %x", count.Payload)
	}
	periodEnd := invoke(RequestGiftPeriodEnd, ReplyGiftPeriodEnd, nil)
	if len(periodEnd.Payload) < 4 ||
		!bytes.Equal(periodEnd.Payload[:4], []byte{0, 0, 0, 0}) ||
		!bytes.Equal(periodEnd.Payload[4:], make([]byte, len(periodEnd.Payload)-4)) {
		t.Fatalf("gift period-end = %x", periodEnd.Payload)
	}
	list := invoke(RequestGiftItemList, ReplyGiftItemList, []byte{0, GiftPageSize, 0})
	if list.Payload[6] != 1 || binary.LittleEndian.Uint32(list.Payload[7:11]) != 7 {
		t.Fatalf("gift list = %x", list.Payload)
	}
	detail := invoke(RequestGiftDetail, ReplyGiftDetail, []byte{7, 0, 0, 0, 0})
	if detail.Payload[10] != 1 {
		t.Fatalf("gift was not marked read = %x", detail.Payload)
	}
	use := invoke(RequestGiftUse, ReplyGiftUse, []byte{7, 0, 0, 0})
	if users.claimedGift != 7 || binary.LittleEndian.Uint16(use.Payload[13:15]) != 12078 {
		t.Fatalf("gift claim = %x, claimed %d", use.Payload, users.claimedGift)
	}
	replay := invoke(RequestGiftUse, ReplyGiftUse, []byte{7, 0, 0, 0})
	if binary.LittleEndian.Uint32(replay.Payload[9:13]) != 0 {
		t.Fatalf("gift replay granted an item = %x", replay.Payload)
	}
}
