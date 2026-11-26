package game

import (
	"testing"

	"wtserver-go/internal/shopdata"
)

func TestNearestShopOptionProRatesUnlistedDurations(t *testing.T) {
	options := []shopdata.Offer{{Days: 1}, {Days: 7}, {Days: 30}}
	for _, testCase := range []struct{ value, want int }{
		{7, 7}, {3, 7}, {45, 30}, {1, 1},
	} {
		got, ok := nearestShopOption(options, testCase.value)
		if !ok || got.Days != testCase.want {
			t.Fatalf("value %d picked %d, want %d", testCase.value, got.Days, testCase.want)
		}
	}
	if _, ok := nearestShopOption(nil, 3); ok {
		t.Fatal("an item without options was priced")
	}
}
