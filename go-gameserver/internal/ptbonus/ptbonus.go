// Package ptbonus parses WT_Attributes\PTBonusGold.dat, the daily play-time
// (P.T.) Gold bonus table (loader sub_34001330): 48 records of u8 kind, u16
// minutes, u16 seconds (u16, wraps past 1092 minutes exactly like the
// client's u16 gauge input, sub_340011B0), u16 Gold. Tiers run every 30
// minutes up to 24 hours.
package ptbonus

import (
	_ "embed"
	"encoding/binary"
)

//go:embed ptbonusgold.dat
var embedded []byte

// Tier is one bonus milestone.
type Tier struct {
	Kind    byte
	Minutes uint16
	Gold    uint16
}

// Seconds is the true (unwrapped) daily play time at which the tier pays.
func (t Tier) Seconds() uint32 { return uint32(t.Minutes) * 60 }

// Tiers returns the client table in file order.
func Tiers() []Tier {
	tiers := make([]Tier, 0, len(embedded)/7)
	for pos := 0; pos+7 <= len(embedded); pos += 7 {
		tiers = append(tiers, Tier{
			Kind:    embedded[pos],
			Minutes: binary.LittleEndian.Uint16(embedded[pos+1:]),
			Gold:    binary.LittleEndian.Uint16(embedded[pos+5:]),
		})
	}
	return tiers
}

// Reward sums the Gold of the tiers reached between two daily play times and
// returns the number of tiers reached at the new time.
func Reward(tiers []Tier, alreadyPaid int, dailySeconds uint32) (gold int, reached int) {
	for _, tier := range tiers {
		if tier.Seconds() <= dailySeconds {
			reached++
		}
	}
	for index := alreadyPaid; index < reached; index++ {
		gold += int(tiers[index].Gold)
	}
	return gold, reached
}
