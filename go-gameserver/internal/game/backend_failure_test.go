package game

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"testing"
	"time"

	"wtserver-go/internal/shopdata"
	"wtserver-go/internal/store"
	"wtserver-go/internal/testsupport"
	"wtserver-go/internal/transport"
)

var errBackendUnavailable = errors.New("backend unavailable")

type unavailableUserStore struct {
	testsupport.GameRepository
	equipmentAttempts int
}

func newUnavailableUserStore() *unavailableUserStore {
	return &unavailableUserStore{GameRepository: testsupport.GameRepository{Err: errBackendUnavailable}}
}

func (s *unavailableUserStore) SetEquipment(context.Context, string, int, int) error {
	s.equipmentAttempts++
	return errBackendUnavailable
}

type failSoftHarness struct {
	server  *Server
	session *Session
	client  *transport.EncryptedConn
}

func newFailSoftHarness(t *testing.T, users store.GameRepository) failSoftHarness {
	t.Helper()
	serverNet, clientNet := net.Pipe()
	t.Cleanup(func() {
		_ = serverNet.Close()
		_ = clientNet.Close()
	})
	_ = clientNet.SetDeadline(time.Now().Add(3 * time.Second))
	loopback := netip.MustParseAddr("127.0.0.1")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer("unused", 40707, 40708, loopback, loopback, users, logger)
	session, err := newSession(transport.NewEncryptedConn(serverNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}
	session.setAuthenticated("admin", 42, "Player", 7)
	return failSoftHarness{server: server, session: session, client: transport.NewEncryptedConn(clientNet, nil)}
}

func dispatchAndRead(t *testing.T, harness failSoftHarness, request, reply uint16, payload []byte) transport.Frame {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{
			MessageID: request,
			Payload:   payload,
		})
	}()
	frame := assertFrameID(t, harness.client, reply)
	if request == RequestUserInfo {
		// 2012: the tutorial progress (0x1345) follows the login user info.
		assertFrameID(t, harness.client, ReplyTutorialClear)
	}
	if err := <-done; err != nil {
		t.Fatalf("handle request %#x: %v", request, err)
	}
	return frame
}

// dispatchAndReadWithFollowUp is dispatchAndRead for replies that are
// followed by one more pushed frame (e.g. 0x1431 after a set purchase).
func dispatchAndReadWithFollowUp(t *testing.T, harness failSoftHarness, request, reply, followUp uint16, payload []byte) (transport.Frame, transport.Frame) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- harness.server.handleMessage(context.Background(), harness.session, transport.Frame{
			MessageID: request,
			Payload:   payload,
		})
	}()
	frame := assertFrameID(t, harness.client, reply)
	next := assertFrameID(t, harness.client, followUp)
	if err := <-done; err != nil {
		t.Fatalf("handle request %#x: %v", request, err)
	}
	return frame, next
}

func TestWeaponUpgradeUsesInventoryUniqueIDAndPreservesBalance(t *testing.T) {
	users := &fakeUsers{}
	harness := newFailSoftHarness(t, users)
	request := make([]byte, 7)
	binary.LittleEndian.PutUint32(request[0:4], 2_000_001_001)
	request[4] = 2
	binary.LittleEndian.PutUint16(request[5:7], 7)

	frame := dispatchAndRead(t, harness, RequestUpgradeItem, ReplyUpgradeItem, request)
	if binary.LittleEndian.Uint32(frame.Payload[1:5]) != 100 ||
		binary.LittleEndian.Uint32(frame.Payload[9:13]) != 2_000_001_001 || frame.Payload[13] != 2 {
		t.Fatalf("weapon-upgrade reply = %x", frame.Payload)
	}
	users.mu.Lock()
	defer users.mu.Unlock()
	if users.upgradedItem != 1001 || users.upgradeSlot != 2 || users.upgradeID != 7 {
		t.Fatalf("persisted upgrade = item %d slot %d id %d", users.upgradedItem, users.upgradeSlot, users.upgradeID)
	}
}

func TestInvalidWeaponUpgradeDoesNotClearDisplayedBalance(t *testing.T) {
	harness := newFailSoftHarness(t, &fakeUsers{})
	frame := dispatchAndRead(t, harness, RequestUpgradeItem, ReplyUpgradeItem, []byte{0})
	if binary.LittleEndian.Uint32(frame.Payload[1:5]) != 100 {
		t.Fatalf("invalid weapon-upgrade reply cleared Gold: %x", frame.Payload)
	}
}

func TestBackendFailuresStillSendProtocolReplies(t *testing.T) {
	buy := make([]byte, 14)
	binary.LittleEndian.PutUint16(buy, 1001)
	binary.LittleEndian.PutUint16(buy[2:4], 7)
	buy[5] = 1
	binary.LittleEndian.PutUint32(buy[6:10], 100)
	item := make([]byte, 4)
	binary.LittleEndian.PutUint32(item, 2_000_001_001)
	useItem := make([]byte, 11)
	binary.LittleEndian.PutUint32(useItem[0:4], 2_000_015_005)
	binary.LittleEndian.PutUint16(useItem[4:6], 15005)
	luckyBox := make([]byte, 4)
	binary.LittleEndian.PutUint32(luckyBox, 2_000_017_006)
	packageItem := make([]byte, 11)
	binary.LittleEndian.PutUint32(packageItem[0:4], 2_000_014_132)
	binary.LittleEndian.PutUint16(packageItem[4:6], 14132)
	equipment := make([]byte, 90)
	equipment[0] = 1

	tests := []struct {
		name    string
		request uint16
		reply   uint16
		payload []byte
		inRoom  bool
	}{
		{name: "user info", request: RequestUserInfo, reply: ReplyUserInfo},
		{name: "item check", request: RequestItemCheck, reply: ReplyItemCheck},
		{name: "item list", request: RequestItemList, reply: ReplyItemList},
		{name: "character item list", request: RequestCharItemList, reply: ReplyCharItemList},
		{name: "equipment list", request: RequestEquipList, reply: ReplyEquipList},
		{name: "character info", request: RequestCharInfo, reply: ReplyCharInfo},
		{name: "currency", request: RequestGetGoldCash, reply: ReplyGetGoldCash},
		{name: "purchase", request: RequestBuyItem, reply: ReplyBuyItem, payload: buy},
		{name: "throw item", request: RequestThrowItem, reply: ReplyThrowItem, payload: item},
		{name: "use item", request: RequestUseItem, reply: ReplyUseItem, payload: useItem},
		{name: "open lucky box", request: RequestUseBigHitBox, reply: ReplyUseBigHitBox, payload: luckyBox},
		{name: "open package", request: RequestUseItem, reply: ReplyUseItem, payload: packageItem},
		{name: "save equipment", request: RequestEquip, reply: ReplyEquip, payload: equipment},
		{name: "save character slots", request: RequestCharSave, reply: ReplyCharSave, payload: []byte{0}},
		{name: "default character", request: RequestDefaultChar, reply: ReplyDefaultChar, payload: []byte{1, 2}},
		{name: "game points", request: RequestGamePoints, reply: ReplyGamePoints, inRoom: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			users := newUnavailableUserStore()
			harness := newFailSoftHarness(t, users)
			if test.inRoom {
				room := newRoom(0, RoomConfig{Capacity: 8})
				if _, ok := room.Add(harness.session); !ok {
					t.Fatal("add session to test room")
				}
				harness.server.rooms[room.Number] = room
			}
			frame := dispatchAndRead(t, harness, test.request, test.reply, test.payload)
			if test.request == RequestBuyItem && frame.Payload[0] != 1 {
				t.Fatalf("failed purchase status = %d, want 1", frame.Payload[0])
			}
			if test.request == RequestGetGoldCash && binary.LittleEndian.Uint32(frame.Payload[1:5]) != 0 {
				t.Fatalf("fallback currency payload = %x, want zero balances", frame.Payload)
			}
			if test.request == RequestEquip && users.equipmentAttempts != 16 {
				t.Fatalf("equipment persistence attempts = %d, want all 16 slots", users.equipmentAttempts)
			}
		})
	}
}

func TestAuthenticatedUserInfoFailureSendsLoginFailure(t *testing.T) {
	harness := newFailSoftHarness(t, newUnavailableUserStore())
	harness.session.setAuthenticated("", 0, "", 0)
	login := []byte{0, 0, 0, 4, 'u', 's', 'e', 'r', 4, 'p', 'a', 's', 's'}
	frame := dispatchAndRead(t, harness, RequestChannelLogin, ReplyChannelLogin, login)
	if frame.Payload[0] != 23 {
		t.Fatalf("login status = %d, want backend-failure status 23", frame.Payload[0])
	}
	if userID := binary.LittleEndian.Uint32(frame.Payload[3:7]); userID != 0 {
		t.Fatalf("login user ID = %d, want 0 after user-info failure", userID)
	}
	if _, _, userID, _ := harness.session.identity(); userID != 0 {
		t.Fatalf("session authenticated as user %d after user-info failure", userID)
	}
}

type voucherSyncFailureStore struct {
	*fakeUsers
}

type purchaseCaptureStore struct {
	*fakeUsers
	request     store.PurchaseOrder
	giftRequest store.GiftSendRequest
}

func (s *purchaseCaptureStore) CommitPurchase(_ context.Context, request store.PurchaseOrder) (store.PurchaseReceipt, error) {
	s.request = request
	return store.PurchaseReceipt{Success: true, Expiry: "20300102030405", Gold: 850000}, nil
}

func (s *purchaseCaptureStore) SendGift(_ context.Context, request store.GiftSendRequest) (store.GiftSendResult, error) {
	s.giftRequest = request
	return store.GiftSendResult{Success: true, Gold: 100, WC: 200, Cash: 300}, nil
}

func TestShopCatalogDerivesGoldCurrencyFromPricingLayout(t *testing.T) {
	users := &purchaseCaptureStore{fakeUsers: &fakeUsers{}}
	harness := newFailSoftHarness(t, users)
	record := make([]byte, shopdata.RecordSize)
	binary.LittleEndian.PutUint32(record[4:8], 3125)
	record[8], record[9], record[13] = 1, 1, 1
	binary.LittleEndian.PutUint32(record[0x49:0x4d], 150000)
	catalog, err := shopdata.Parse(record)
	if err != nil {
		t.Fatal(err)
	}
	harness.server.ConfigureShopCatalog(catalog)

	buy := make([]byte, 14)
	binary.LittleEndian.PutUint16(buy[0:2], 3125)
	binary.LittleEndian.PutUint16(buy[2:4], 30)
	buy[5] = 1 // normal-shop purchase mode, not AP currency
	binary.LittleEndian.PutUint32(buy[6:10], 150000)
	frame := dispatchAndRead(t, harness, RequestBuyItem, ReplyBuyItem, buy)
	if frame.Payload[0] != 0 {
		t.Fatalf("Gold purchase status = %d", frame.Payload[0])
	}
	if users.request.ItemID != 3125 || users.request.Days != 30 || users.request.Currency != 4 || users.request.Price != 150000 {
		t.Fatalf("authoritative purchase = %+v", users.request)
	}
}

func TestShopCatalogSeparatesQuantityFromDuration(t *testing.T) {
	users := &purchaseCaptureStore{fakeUsers: &fakeUsers{}}
	harness := newFailSoftHarness(t, users)
	record := make([]byte, shopdata.RecordSize)
	binary.LittleEndian.PutUint32(record[4:8], 15005)
	record[8], record[9], record[13] = 1, 1, 5
	binary.LittleEndian.PutUint32(record[0xa2:0xa6], 10)
	binary.LittleEndian.PutUint32(record[0xb2:0xb6], 3000)
	binary.LittleEndian.PutUint32(record[0x122:0x126], 300)
	catalog, err := shopdata.Parse(record)
	if err != nil {
		t.Fatal(err)
	}
	harness.server.ConfigureShopCatalog(catalog)

	buy := make([]byte, 14)
	binary.LittleEndian.PutUint16(buy[0:2], 15005)
	binary.LittleEndian.PutUint16(buy[2:4], 10)
	buy[5] = 1
	binary.LittleEndian.PutUint32(buy[6:10], 3000)
	binary.LittleEndian.PutUint32(buy[10:14], 300)
	frame := dispatchAndRead(t, harness, RequestBuyItem, ReplyBuyItem, buy)
	if frame.Payload[0] != 0 {
		t.Fatalf("quantity purchase status = %d", frame.Payload[0])
	}
	if users.request.ItemID != 15005 || users.request.Days != 0 || users.request.Quantity != 10 ||
		users.request.Currency != 1 || users.request.Price != 3000 {
		t.Fatalf("quantity purchase grant = %+v", users.request)
	}

	gift := make([]byte, 48)
	binary.LittleEndian.PutUint16(gift[0:2], 15005)
	binary.LittleEndian.PutUint16(gift[2:4], 10)
	gift[4], gift[5], gift[6] = 2, 'X', 0
	gift[7], gift[8] = 0, 0
	binary.LittleEndian.PutUint32(gift[9:13], 3000)
	binary.LittleEndian.PutUint32(gift[13:17], 300)
	frame = dispatchAndRead(t, harness, RequestSendGift, ReplySendGift, gift)
	if frame.Payload[0] != 0 || users.giftRequest.Days != 0 || users.giftRequest.Quantity != 10 {
		t.Fatalf("quantity gift = %x, grant %+v", frame.Payload, users.giftRequest)
	}
}

func TestShopCatalogPassesSetComponentsToPurchase(t *testing.T) {
	users := &purchaseCaptureStore{fakeUsers: &fakeUsers{}}
	harness := newFailSoftHarness(t, users)
	record := make([]byte, shopdata.RecordSize)
	binary.LittleEndian.PutUint32(record[4:8], 14007)
	record[8], record[9], record[13], record[0x11] = 4, 1, 1, 16
	binary.LittleEndian.PutUint32(record[0xf2:0xf6], 29800)
	catalog, err := shopdata.Parse(record)
	if err != nil {
		t.Fatal(err)
	}
	setData := make([]byte, shopdata.SetRecordSize)
	binary.LittleEndian.PutUint32(setData[0:4], 14007)
	binary.LittleEndian.PutUint32(setData[4:8], 1009)
	binary.LittleEndian.PutUint32(setData[8:12], 30)
	binary.LittleEndian.PutUint32(setData[12:16], 30011)
	binary.LittleEndian.PutUint32(setData[16:20], 7)
	if err := catalog.AttachSetData(setData); err != nil {
		t.Fatal(err)
	}
	harness.server.ConfigureShopCatalog(catalog)

	buy := make([]byte, 14)
	binary.LittleEndian.PutUint16(buy[0:2], 14007)
	binary.LittleEndian.PutUint16(buy[2:4], 30)
	buy[5] = 4
	binary.LittleEndian.PutUint32(buy[6:10], 29800)
	// A set grants several rows, so the inventory (0x1431) follows the ACK.
	frame, _ := dispatchAndReadWithFollowUp(t, harness, RequestBuyItem, ReplyBuyItem, ReplyItemList, buy)
	if frame.Payload[0] != 0 {
		t.Fatalf("set purchase status = %d", frame.Payload[0])
	}
	want := []store.PurchaseComponent{{ItemID: 1009, Days: 30}, {ItemID: 30011, Days: 7}}
	if !slices.Equal(users.request.Components, want) {
		t.Fatalf("set purchase components = %+v, want %+v", users.request.Components, want)
	}
	gift := make([]byte, 48)
	binary.LittleEndian.PutUint16(gift[0:2], 14007)
	binary.LittleEndian.PutUint16(gift[2:4], 30)
	gift[4], gift[5], gift[6] = 2, 'X', 0
	gift[7], gift[8] = 0, 0 // empty message, weapon item type
	binary.LittleEndian.PutUint32(gift[9:13], 29800)
	frame = dispatchAndRead(t, harness, RequestSendGift, ReplySendGift, gift)
	if frame.Payload[0] != 0 || !slices.Equal(users.giftRequest.Components, want) {
		t.Fatalf("set gift = %x, components %+v, want %+v", frame.Payload, users.giftRequest.Components, want)
	}
}

func (s *voucherSyncFailureStore) SyncSlotVouchers(context.Context, int) error {
	return errBackendUnavailable
}

func TestVoucherSyncFailureDoesNotSuppressSuccessfulPurchaseReply(t *testing.T) {
	harness := newFailSoftHarness(t, &voucherSyncFailureStore{fakeUsers: &fakeUsers{}})
	buy := make([]byte, 14)
	binary.LittleEndian.PutUint16(buy, 10050)
	binary.LittleEndian.PutUint16(buy[2:4], 1)
	buy[5] = 1
	binary.LittleEndian.PutUint32(buy[6:10], 100)
	frame := dispatchAndRead(t, harness, RequestBuyItem, ReplyBuyItem, buy)
	if frame.Payload[0] != 0 {
		t.Fatalf("purchase status = %d, want success despite voucher-sync failure", frame.Payload[0])
	}
}

func TestLuckyBoxTakeUsesClientManagedInventoryUpdate(t *testing.T) {
	harness := newFailSoftHarness(t, newUnavailableUserStore())
	harness.session.setPendingBox(2_000_017_006, 19601, 7, 3)
	frame := dispatchAndRead(t, harness, RequestGetBigHitBox, ReplyGetBigHitBox, nil)
	if binary.LittleEndian.Uint32(frame.Payload[1:5]) != 2_000_017_006 ||
		binary.LittleEndian.Uint16(frame.Payload[5:7]) != 3 ||
		binary.LittleEndian.Uint16(frame.Payload[7:9]) != 19601 ||
		binary.LittleEndian.Uint16(frame.Payload[9:11]) != 7 {
		t.Fatalf("take lucky-box payload = %x", frame.Payload)
	}
}
