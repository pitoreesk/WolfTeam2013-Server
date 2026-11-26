package shopdata

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

// The 2013 TR client (CShell "Mar 29 2013", loader sub_3406B420) reads
// 350-byte ShopData records field by field: +0 u32, +4 u32 item id, +8..+16
// u8 flags (+9 listed, +13 pricing mode), +17 u32 category, ... +65 6 x u32
// Gold, +89 u8, +90 6 x u32 AP, +114 6 x u32, +138 6 x u32, +162 4 x u32
// explicit values, +178 4 x u32 explicit AP, +194 6 x u32, +218 4 x u32,
// +234 6 x u32 WolfCoin price, +258/+274 4 x u32, +290 6 x u32 WolfCoin
// reward, +314 6 x u32, +338/+342/+346 u32 (docs/data/shopdata_2013.md).
// The 2012 client used 326-byte records without the +114 array.
const (
	RecordSize     = 350
	SetRecordSize  = 140
	setMemberSlots = 17
)

var fixedDurations = [...]int{1, 7, 30, 90, 180, 0}

const (
	goldCurrency     = 4
	cashCurrency     = 1
	wolfCoinCurrency = 2

	goldPriceOffset        = 0x41  // 65
	apPriceOffset          = 0x5a  // 90
	wolfCoinPriceOffset    = 0xea  // 234
	wolfCoinRewardOffset   = 0x122 // 290
	explicitValueOffset    = 0xa2  // 162
	explicitAPPriceOffset  = 0xb2  // 178
	detailedCategoryOffset = 0x11
	setDetailedCategory    = 16
)

// SetComponent is one inventory item contained in a setitemdata.dat record.
// Days is authoritative per component; some sets contain mixed durations.
type SetComponent struct {
	ItemID int
	Days   int
}

// Offer is one authoritative purchase option decoded from shopdata.dat.
// Days is also used for the quantity value of pricing modes 4 and 5, matching
// the legacy request field.
type Offer struct {
	ItemID         int
	Days           int
	ShopFamily     byte
	PricingMode    byte
	PurchaseMode   int
	Currency       int
	Price          uint32
	WolfCoinReward uint32
	Components     []SetComponent
}

type item struct {
	listed     bool
	isSet      bool
	options    []Offer
	components []SetComponent
	// priceMeta holds shopdata bytes +29, +25, +61 and +89, echoed by the
	// 2012 CS_IN_ITEMPRICE_INFO_ACK override entry (sub_341D25D0).
	priceMeta [4]byte
}

// Catalog is an immutable, in-memory view of the shop catalog.
type Catalog struct {
	items       map[int]item
	optionCount int
}

func Load(path string) (*Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read shopdata %q: %w", path, err)
	}
	catalog, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse shopdata %q: %w", path, err)
	}
	setPath := filepath.Join(filepath.Dir(path), "setitemdata.dat")
	setData, err := os.ReadFile(setPath)
	if err != nil {
		return nil, fmt.Errorf("read set-item data %q: %w", setPath, err)
	}
	if err := catalog.AttachSetData(setData); err != nil {
		return nil, fmt.Errorf("parse set-item data %q: %w", setPath, err)
	}
	return catalog, nil
}

func Parse(data []byte) (*Catalog, error) {
	if len(data) == 0 || len(data)%RecordSize != 0 {
		return nil, fmt.Errorf("size %d is not a nonzero multiple of %d", len(data), RecordSize)
	}
	catalog := &Catalog{items: make(map[int]item, len(data)/RecordSize)}
	for offset := 0; offset < len(data); offset += RecordSize {
		record := data[offset : offset+RecordSize]
		itemID := int(binary.LittleEndian.Uint32(record[4:8]))
		if itemID == 0 {
			continue
		}
		if _, duplicate := catalog.items[itemID]; duplicate {
			return nil, fmt.Errorf("item %d occurs more than once", itemID)
		}
		decoded := item{
			listed: record[9] == 1, isSet: record[detailedCategoryOffset] == setDetailedCategory,
			priceMeta: [4]byte{record[29], record[25], record[61], record[89]},
		}
		family, mode := record[8], record[13]
		switch {
		case family == 1 && mode == 1:
			decoded.options = fixedOptions(record, itemID, goldCurrency, goldPriceOffset, false)
		case family == 1 && mode == 2:
			decoded.options = fixedOptions(record, itemID, cashCurrency, apPriceOffset, true)
		case family == 1 && mode == 4:
			decoded.options = explicitOptions(record, itemID, goldCurrency, goldPriceOffset, false)
		case family == 1 && mode == 5:
			decoded.options = explicitOptions(record, itemID, cashCurrency, explicitAPPriceOffset, true)
		case family == 4 && mode == 1:
			decoded.options = fixedOptions(record, itemID, wolfCoinCurrency, wolfCoinPriceOffset, false)
		case family == 4 && mode == 4:
			decoded.options = explicitOptions(record, itemID, wolfCoinCurrency, wolfCoinPriceOffset, false)
		}
		if len(decoded.options) > 0 {
			decoded.listed = true
		}
		purchaseMode := 1
		if family == 4 {
			purchaseMode = 4
		}
		for index := range decoded.options {
			decoded.options[index].ShopFamily = family
			decoded.options[index].PricingMode = mode
			decoded.options[index].PurchaseMode = purchaseMode
		}
		catalog.optionCount += len(decoded.options)
		catalog.items[itemID] = decoded
	}
	return catalog, nil
}

// AttachSetData decodes each 140-byte set record as one set ID followed by
// seventeen {item ID, duration} uint32 pairs.
func (c *Catalog) AttachSetData(data []byte) error {
	if c == nil {
		return fmt.Errorf("shop catalog is nil")
	}
	if len(data) == 0 || len(data)%SetRecordSize != 0 {
		return fmt.Errorf("size %d is not a nonzero multiple of %d", len(data), SetRecordSize)
	}
	definitions := make(map[int][]SetComponent, len(data)/SetRecordSize)
	for offset := 0; offset < len(data); offset += SetRecordSize {
		record := data[offset : offset+SetRecordSize]
		setID := int(binary.LittleEndian.Uint32(record[0:4]))
		if setID < 1 || setID > 65535 {
			return fmt.Errorf("set record at offset %#x has invalid ID %d", offset, setID)
		}
		if _, duplicate := definitions[setID]; duplicate {
			return fmt.Errorf("set item %d occurs more than once", setID)
		}
		components := make([]SetComponent, 0, setMemberSlots)
		for slot := 0; slot < setMemberSlots; slot++ {
			memberOffset := 4 + slot*8
			itemID := int(binary.LittleEndian.Uint32(record[memberOffset : memberOffset+4]))
			days := int(binary.LittleEndian.Uint32(record[memberOffset+4 : memberOffset+8]))
			if itemID == 0 {
				if days != 0 {
					return fmt.Errorf("set item %d slot %d has duration %d without an item", setID, slot, days)
				}
				continue
			}
			if itemID > 65535 || days < 0 || days > 3650 {
				return fmt.Errorf("set item %d slot %d has invalid component %d/%d", setID, slot, itemID, days)
			}
			components = append(components, SetComponent{ItemID: itemID, Days: days})
		}
		definitions[setID] = components
	}
	for itemID, entry := range c.items {
		components, found := definitions[itemID]
		if entry.listed && (!found || len(components) == 0) {
			if entry.isSet {
				return fmt.Errorf("listed set item %d has no component definition", itemID)
			}
			continue
		}
		if found && len(components) > 0 {
			entry.components = append([]SetComponent(nil), components...)
			c.items[itemID] = entry
		}
	}
	return nil
}

func fixedOptions(record []byte, itemID, currency, priceOffset int, includeReward bool) []Offer {
	options := make([]Offer, 0, len(fixedDurations))
	for slot, days := range fixedDurations {
		price := readU32(record, priceOffset+slot*4)
		if price == 0 {
			continue
		}
		option := Offer{ItemID: itemID, Days: days, Currency: currency, Price: price}
		if includeReward {
			option.WolfCoinReward = readU32(record, wolfCoinRewardOffset+slot*4)
		}
		options = append(options, option)
	}
	return options
}

func explicitOptions(record []byte, itemID, currency, priceOffset int, includeReward bool) []Offer {
	options := make([]Offer, 0, 4)
	for slot := 0; slot < 4; slot++ {
		value := readU32(record, explicitValueOffset+slot*4)
		price := readU32(record, priceOffset+slot*4)
		if value == 0 || price == 0 || uint64(value) > uint64(^uint(0)>>1) {
			continue
		}
		option := Offer{ItemID: itemID, Days: int(value), Currency: currency, Price: price}
		if includeReward {
			option.WolfCoinReward = readU32(record, wolfCoinRewardOffset+slot*4)
		}
		options = append(options, option)
	}
	return options
}

func readU32(record []byte, offset int) uint32 {
	return binary.LittleEndian.Uint32(record[offset : offset+4])
}

func (c *Catalog) ItemCount() int {
	if c == nil {
		return 0
	}
	return len(c.items)
}

func (c *Catalog) OptionCount() int {
	if c == nil {
		return 0
	}
	return c.optionCount
}

// Options returns a copy of an item's shop options (durations/quantities).
func (c *Catalog) Options(itemID int) []Offer {
	if c == nil {
		return nil
	}
	entry, found := c.items[itemID]
	if !found {
		return nil
	}
	return append([]Offer(nil), entry.options...)
}

// Resolve returns the authoritative option for an item and duration/quantity.
func (c *Catalog) Resolve(itemID, days int) (Offer, error) {
	if c == nil {
		return Offer{}, fmt.Errorf("shop catalog is not configured")
	}
	entry, found := c.items[itemID]
	if !found {
		return Offer{}, fmt.Errorf("item %d is absent from shopdata", itemID)
	}
	if !entry.listed && len(entry.options) == 0 {
		return Offer{}, fmt.Errorf("item %d is not listed", itemID)
	}
	var match *Offer
	for index := range entry.options {
		if entry.options[index].Days != days {
			continue
		}
		if match != nil {
			return Offer{}, fmt.Errorf("item %d value %d is ambiguous", itemID, days)
		}
		option := entry.options[index]
		match = &option
	}
	if match == nil {
		return Offer{}, fmt.Errorf("item %d does not offer value %d", itemID, days)
	}
	if len(entry.components) > 0 {
		match.Components = append([]SetComponent(nil), entry.components...)
	} else if entry.isSet || itemID >= WPackFirst && itemID <= WPackLast {
		// A W-Pack without server-defined contents would grant the pack
		// record itself, which the client never shows (docs/protocol/wpack_2012.md).
		return Offer{}, fmt.Errorf("set item %d has no component definition", itemID)
	}
	return *match, nil
}

// ValidatePurchase rejects purchase-mode, price, or reward values that do not
// exactly match the selected shopdata option. The purchase
// mode identifies the shop context; it is not the debit currency.
func (c *Catalog) ValidatePurchase(itemID, days, purchaseMode int, price, wolfCoinReward uint32) (Offer, error) {
	option, err := c.Resolve(itemID, days)
	if err != nil {
		return Offer{}, err
	}
	if purchaseMode != option.PurchaseMode {
		return Offer{}, fmt.Errorf("item %d purchase mode %d does not match catalog shop mode %d", itemID, purchaseMode, option.PurchaseMode)
	}
	if price != option.Price {
		return Offer{}, fmt.Errorf("item %d price %d does not match catalog price %d", itemID, price, option.Price)
	}
	if wolfCoinReward != option.WolfCoinReward {
		return Offer{}, fmt.Errorf("item %d WolfCoin reward %d does not match catalog reward %d", itemID, wolfCoinReward, option.WolfCoinReward)
	}
	return option, nil
}

// ValidateGift also checks the AP-specific WolfCoin reward.
func (c *Catalog) ValidateGift(itemID, days int, price, wolfCoinReward uint32) (Offer, error) {
	option, err := c.Resolve(itemID, days)
	if err != nil {
		return Offer{}, err
	}
	if price != option.Price {
		return Offer{}, fmt.Errorf("gift item %d price %d does not match catalog price %d", itemID, price, option.Price)
	}
	if wolfCoinReward != option.WolfCoinReward {
		return Offer{}, fmt.Errorf("gift item %d WolfCoin reward %d does not match catalog reward %d", itemID, wolfCoinReward, option.WolfCoinReward)
	}
	return option, nil
}

// W-Packs are shop items 14901..15000 (2012 CShell sub_34089200). Their
// contents are server-defined: setitemdata.dat only holds empty placeholders.
const (
	WPackFirst = 14901
	WPackLast  = 15000
)

// AttachWPackContents attaches server-defined W-Pack contents as purchase
// components, so buying or gifting a pack grants its sub-items. The client
// (sub_341C1F10) aborts the pack dialog on any sub-item absent from shopdata.
func (c *Catalog) AttachWPackContents(contents map[int][]SetComponent) error {
	if c == nil {
		return fmt.Errorf("shop catalog is nil")
	}
	for packID, components := range contents {
		pack, ok := c.items[packID]
		if packID < WPackFirst || packID > WPackLast || !ok || len(pack.options) == 0 {
			return fmt.Errorf("W-Pack %d has no purchasable shopdata record", packID)
		}
		if len(components) == 0 || len(components) > setMemberSlots {
			return fmt.Errorf("W-Pack %d has %d components", packID, len(components))
		}
		for _, component := range components {
			sub, ok := c.items[component.ItemID]
			if !ok {
				return fmt.Errorf("W-Pack %d component %d is absent from shopdata", packID, component.ItemID)
			}
			if sub.isSet || (component.ItemID >= 14001 && component.ItemID <= WPackLast) || component.Days < 1 || component.Days > 3650 {
				return fmt.Errorf("W-Pack %d component %d/%d is invalid", packID, component.ItemID, component.Days)
			}
		}
	}
	// Apply only after every pack validated, so a bad row changes nothing.
	for packID, components := range contents {
		pack := c.items[packID]
		pack.components = append([]SetComponent(nil), components...)
		c.items[packID] = pack
	}
	return nil
}

// PremiumMeta returns shopdata bytes +29 and +25 for the Premium Shop list
// (0x146D) and whether the item exists in shopdata.
func (c *Catalog) PremiumMeta(itemID int) (byte, byte, bool) {
	if c == nil {
		return 0, 0, false
	}
	entry, ok := c.items[itemID]
	if !ok {
		return 0, 0, false
	}
	return entry.priceMeta[0], entry.priceMeta[1], true
}

// WPackComponents returns the ordered sub-items of a W-Pack (0x1463).
func (c *Catalog) WPackComponents(itemID int) []SetComponent {
	if c == nil || itemID < WPackFirst || itemID > WPackLast {
		return nil
	}
	return append([]SetComponent(nil), c.items[itemID].components...)
}

// PriceOverride is one CS_IN_ITEMPRICE_INFO_ACK entry that marks an item as
// on sale and repeats its first shopdata price.
type PriceOverride struct {
	ItemID         int
	Kind           byte // 0x81 Gold, 0x82 AP/Cash (bit 7 = override present)
	Meta           [4]byte
	Slot           byte // 1-based duration slot
	Price          uint32
	WolfCoinReward uint32
}

// WPackPriceOverrides returns an override for every W-Pack that has contents.
// 2012 W-Packs are hidden in shopdata (byte +9 = 0); the shop grid only shows
// entries the ITEMPRICE override marks on sale.
func (c *Catalog) WPackPriceOverrides() []PriceOverride {
	if c == nil {
		return nil
	}
	var overrides []PriceOverride
	for id := WPackFirst; id <= WPackLast; id++ {
		entry, ok := c.items[id]
		if !ok || len(entry.components) == 0 || len(entry.options) == 0 {
			continue
		}
		option := entry.options[0]
		kind := byte(0x82)
		if option.Currency == goldCurrency {
			kind = 0x81
		}
		slot := byte(1)
		for index, days := range fixedDurations {
			if days == option.Days {
				slot = byte(index + 1)
				break
			}
		}
		overrides = append(overrides, PriceOverride{
			ItemID: id, Kind: kind, Meta: entry.priceMeta, Slot: slot,
			Price: option.Price, WolfCoinReward: option.WolfCoinReward,
		})
	}
	return overrides
}
