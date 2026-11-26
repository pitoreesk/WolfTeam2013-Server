package game

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"wtserver-go/internal/shopdata"
)

func loadCatalog2012(t *testing.T) *shopdata.Catalog {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "shopdata.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skip("2012 shopdata.dat not present")
	}
	catalog, err := shopdata.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestWPack2012CatalogAndPackets(t *testing.T) {
	catalog := loadCatalog2012(t)
	// Disabled packs (no contents) are hidden and must not be sold as the bare pack record.
	if _, err := catalog.ValidatePurchase(14901, 1, 1, 1000, 100); err == nil {
		t.Fatal("W-Pack without contents was purchasable")
	}
	if overrides := catalog.WPackPriceOverrides(); len(overrides) != 0 {
		t.Fatalf("W-Pack without contents has ITEMPRICE overrides: %+v", overrides)
	}
	if a, b := ItemPriceOverrideVersions(nil); a != 0 || b != 0 {
		t.Fatalf("empty override versions = %d/%d", a, b)
	}

	contents := map[int][]shopdata.SetComponent{14901: {{ItemID: 3137, Days: 7}, {ItemID: 10017, Days: 7}}}
	if err := catalog.AttachWPackContents(contents); err != nil {
		t.Fatal(err)
	}
	if err := catalog.AttachWPackContents(map[int][]shopdata.SetComponent{14902: {{ItemID: 14001, Days: 7}}}); err == nil {
		t.Fatal("a set was accepted as a W-Pack component")
	}

	// 0x1463 (sub_341C1F10): u8 count, count x {u16 item, u16 days}.
	var rows []WPackSubItem
	for _, component := range catalog.WPackComponents(14901) {
		rows = append(rows, WPackSubItem{ItemID: uint16(component.ItemID), Value: uint16(component.Days)})
	}
	payload, err := WPackSubItemsPayload(rows)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := hex.DecodeString("0002" + "410c0700" + "21270700"); !bytes.Equal(payload, want) {
		t.Fatalf("W-Pack sub items = %x", payload)
	}

	// Buying the pack grants its components (1 day AP slot).
	option, err := catalog.Resolve(14901, 1)
	if err != nil || len(option.Components) != 2 || option.Price != 1000 {
		t.Fatalf("pack option = %+v, %v", option, err)
	}

	// 0x143F entry that marks 14901 on sale (docs/protocol/wpack_2012.md §1.4).
	overrides := catalog.WPackPriceOverrides()
	if len(overrides) != 1 {
		t.Fatalf("overrides = %+v", overrides)
	}
	want, _ := hex.DecodeString("353a82" + "01010000" + "0101" + "e8030000" + "64000000" + "0000")
	if got := itemPriceOverrideEntries(overrides); !bytes.Equal(got, want) {
		t.Fatalf("override entry = %x, want %x", got, want)
	}
	info := ItemPriceInfoPayload(overrides)
	r := newReader2012(t, info)
	versionA, versionB := r.u32(), r.u32()
	if versionA == 0 || versionB == 0 || r.u8() != 1 {
		t.Fatalf("price info header = %x", info)
	}
	r.need(len(want))
	r.done()
	if a, b := ItemPriceOverrideVersions(overrides); a != versionA || b != versionB {
		t.Fatal("0x1441 and 0x143F versions differ")
	}
}
