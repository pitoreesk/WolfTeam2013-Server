package shopdata

import (
	"encoding/binary"
	"os"
	"testing"
)

func testRecord(itemID uint32, family, listed, mode byte) []byte {
	record := make([]byte, RecordSize)
	binary.LittleEndian.PutUint32(record[4:8], itemID)
	record[8], record[9], record[13] = family, listed, mode
	return record
}

func putU32(record []byte, offset int, value uint32) {
	binary.LittleEndian.PutUint32(record[offset:offset+4], value)
}

func testSetRecord(setID uint32, components ...SetComponent) []byte {
	record := make([]byte, SetRecordSize)
	putU32(record, 0, setID)
	for index, component := range components {
		putU32(record, 4+index*8, uint32(component.ItemID))
		putU32(record, 8+index*8, uint32(component.Days))
	}
	return record
}

func TestCatalogValidatesFixedAndExplicitPurchases(t *testing.T) {
	gold := testRecord(1001, 1, 1, 1)
	putU32(gold, goldPriceOffset+2*4, 500)

	ap := testRecord(1002, 1, 1, 2)
	putU32(ap, apPriceOffset+2*4, 750)
	putU32(ap, wolfCoinRewardOffset+2*4, 75)

	apWithoutReward := testRecord(1003, 1, 1, 2)
	putU32(apWithoutReward, apPriceOffset, 125)

	quantity := testRecord(17007, 1, 1, 5)
	putU32(quantity, explicitValueOffset, 10)
	putU32(quantity, explicitAPPriceOffset, 3000)
	putU32(quantity, wolfCoinRewardOffset, 300)

	wolfCoinQuantity := testRecord(17008, 4, 1, 4)
	putU32(wolfCoinQuantity, explicitValueOffset, 50)
	putU32(wolfCoinQuantity, wolfCoinPriceOffset, 7000)

	hidden := testRecord(1004, 1, 0, 1)

	unlistedOffer := testRecord(1005, 1, 0, 1)
	putU32(unlistedOffer, goldPriceOffset, 250)

	data := append(append(append(append(append(append(gold, ap...), apWithoutReward...), quantity...), wolfCoinQuantity...), hidden...), unlistedOffer...)
	catalog, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.ItemCount() != 7 || catalog.OptionCount() != 6 {
		t.Fatalf("catalog counts = %d/%d", catalog.ItemCount(), catalog.OptionCount())
	}
	if option, err := catalog.ValidatePurchase(1001, 30, 1, 500, 0); err != nil || option.Currency != goldCurrency || option.PricingMode != 1 {
		t.Fatalf("gold option = %+v, %v", option, err)
	}
	if option, err := catalog.ValidateGift(1002, 30, 750, 75); err != nil || option.Currency != cashCurrency {
		t.Fatalf("AP gift option = %+v, %v", option, err)
	}
	if option, err := catalog.ValidateGift(1003, 1, 125, 0); err != nil || option.Currency != cashCurrency {
		t.Fatalf("AP gift without reward = %+v, %v", option, err)
	}
	if option, err := catalog.ValidatePurchase(17007, 10, 1, 3000, 300); err != nil || option.Currency != cashCurrency || option.PricingMode != 5 {
		t.Fatalf("quantity option = %+v, %v", option, err)
	}
	if option, err := catalog.ValidatePurchase(17008, 50, 4, 7000, 0); err != nil || option.Currency != wolfCoinCurrency || option.ShopFamily != 4 || option.PricingMode != 4 {
		t.Fatalf("WolfCoin quantity option = %+v, %v", option, err)
	}
	if _, err := catalog.Resolve(1004, 1); err == nil {
		t.Fatal("hidden shop item without offers was accepted")
	}
	// Unlisted records that still carry offers stay purchasable so client/catalog
	// tab variations still validate against their recorded price.
	if option, err := catalog.Resolve(1005, 1); err != nil || option.Price != 250 {
		t.Fatalf("unlisted priced option = %+v, %v", option, err)
	}
}

func TestCatalogRejectsTamperedPurchaseFields(t *testing.T) {
	record := testRecord(1001, 1, 1, 1)
	putU32(record, goldPriceOffset+2*4, 500)
	catalog, err := Parse(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		days   int
		mode   int
		price  uint32
		reward uint32
	}{
		{name: "duration", days: 7, mode: 1, price: 500},
		{name: "shop mode", days: 30, mode: 4, price: 500},
		{name: "price", days: 30, mode: 1, price: 499},
		{name: "reward", days: 30, mode: 1, price: 500, reward: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := catalog.ValidatePurchase(1001, test.days, test.mode, test.price, test.reward); err == nil {
				t.Fatal("tampered purchase was accepted")
			}
		})
	}
	if _, err := catalog.ValidateGift(1001, 30, 500, 1); err == nil {
		t.Fatal("tampered gift reward was accepted")
	}
}

func TestCatalogExpandsEveryDefinedSetItemRegardlessOfShopCategory(t *testing.T) {
	set := testRecord(14007, 4, 1, 1)
	set[detailedCategoryOffset] = setDetailedCategory
	putU32(set, wolfCoinPriceOffset+2*4, 29800)
	pack := testRecord(14133, 4, 1, 1)
	pack[detailedCategoryOffset] = 17
	putU32(pack, wolfCoinPriceOffset+2*4, 12000)
	powerUser := testRecord(14032, 1, 1, 2)
	powerUser[detailedCategoryOffset] = 15
	putU32(powerUser, apPriceOffset+2*4, 900)
	ordinary := testRecord(12078, 1, 1, 1)
	putU32(ordinary, goldPriceOffset+2*4, 500)
	data := append(append(append(set, pack...), powerUser...), ordinary...)
	catalog, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Resolve(14007, 30); err == nil {
		t.Fatal("set without setitemdata components was accepted")
	}
	setData := append(
		testSetRecord(14007, SetComponent{ItemID: 1009, Days: 30}, SetComponent{ItemID: 3035, Days: 30}),
		testSetRecord(14133, SetComponent{ItemID: 12150, Days: 30}, SetComponent{ItemID: 20026, Days: 30})...,
	)
	setData = append(setData,
		testSetRecord(14032, SetComponent{ItemID: 10027, Days: 30}, SetComponent{ItemID: 12031, Days: 7})...)
	if err := catalog.AttachSetData(setData); err != nil {
		t.Fatal(err)
	}
	option, err := catalog.ValidatePurchase(14007, 30, 4, 29800, 0)
	if err != nil || len(option.Components) != 2 || option.Components[0] != (SetComponent{ItemID: 1009, Days: 30}) {
		t.Fatalf("set option = %+v, %v", option, err)
	}
	packageOption, err := catalog.ValidatePurchase(14133, 30, 4, 12000, 0)
	if err != nil || len(packageOption.Components) != 2 || packageOption.Components[0].ItemID != 12150 {
		t.Fatalf("package option = %+v, %v", packageOption, err)
	}
	powerOption, err := catalog.ValidatePurchase(14032, 30, 1, 900, 0)
	if err != nil || len(powerOption.Components) != 2 || powerOption.Components[0] != (SetComponent{ItemID: 10027, Days: 30}) ||
		powerOption.Components[1] != (SetComponent{ItemID: 12031, Days: 7}) {
		t.Fatalf("Power User Package option = %+v, %v", powerOption, err)
	}
	ordinaryOption, err := catalog.ValidatePurchase(12078, 30, 1, 500, 0)
	if err != nil || len(ordinaryOption.Components) != 0 {
		t.Fatalf("ordinary item option = %+v, %v", ordinaryOption, err)
	}
}

func TestCatalogRejectsMalformedData(t *testing.T) {
	if _, err := Parse(make([]byte, RecordSize-1)); err == nil {
		t.Fatal("misaligned shopdata was accepted")
	}
	first := testRecord(1001, 1, 1, 1)
	second := testRecord(1001, 1, 1, 2)
	if _, err := Parse(append(first, second...)); err == nil {
		t.Fatal("duplicate item IDs were accepted")
	}
	catalog, err := Parse(first)
	if err != nil {
		t.Fatal(err)
	}
	malformedSet := testSetRecord(14007)
	putU32(malformedSet, 8, 30)
	if err := catalog.AttachSetData(malformedSet); err == nil {
		t.Fatal("set duration without a component item was accepted")
	}
}

func TestExternalCatalogWhenConfigured(t *testing.T) {
	path := os.Getenv("WT_TEST_SHOPDATA_PATH")
	if path == "" {
		t.Skip("WT_TEST_SHOPDATA_PATH is not set")
	}
	catalog, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.ItemCount() < 1 || catalog.OptionCount() < 1 {
		t.Fatalf("external catalog counts = %d/%d", catalog.ItemCount(), catalog.OptionCount())
	}
	packageOption, err := catalog.Resolve(14133, 30)
	if err != nil || len(packageOption.Components) != 3 || packageOption.Components[0].ItemID != 12150 {
		t.Fatalf("external package components = %+v, %v", packageOption.Components, err)
	}
}
