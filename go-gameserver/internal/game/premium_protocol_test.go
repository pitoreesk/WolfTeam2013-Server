package game

import (
	"testing"

	"wtserver-go/internal/store"
)

// TestPremiumShop2013 translates CS_IN_PREMIUMSHOP_ITEMLIST_ACK (sub_34178E50).
func TestPremiumShop2013(t *testing.T) {
	shop := store.PremiumShop{Enabled: true, Target: 30000, Used: 31000, Items: []store.PremiumShopItem{
		{ItemID: 30069, Days: 30, Price: 15000},
		{ItemID: 7112, Days: 7, Price: 20000}, {ItemID: 7112, Days: 30, Price: 80000},
		{ItemID: 9999, Days: 30, Price: 1}, // absent from shopdata: skipped
	}}
	meta := func(id int) (byte, byte, bool) { return 1, 3, id != 9999 }
	r := newReader2012(t, PremiumShopPayload(shop, meta))
	r.need(2)
	reached, target, used, n := r.u8(), r.u32(), r.u32(), r.u8()
	if reached != 1 || used != 31000 || target != 30000 || n != 2 {
		t.Fatalf("header = %d %d %d %d", reached, used, target, n)
	}
	type row struct {
		item          uint16
		prices        [6]uint32
		fill, kind, m byte
	}
	var rows []row
	for i := byte(0); i < n; i++ {
		var x row
		x.item = r.u16()
		if a, b := r.u8(), r.u8(); a != 1 || b != 3 {
			t.Fatalf("meta = %d %d", a, b)
		}
		r.u8()
		r.u8()
		r.u32()
		r.u32()
		x.fill, x.kind, x.m = r.u8(), r.u8(), r.u8()
		for k := byte(0); k < x.m; k++ {
			r.u16()
			x.prices[k] = r.u32()
			r.u32()
		}
		rows = append(rows, x)
	}
	r.done()
	if rows[0].item != 30069 || rows[0].prices != [6]uint32{0, 0, 15000, 0, 0, 0} || rows[0].kind != 0 || rows[0].m != 6 ||
		rows[1].item != 7112 || rows[1].prices != [6]uint32{0, 20000, 80000, 0, 0, 0} {
		t.Fatalf("rows = %+v", rows)
	}
}
