package lootbox

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"unicode/utf16"
)

const probabilityScale = 10_000

// Reward is one weighted result decoded from bighitboxitem.dat.
type Reward struct {
	BoxID            int
	Name             string
	ItemID           int
	ProbabilityStart int
	ProbabilityEnd   int
	Stackable        bool
	MinimumDays      int
	MaximumDays      int
	Bonus            int
	Tier             int
}

// Catalog contains the lucky-box reward tables.
type Catalog struct {
	boxes       map[int]map[int][]Reward
	rewardCount int
}

func Load(path string) (*Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read lucky-box catalog %q: %w", path, err)
	}
	catalog, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse lucky-box catalog %q: %w", path, err)
	}
	return catalog, nil
}

// Parse decodes the variable-length records used by bighitboxitem.dat.
func Parse(data []byte) (*Catalog, error) {
	catalog := &Catalog{boxes: make(map[int]map[int][]Reward)}
	for offset := 0; offset < len(data); {
		const headerSize = 6
		if len(data)-offset < headerSize {
			return nil, fmt.Errorf("record %d header is truncated at byte %d", catalog.rewardCount, offset)
		}
		boxID := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
		nameUnits := int(binary.LittleEndian.Uint16(data[offset+4 : offset+6]))
		offset += headerSize
		nameBytes := nameUnits * 2
		const valueBytes = 8 * 4
		if nameUnits < 0 || nameBytes > len(data)-offset || valueBytes > len(data)-offset-nameBytes {
			return nil, fmt.Errorf("record %d body is truncated at byte %d", catalog.rewardCount, offset-headerSize)
		}
		encodedName := make([]uint16, nameUnits)
		for index := range encodedName {
			encodedName[index] = binary.LittleEndian.Uint16(data[offset+index*2 : offset+index*2+2])
		}
		name := string(utf16.Decode(encodedName))
		offset += nameBytes
		values := [8]int{}
		for index := range values {
			values[index] = int(binary.LittleEndian.Uint32(data[offset+index*4 : offset+index*4+4]))
		}
		offset += valueBytes

		reward := Reward{
			BoxID: boxID, Name: name, ItemID: values[0],
			ProbabilityStart: values[1], ProbabilityEnd: values[2],
			Stackable: values[3] == 1, MinimumDays: values[4], MaximumDays: values[5],
			Bonus: values[6], Tier: values[7],
		}
		if err := validateReward(reward, values[3]); err != nil {
			return nil, fmt.Errorf("record %d at byte %d: %w", catalog.rewardCount, offset-nameBytes-valueBytes-headerSize, err)
		}
		if catalog.boxes[boxID] == nil {
			catalog.boxes[boxID] = make(map[int][]Reward)
		}
		catalog.boxes[boxID][reward.Tier] = append(catalog.boxes[boxID][reward.Tier], reward)
		catalog.rewardCount++
	}
	if catalog.rewardCount == 0 {
		return nil, fmt.Errorf("catalog is empty")
	}
	for boxID, tiers := range catalog.boxes {
		if _, ok := tiers[0]; !ok {
			return nil, fmt.Errorf("box %d has no normal reward tier", boxID)
		}
		for tier, rewards := range tiers {
			sort.Slice(rewards, func(i, j int) bool {
				if rewards[i].ProbabilityStart == rewards[j].ProbabilityStart {
					return rewards[i].ProbabilityEnd < rewards[j].ProbabilityEnd
				}
				return rewards[i].ProbabilityStart < rewards[j].ProbabilityStart
			})
			previousEnd := 0
			for index, reward := range rewards {
				if tier == 0 {
					if index > 0 && reward.ProbabilityStart < previousEnd {
						return nil, fmt.Errorf("box %d normal tier has overlapping probability ranges at %d", boxID, reward.ProbabilityStart)
					}
					if reward.ProbabilityStart != previousEnd {
						return nil, fmt.Errorf("box %d normal tier has a probability gap at %d", boxID, previousEnd)
					}
				}
				previousEnd = reward.ProbabilityEnd
			}
			if tier == 0 && previousEnd != probabilityScale {
				return nil, fmt.Errorf("box %d normal tier ends at %d, want %d", boxID, previousEnd, probabilityScale)
			}
			tiers[tier] = rewards
		}
	}
	return catalog, nil
}

func validateReward(reward Reward, stackFlag int) error {
	if reward.BoxID < 1 || reward.BoxID > 0xffff {
		return fmt.Errorf("invalid box ID %d", reward.BoxID)
	}
	if reward.ItemID < 1 || reward.ItemID > 0xffff {
		return fmt.Errorf("invalid reward item ID %d", reward.ItemID)
	}
	if reward.ProbabilityStart < 0 || reward.ProbabilityEnd <= reward.ProbabilityStart || reward.ProbabilityEnd > probabilityScale {
		return fmt.Errorf("invalid probability range [%d,%d)", reward.ProbabilityStart, reward.ProbabilityEnd)
	}
	if stackFlag != 0 && stackFlag != 1 {
		return fmt.Errorf("invalid stackable flag %d", stackFlag)
	}
	if reward.MinimumDays < 0 || reward.MaximumDays < reward.MinimumDays || reward.MaximumDays > 0xffff {
		return fmt.Errorf("invalid duration range [%d,%d]", reward.MinimumDays, reward.MaximumDays)
	}
	if reward.Tier < 0 || reward.Tier > 4 {
		return fmt.Errorf("invalid tier %d", reward.Tier)
	}
	return nil
}

// Resolve returns the normal-tier reward whose half-open probability range
// contains roll. Nonzero tiers require separate upgrade-state information.
func (c *Catalog) Resolve(boxID, roll int) (Reward, error) {
	const tier = 0
	if c == nil || roll < 0 || roll >= probabilityScale {
		return Reward{}, fmt.Errorf("invalid lucky-box roll %d", roll)
	}
	tiers, ok := c.boxes[boxID]
	if !ok {
		return Reward{}, fmt.Errorf("unsupported lucky box %d", boxID)
	}
	rewards, ok := tiers[tier]
	if !ok {
		return Reward{}, fmt.Errorf("lucky box %d has no tier %d", boxID, tier)
	}
	index := sort.Search(len(rewards), func(index int) bool {
		return rewards[index].ProbabilityEnd > roll
	})
	if index == len(rewards) || rewards[index].ProbabilityStart > roll {
		return Reward{}, fmt.Errorf("lucky box %d tier %d has no reward at roll %d", boxID, tier, roll)
	}
	return rewards[index], nil
}

func (c *Catalog) HasBox(boxID int) bool {
	if c == nil {
		return false
	}
	_, ok := c.boxes[boxID][0]
	return ok
}

func (c *Catalog) BoxCount() int {
	if c == nil {
		return 0
	}
	return len(c.boxes)
}

func (c *Catalog) RewardCount() int {
	if c == nil {
		return 0
	}
	return c.rewardCount
}
