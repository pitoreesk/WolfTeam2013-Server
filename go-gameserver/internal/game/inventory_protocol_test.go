package game

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	"wtserver-go/internal/store"
)

func TestInventoryFilteringAndUniqueIDs(t *testing.T) {
	items := []store.Item{{ID: 1001, Type: 0, Upgrade: 0x0004000300020001, Expiry: "permanent"}, {ID: 30005, Type: 1, Expiry: "2030-01-02 03:04:05"}}
	weapons, err := ItemListPayload(items)
	if err != nil {
		t.Fatal(err)
	}
	if weapons[0] != 0 || weapons[1] != 1 || binary.LittleEndian.Uint32(weapons[2:6]) != 2_000_001_001 || weapons[len(weapons)-1] != 0 {
		t.Fatalf("weapon payload = %x", weapons)
	}
	for slot, want := range []uint16{1, 2, 3, 4} {
		if got := binary.LittleEndian.Uint16(weapons[8+slot*2 : 10+slot*2]); got != want {
			t.Fatalf("upgrade slot %d = %d, want %d", slot, got, want)
		}
	}
	characters, err := CharacterItemListPayload(items)
	if err != nil {
		t.Fatal(err)
	}
	if characters[0] != 0 || binary.LittleEndian.Uint32(characters[1:5]) != 1 || binary.LittleEndian.Uint32(characters[5:9]) != 1 || binary.LittleEndian.Uint32(characters[9:13]) != 2_000_030_005 {
		t.Fatalf("character payload = %x", characters)
	}
}

func TestExpiryStampCanonicalization(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "compact", input: "20300102030405", want: "20300102030405"},
		{name: "formatted", input: "2030-01-02 03:04:05", want: "20300102030405"},
		{name: "permanent", input: "permanent", want: unlimitedExpiryText},
		{name: "too short", input: "20300102", want: unlimitedExpiryText},
		{name: "non-ASCII digit", input: "2030010203040٥", want: unlimitedExpiryText},
		{name: "too many digits", input: "203001020304050", want: unlimitedExpiryText},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := string(expiryStampFrom(test.input)); got != test.want {
				t.Fatalf("expiryStampFrom(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestPowerRoomItemsArePartOfAuthoritativeInventorySnapshot(t *testing.T) {
	temporary := []PowerRoomItem{{UniqueID: 2_000_003_005, ItemID: 3005}}
	payload, err := ItemListPayloadWithPowerRoomItems(nil, temporary)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != 22 || payload[0] != 0 || payload[1] != 1 ||
		binary.LittleEndian.Uint32(payload[2:6]) != temporary[0].UniqueID ||
		binary.LittleEndian.Uint16(payload[6:8]) != temporary[0].ItemID || payload[16] != 0 ||
		binary.LittleEndian.Uint32(payload[17:21]) != 0x0002ffff || payload[21] != 0 {
		t.Fatalf("Power Room inventory payload = %x", payload)
	}
}

func TestCurrencyRefreshCarriesProgressionFields(t *testing.T) {
	payload := CurrencyPayload(store.AccountSnapshot{Level: 34, GP: 999999, Gold: 1234, WC: 5678, Cash: 9012})
	if len(payload) != 26 || payload[0] != 0 ||
		binary.LittleEndian.Uint32(payload[1:5]) != 1234 ||
		binary.LittleEndian.Uint32(payload[5:9]) != 5678 ||
		binary.LittleEndian.Uint32(payload[9:13]) != 9012 ||
		binary.LittleEndian.Uint32(payload[13:17]) != 999999 ||
		binary.LittleEndian.Uint32(payload[17:21]) != uint32(store.LevelBaseThreshold(34)) ||
		binary.LittleEndian.Uint32(payload[21:25]) != 389000 || payload[25] != 34 {
		t.Fatalf("currency payload = %x", payload)
	}
}

func TestBuyItemReplyCarriesBalancesBeforeItemRecord(t *testing.T) {
	request := BuyRequest{ItemID: 30009, Days: 30, PurchaseMode: 1}
	payload := BuyItemReplyPayload(request, store.PurchaseReceipt{
		Success: true, Expiry: time.Now().UTC().Add(30 * 24 * time.Hour).Format("20060102150405"), Gold: 800, WC: 700, Cash: 600,
	}, 0, false)
	if len(payload) != 41 || payload[0] != 0 || binary.LittleEndian.Uint16(payload[1:3]) != 0 ||
		binary.LittleEndian.Uint32(payload[3:7]) != 700 ||
		binary.LittleEndian.Uint32(payload[7:11]) != 800 ||
		binary.LittleEndian.Uint32(payload[11:15]) != 600 ||
		binary.LittleEndian.Uint32(payload[15:19]) != 2_000_030_009 ||
		binary.LittleEndian.Uint16(payload[19:21]) != 30009 || payload[21] != 14 ||
		binary.LittleEndian.Uint16(payload[36:38]) != 30 || payload[38] != 0 || payload[39] != 2 || payload[40] != 0 {
		t.Fatalf("buy-item payload = %x", payload)
	}
}

func TestBuyRequestSeparatesPurchaseModeFromCurrency(t *testing.T) {
	payload := make([]byte, 14)
	binary.LittleEndian.PutUint16(payload[0:2], 3125)
	binary.LittleEndian.PutUint16(payload[2:4], 30)
	payload[4], payload[5] = 0, 1
	binary.LittleEndian.PutUint32(payload[6:10], 150000)
	request, err := ParseBuyRequest(payload)
	if err != nil {
		t.Fatal(err)
	}
	if request.ItemID != 3125 || request.Days != 30 || request.ItemType != 0 || request.PurchaseMode != 1 ||
		request.Currency != 0 || request.Price != 150000 || request.WolfCoinReward != 0 {
		t.Fatalf("buy request = %+v", request)
	}
	if currency, price, err := BuyRequestPayment(request); err != nil || currency != 4 || price != 150000 {
		t.Fatalf("fallback Gold payment = %d/%d, %v", currency, price, err)
	}
	request.WolfCoinReward = 15000
	if currency, price, err := BuyRequestPayment(request); err != nil || currency != 1 || price != 150000 {
		t.Fatalf("fallback AP payment = %d/%d, %v", currency, price, err)
	}
}

func TestCharacterItemListEncodesTimedAndPermanentUse(t *testing.T) {
	items := []store.Item{
		{ID: 30009, Type: 1, Expiry: time.Now().UTC().Add(30 * 24 * time.Hour).Format("20060102150405")},
		{ID: 30069, Type: 1, Expiry: "permanent"},
	}
	payload, err := CharacterItemListPayload(items)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != 59 || binary.LittleEndian.Uint16(payload[30:32]) != 30 || payload[32] != 0 || payload[33] != 0 {
		t.Fatalf("timed character record = %x", payload)
	}
	if binary.LittleEndian.Uint16(payload[55:57]) != 0xffff || payload[57] != 0 || payload[58] != 0 {
		t.Fatalf("permanent character record = %x", payload)
	}
}

func TestItemUpgradeAndTitlePacketLayouts(t *testing.T) {
	requestPayload := make([]byte, 7)
	binary.LittleEndian.PutUint32(requestPayload[0:4], 2_000_001_001)
	requestPayload[4] = 2
	binary.LittleEndian.PutUint16(requestPayload[5:7], 7)
	request, err := ParseItemUpgradeRequest(requestPayload, false)
	if err != nil {
		t.Fatal(err)
	}
	if request.ItemID != 1001 || request.Slot != 2 || request.UpgradeID != 7 {
		t.Fatalf("upgrade request = %+v", request)
	}
	// Upgrade IDs reach 396 (UpgradeText.dat), so slots are u16 on the wire
	// (sub_34186D30 reads 4 x u16) and in the packed store column.
	binary.LittleEndian.PutUint16(requestPayload[5:7], 0x18c)
	if wide, err := ParseItemUpgradeRequest(requestPayload, false); err != nil || wide.UpgradeID != 396 {
		t.Fatalf("upgrade ID 396 = %+v, %v", wide, err)
	}
	reply := ItemUpgradeReplyPayload(store.AccountSnapshot{Gold: 10, Cash: 20}, request, true)
	if len(reply) != 14 || reply[0] != 0 || binary.LittleEndian.Uint32(reply[1:5]) != 10 || binary.LittleEndian.Uint32(reply[9:13]) != request.UniqueID || reply[13] != 2 {
		t.Fatalf("upgrade reply = %x", reply)
	}

	titlePayload := []byte{1, 4, 'O', 0, 'K', 0}
	title, err := ParseEquipTitleRequest(titlePayload)
	if err != nil {
		t.Fatal(err)
	}
	if title.Slot != 1 || title.Title != "OK" {
		t.Fatalf("title request = %+v", title)
	}
}

func TestItemPriceVersionHasExpectedLayout(t *testing.T) {
	payload := ItemPriceVersionPayload(itemPriceVersionA, itemPriceVersionB)
	if len(payload) != 9 || payload[0] != 0 ||
		binary.LittleEndian.Uint32(payload[1:5]) != itemPriceVersionA ||
		binary.LittleEndian.Uint32(payload[5:9]) != itemPriceVersionB {
		t.Fatalf("item-price version payload = %x", payload)
	}
}

func TestSellAndRefundPacketLayouts(t *testing.T) {
	request := make([]byte, 4)
	binary.LittleEndian.PutUint32(request, 2_000_001_001)
	uniqueID, itemID, err := ItemIDFromUniquePayload(request)
	if err != nil {
		t.Fatal(err)
	}
	if itemID != 1001 {
		t.Fatalf("sold item ID = %d", itemID)
	}
	sell := SellItemReplyPayload(store.SellResult{Success: true, Gold: 25, Cash: 30}, uniqueID)
	if len(sell) != 13 || sell[0] != 0 || binary.LittleEndian.Uint32(sell[1:5]) != 25 || binary.LittleEndian.Uint32(sell[9:13]) != uniqueID {
		t.Fatalf("sell reply = %x", sell)
	}
	refund := RefundItemReplyPayload(40, 50)
	if !bytes.Equal(refund, []byte{0, 40, 0, 0, 0, 50, 0, 0, 0}) {
		t.Fatalf("refund reply = %x", refund)
	}
}

func TestPermanentTicketPacketLayouts(t *testing.T) {
	info, err := PermanentTicketInfoPayload(2_000_015_001, store.PermanentTicketInfo{
		Valid: true, TargetType: -1,
		Eligible: []store.Item{{ID: 1001, Type: 0}, {ID: 30005, Type: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(info) != 12 || info[0] != 0 || binary.LittleEndian.Uint32(info[1:5]) != 2_000_015_001 || info[5] != 0xff || info[6] != 1 || binary.LittleEndian.Uint16(info[7:9]) != 1001 || info[9] != 1 || binary.LittleEndian.Uint16(info[10:12]) != 30005 {
		t.Fatalf("permanent-ticket info = %x", info)
	}
	use := PermanentTicketUsePayload(2_000_015_001, 2_000_001_001, 1001, true)
	if len(use) != 18 || use[0] != 0 || use[1] != 2 || binary.LittleEndian.Uint32(use[2:6]) != 2_000_015_001 || use[10] != 1 || binary.LittleEndian.Uint16(use[15:17]) != 1001 || use[17] != 0 {
		t.Fatalf("permanent-ticket use = %x", use)
	}
	if failed := PermanentTicketUsePayload(0, 0, 0, false); !bytes.Equal(failed, []byte{1}) {
		t.Fatalf("failed permanent-ticket use = %x", failed)
	}
}

func TestDailyShopAndBasketPacketLayouts(t *testing.T) {
	if got := BuyNowListPayload([2]uint16{10038, 10087}); !bytes.Equal(got, []byte{0x00, 0x36, 0x27, 0x67, 0x27}) {
		t.Fatalf("daily shop = %x", got)
	}
	if got := CheckBuyNowOpenPayload(true); !bytes.Equal(got, []byte{0x00, 0x01}) {
		t.Fatalf("buy-now open = %x", got)
	}
	requestPayload := make([]byte, 25)
	requestPayload[0] = 4
	requestPayload[1] = 1
	binary.LittleEndian.PutUint16(requestPayload[2:4], 1001)
	requestPayload[4] = 30
	requestPayload[7] = 4
	binary.LittleEndian.PutUint32(requestPayload[8:12], 50)
	request, err := ParseBasketPurchaseRequest(requestPayload)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Items) != 1 || request.Items[0].ItemID != 1001 || request.Items[0].Days != 30 || request.Items[0].PurchaseMode != 4 || request.Items[0].Price != 50 || request.Items[0].WolfCoinReward != 0 {
		t.Fatalf("basket request = %+v", request)
	}
	reply := BasketPurchaseReplyPayload(request, store.BasketPurchaseResult{
		Success: true, Gold: 950, WC: 20, Cash: 30,
		Items: []store.PurchaseReceipt{{Success: true, Expiry: "20991231235959"}},
	})
	if len(reply) != 41 || reply[0] != 0 || reply[1] != 1 || reply[2] != 0 || reply[4] != 1 || binary.LittleEndian.Uint16(reply[5:7]) != 1001 || binary.LittleEndian.Uint32(reply[29:33]) != 950 {
		t.Fatalf("basket reply = %x", reply)
	}
}

func TestChangeInventoryEchoesFields(t *testing.T) {
	request := []byte{9, 8, 7, 1, 2, 3, 4, 5, 6}
	payload, err := ChangeInventoryPayload(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != 9 || payload[0] != 0 ||
		binary.LittleEndian.Uint16(payload[1:3]) != 0 ||
		binary.LittleEndian.Uint16(payload[3:5]) != 0x0605 ||
		binary.LittleEndian.Uint32(payload[5:9]) != 0x04030201 {
		t.Fatalf("change payload = %x", payload)
	}
}

func TestEventGoldHasExpectedLayout(t *testing.T) {
	payload := EventGoldPayload()
	if len(payload) != 30 || payload[0] != 0 || payload[1] != 1 ||
		binary.LittleEndian.Uint32(payload[2:6]) != 0 ||
		!bytes.Equal(payload[6:14], make([]byte, 8)) ||
		binary.LittleEndian.Uint32(payload[14:18]) != 10 ||
		!bytes.Equal(payload[18:30], make([]byte, 12)) {
		t.Fatalf("event-gold payload = %x", payload)
	}
}

func TestCharacterInfoUsesSavedFactionSlots(t *testing.T) {
	equipment := make(map[int]store.CharacterEquipment)
	equipped := store.CharacterEquipment{}
	equipped[2] = 2_000_031_267
	equipment[30069] = equipped
	payload := CharacterInfoPayload(30069, 30071, map[int]int{3: 30030, 4: 30069, 7: 30071}, equipment)
	if len(payload) != 212 || payload[0] != 0 || payload[1] != 3 {
		t.Fatalf("character-info header = %x", payload)
	}
	if payload[2] != 3 || payload[3] != 0 || binary.LittleEndian.Uint32(payload[4:8]) != 2_000_030_030 {
		t.Fatalf("inactive character record = %x", payload[2:72])
	}
	if payload[72] != 4 || payload[73] != 1 || binary.LittleEndian.Uint32(payload[74:78]) != 2_000_030_069 {
		t.Fatalf("red character record = %x", payload[72:142])
	}
	if got := binary.LittleEndian.Uint32(payload[86:90]); got != 2_000_031_267 {
		t.Fatalf("red character equipment slot 2 = %d", got)
	}
	if payload[142] != 7 || payload[143] != 2 || binary.LittleEndian.Uint32(payload[144:148]) != 2_000_030_071 {
		t.Fatalf("blue character record = %x", payload[142:])
	}
}

func TestCharacterIDsExcludeInventoryOnlyCharacterItems(t *testing.T) {
	items := []store.Item{
		{ID: 30005, Type: 1},
		{ID: 31000, Type: 1},
		{ID: 31267, Type: 1},
		{ID: 33439, Type: 1},
		{ID: 30005, Type: 1},
		{ID: 30006, Type: 0},
	}
	if got, want := characterIDs(items), []int{30005, 31000}; !reflect.DeepEqual(got, want) {
		t.Fatalf("selectable character IDs = %v, want %v", got, want)
	}
}

func TestCanonicalCharacterSlotsDoesNotRegenerateInventoryOnlyItems(t *testing.T) {
	got := canonicalCharacterSlots(
		map[int]int{1: 30005, 2: 31267},
		[]int{30005, 30006},
	)
	want := map[int]int{1: 30005, 2: 30006}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical character slots = %v, want %v", got, want)
	}
}

func TestInventoryDeduplicatesAndCountsPermanentItems(t *testing.T) {
	items := []store.Item{
		{ID: 19601, Type: 2, Expiry: "permanent", Quantity: 2},
		{ID: 30069, Type: 1, Expiry: "permanent"},
		{ID: 30069, Type: 1, Expiry: "permanent"},
	}
	weapons, err := ItemListPayload(items)
	if err != nil {
		t.Fatal(err)
	}
	if weapons[0] != 0 || weapons[1] != 1 || binary.LittleEndian.Uint32(weapons[31:35]) != 2 || weapons[len(weapons)-1] != 0 {
		t.Fatalf("deduplicated item payload = %x", weapons)
	}
	characters, err := CharacterItemListPayload(items)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(characters[5:9]) != 1 {
		t.Fatalf("deduplicated character payload = %x", characters)
	}
}

func TestGenericUseItemPacketLayoutsMatchProtocol(t *testing.T) {
	requestPayload := make([]byte, 15)
	binary.LittleEndian.PutUint32(requestPayload[0:4], 2_000_015_005)
	binary.LittleEndian.PutUint16(requestPayload[4:6], 15005)
	binary.LittleEndian.PutUint32(requestPayload[6:10], 0x12345678)
	requestPayload[10] = 4
	copy(requestPayload[11:], []byte{'O', 0, 'K', 0})

	request, err := ParseGenericUseItemRequest(requestPayload)
	if err != nil {
		t.Fatal(err)
	}
	if request.UniqueID != 2_000_015_005 || request.ItemID != 15005 ||
		request.Context != 0x12345678 || request.Text != "OK" {
		t.Fatalf("use-item request = %+v", request)
	}

	info := store.AccountSnapshot{Gold: 100, Cash: 300, WC: 200, GP: 400, Level: 10}
	reply := GenericUseItemReplyPayload(0, request, 3, info)
	if len(reply) != 43 || reply[0] != 0 {
		t.Fatalf("use-item reply header = %x", reply)
	}
	if got := binary.LittleEndian.Uint32(reply[1:5]); got != 100 {
		t.Fatalf("reply Gold = %d, want 100", got)
	}
	if got := binary.LittleEndian.Uint32(reply[5:9]); got != 300 {
		t.Fatalf("reply AP/Cash = %d, want 300", got)
	}
	if got := binary.LittleEndian.Uint32(reply[9:13]); got != 400 {
		t.Fatalf("reply GP = %d, want 400", got)
	}
	if got := binary.LittleEndian.Uint32(reply[13:17]); got != uint32(store.LevelBaseThreshold(10)) {
		t.Fatalf("reply gauge base = %d", got)
	}
	if got := binary.LittleEndian.Uint32(reply[17:21]); got != uint32(store.NextLevelThreshold(10)) {
		t.Fatalf("reply next-rank threshold = %d", got)
	}
	if reply[21] != 10 {
		t.Fatalf("reply rank = %d, want 10", reply[21])
	}
	if got := binary.LittleEndian.Uint32(reply[22:26]); got != 2_000_015_005 {
		t.Fatalf("use-item unique ID = %d", got)
	}
	if got := binary.LittleEndian.Uint16(reply[26:28]); got != 3 {
		t.Fatalf("remaining count = %d, want 3", got)
	}
	if got := binary.LittleEndian.Uint16(reply[28:30]); got != 15005 {
		t.Fatalf("reply item ID = %d, want 15005", got)
	}
	if got := binary.LittleEndian.Uint32(reply[30:34]); got != 0x12345678 {
		t.Fatalf("reply context = %#x", got)
	}
	if reply[34] != 4 || !bytes.Equal(reply[35:39], []byte{'O', 0, 'K', 0}) {
		t.Fatalf("reply popup text = %x", reply[34:39])
	}
	if got := binary.LittleEndian.Uint32(reply[39:43]); got != 200 {
		t.Fatalf("reply WC = %d, want 200", got)
	}

	failure := GenericUseItemReplyPayload(1, request, 3, info)
	if !bytes.Equal(failure, []byte{1}) {
		t.Fatalf("use-item failure = %x, want common error status", failure)
	}
}

func TestGenericUseItemRejectsMalformedTextLength(t *testing.T) {
	payload := make([]byte, 11)
	binary.LittleEndian.PutUint32(payload[0:4], 2_000_015_005)
	binary.LittleEndian.PutUint16(payload[4:6], 15005)
	payload[10] = 3
	if _, err := ParseGenericUseItemRequest(payload); err == nil {
		t.Fatal("odd or out-of-bounds UTF-16 text length must fail")
	}
}

func TestDeletedItemsPayloadIsEmptySuccessfulSync(t *testing.T) {
	payload, err := DeletedItemsPayload(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != 2 || payload[0] != 0 || payload[1] != 0 {
		t.Fatalf("deleted-item payload = %x, want success with zero records", payload)
	}

	payload, err = DeletedItemsPayload([]int{7777, 30011})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != 14 || payload[1] != 2 ||
		binary.LittleEndian.Uint32(payload[2:6]) != 2_000_007_777 || binary.LittleEndian.Uint16(payload[6:8]) != 7777 ||
		binary.LittleEndian.Uint32(payload[8:12]) != 2_000_030_011 || binary.LittleEndian.Uint16(payload[12:14]) != 30011 {
		t.Fatalf("populated deleted-item payload = %x", payload)
	}
	if _, err := DeletedItemsPayload(make([]int, 9)); err == nil {
		t.Fatal("accepted more than eight deleted-item records")
	}
}

func TestLuckyBoxPreviewCarriesOneNormalReward(t *testing.T) {
	payload := LuckyBoxPreviewPayload(2_000_017_019, 19601, 7)
	if len(payload) != 20 || payload[0] != 0 {
		t.Fatalf("lucky-box preview header = %x", payload)
	}
	if binary.LittleEndian.Uint32(payload[1:5]) != 2_000_017_019 ||
		binary.LittleEndian.Uint16(payload[5:7]) != 19601 ||
		binary.LittleEndian.Uint16(payload[7:9]) != 7 {
		t.Fatalf("lucky-box initial result = %x", payload)
	}
	if binary.LittleEndian.Uint16(payload[11:13]) != 0 ||
		binary.LittleEndian.Uint16(payload[13:15]) != 0 || payload[15] != 0 {
		t.Fatalf("normal opening contains jackpot fields = %x", payload)
	}
	if binary.LittleEndian.Uint32(payload[16:20]) != 100 {
		t.Fatalf("lucky-box fullmoon probability source = %x", payload[16:20])
	}
}

func TestLuckyBoxTakeHasExpectedLength(t *testing.T) {
	payload := LuckyBoxTakePayload(2_000_017_019, 3, 19601, 7)
	if len(payload) != 17 || payload[0] != 0 ||
		binary.LittleEndian.Uint32(payload[1:5]) != 2_000_017_019 ||
		binary.LittleEndian.Uint16(payload[5:7]) != 3 ||
		binary.LittleEndian.Uint16(payload[7:9]) != 19601 ||
		binary.LittleEndian.Uint16(payload[9:11]) != 7 ||
		!bytes.Equal(payload[11:], make([]byte, 6)) {
		t.Fatalf("lucky-box take payload = %x", payload)
	}
}

func TestInventoryStatusPayloadContainsOnlyCommonStatus(t *testing.T) {
	if got := InventoryStatusPayload(0); !bytes.Equal(got, []byte{0}) {
		t.Fatalf("inventory success status = %x", got)
	}
	if got := InventoryStatusPayload(1); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("inventory failure status = %x", got)
	}
}

func TestItemListAppendsOnlyNewCharacters(t *testing.T) {
	before := []store.Item{{ID: 1001}, {ID: 31010, Type: 1, Expiry: "permanent"}}
	after := append(before, store.Item{ID: 31021, Type: 1, Expiry: "permanent"},
		store.Item{ID: 31021, Type: 1, Expiry: "permanent"}, store.Item{ID: 30002, Type: 1, Expiry: "permanent"})
	added := newCharacterItems(before, after, map[int]bool{30002: true})
	if len(added) != 1 || added[0].ID != 31021 {
		t.Fatalf("added characters = %+v", added)
	}
	if newCharacterItems(nil, after, nil) != nil {
		t.Fatal("unknown previous inventory must not append characters")
	}
	payload, err := ItemListPayloadWithNewCharacters(after, added)
	if err != nil {
		t.Fatal(err)
	}
	// status, count 2, weapon record, character record, trailing flag.
	if payload[1] != 2 || len(payload) != 2+2*33+1 {
		t.Fatalf("payload = %x", payload)
	}
	character := payload[2+33:]
	if binary.LittleEndian.Uint16(character[4:6]) != 31021 || !bytes.Equal(character[6:14], make([]byte, 8)) {
		t.Fatalf("character record = %x", character)
	}
}
