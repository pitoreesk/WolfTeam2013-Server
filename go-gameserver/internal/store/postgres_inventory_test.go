package store

import "testing"

func TestPurchaseQuantityValidation(t *testing.T) {
	tests := []struct {
		name    string
		request PurchaseOrder
		want    int
		valid   bool
	}{
		{name: "legacy single", request: PurchaseOrder{}, want: 1, valid: true},
		{name: "quantity", request: PurchaseOrder{Quantity: 50}, want: 50, valid: true},
		{name: "timed quantity", request: PurchaseOrder{Days: 30, Quantity: 2}, want: 2, valid: false},
		{name: "set quantity", request: PurchaseOrder{Quantity: 2, Components: []PurchaseComponent{{ItemID: 1001}}}, want: 2, valid: false},
		{name: "too large", request: PurchaseOrder{Quantity: maxInventoryQuantity + 1}, want: maxInventoryQuantity + 1, valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, valid := purchaseQuantity(test.request)
			if got != test.want || valid != test.valid {
				t.Fatalf("purchaseQuantity() = %d/%v, want %d/%v", got, valid, test.want, test.valid)
			}
		})
	}
}

func TestCouponCreditForOriginalClientItemIDs(t *testing.T) {
	tests := []struct {
		itemID int
		want   couponCredit
		ok     bool
	}{
		{itemID: 15003, want: couponCredit{Gold: 30_000}, ok: true},
		{itemID: 15005, want: couponCredit{Gold: 100_000}, ok: true},
		{itemID: 15030, want: couponCredit{WC: 100_000}, ok: true},
		{itemID: 15037, want: couponCredit{WC: 5_000}, ok: true},
		{itemID: 7778},
	}
	for _, test := range tests {
		got, ok := couponCreditForItem(test.itemID)
		if got != test.want || ok != test.ok {
			t.Errorf("couponCreditForItem(%d) = %+v/%v, want %+v/%v", test.itemID, got, ok, test.want, test.ok)
		}
	}
}
