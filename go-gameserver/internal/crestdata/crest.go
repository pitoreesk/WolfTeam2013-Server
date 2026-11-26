// Package crestdata parses WT_Attributes\CrestData.dat, the client's crest
// (arma) table. The 2013 TR file is embedded byte for byte (2942 bytes, 22
// records); docs/protocol/crest_2013.md has the record layout.
package crestdata

import (
	_ "embed"
	"encoding/binary"
	"fmt"
	"unicode/utf16"
)

//go:embed crestdata.dat
var embedded []byte

// Crest is one CrestData.dat record:
//
//	0x00 u16 crest ID
//	0x02 u8  page (0 kill, 1 mode, 2 other)
//	0x03 u16 group code + 1000 (the client subtracts 1000: array index at +2380)
//	0x05 u8  tier 0..2
//	0x06 u16 Gold challenge cost (summed by the challenge sheet before 0x1352)
//	0x08 u16 n, n x UTF-16 icon path
//	     u16 target count, u16 completion reward (WC), 20 bytes padding
type Crest struct {
	ID       uint16
	Page     byte
	Group    uint16
	Tier     byte
	Cost     uint32
	Icon     string
	Target   uint32
	RewardWC uint32
}

// Challenge groups: the argument of the client's crest event hook
// sub_3407DB90 at its seven call sites (2013 TR CShell).
const (
	GroupHeadshot  uint16 = 0 // death message bit -> 0x1634 byte [3]
	GroupSneakKill uint16 = 1 // death message bit, never reported to the server
	GroupBombPlant uint16 = 2 // sub_34241630 (bomb plant)
	GroupWolfKing  uint16 = 3 // sub_342E4690 (Wolf King selection)
	GroupItemThrow uint16 = 4 // sub_342C0560 (0x141F THROWITEM_ACK)
	GroupGift      uint16 = 5 // sub_34229C50 (0x1A01 SENDGIFT_ACK)
	GroupLuckyBox  uint16 = 6 // sub_342C2B60 (0x1423 lucky box ACK)
)

// Catalog indexes the crest records.
type Catalog struct {
	byID    map[uint16]Crest
	ordered []Crest
}

// Default parses the embedded client table.
func Default() *Catalog {
	catalog, err := Parse(embedded)
	if err != nil {
		panic(err)
	}
	return catalog
}

// Parse decodes a CrestData.dat image. The ID-0 "none" row is skipped.
func Parse(data []byte) (*Catalog, error) {
	catalog := &Catalog{byID: map[uint16]Crest{}}
	for pos := 0; pos < len(data); {
		if pos+10 > len(data) {
			return nil, fmt.Errorf("crest record at %d is truncated", pos)
		}
		length := int(binary.LittleEndian.Uint16(data[pos+8:]))
		iconEnd := pos + 10 + 2*length
		if iconEnd+24 > len(data) {
			return nil, fmt.Errorf("crest record at %d exceeds the file", pos)
		}
		units := make([]uint16, length)
		for i := range units {
			units[i] = binary.LittleEndian.Uint16(data[pos+10+2*i:])
		}
		crest := Crest{
			ID:       binary.LittleEndian.Uint16(data[pos:]),
			Page:     data[pos+2],
			Group:    binary.LittleEndian.Uint16(data[pos+3:]) - 1000,
			Tier:     data[pos+5],
			Cost:     uint32(binary.LittleEndian.Uint16(data[pos+6:])),
			Icon:     string(utf16.Decode(units)),
			Target:   uint32(binary.LittleEndian.Uint16(data[iconEnd:])),
			RewardWC: uint32(binary.LittleEndian.Uint16(data[iconEnd+2:])),
		}
		pos = iconEnd + 24
		if crest.ID == 0 {
			continue
		}
		catalog.byID[crest.ID] = crest
		catalog.ordered = append(catalog.ordered, crest)
	}
	return catalog, nil
}

// Lookup returns the record of a crest ID.
func (c *Catalog) Lookup(id uint16) (Crest, bool) {
	if c == nil {
		return Crest{}, false
	}
	crest, ok := c.byID[id]
	return crest, ok
}

// All returns the records in file order.
func (c *Catalog) All() []Crest {
	if c == nil {
		return nil
	}
	return c.ordered
}
