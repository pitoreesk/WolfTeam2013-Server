package game

import (
	"context"

	"wtserver-go/internal/store"
)

// End-game bonus flags select the corresponding result categories.
const (
	endGameBonusPrideChain  uint32 = 0x001
	endGameBonusGoldPlus    uint32 = 0x002
	endGameBonusGPPlus      uint32 = 0x004
	endGameBonusBurning     uint32 = 0x020
	endGameBonusPrideBattle uint32 = 0x040
	endGameBonusPowerUser   uint32 = 0x100
)

type rewardBonusGroup byte

const (
	rewardBonusBooster rewardBonusGroup = iota
	rewardBonusPowerUser
	rewardBonusBackpack
	rewardBonusCharacter
	rewardBonusBurning
)

type rewardItemBonus struct {
	goldPercent uint32
	gpPercent   uint32
	group       rewardBonusGroup
}

type matchReward struct {
	gold       uint32
	gp         uint32
	bonusGold  uint32
	bonusGP    uint32
	bonusFlags uint32
}

// rewardItemBonuses contains the Gold/GP percentages used for match awards.
var rewardItemBonuses = func() map[int]rewardItemBonus {
	result := map[int]rewardItemBonus{
		10001: {gpPercent: 30, group: rewardBonusBooster},
		10002: {goldPercent: 30, group: rewardBonusBooster},
		10003: {goldPercent: 30, gpPercent: 30, group: rewardBonusBooster},
		10015: {gpPercent: 60, group: rewardBonusBooster},
		10025: {goldPercent: 100, gpPercent: 100, group: rewardBonusBooster},
		10027: {goldPercent: 50, gpPercent: 50, group: rewardBonusPowerUser},
		10029: {goldPercent: 50, gpPercent: 50, group: rewardBonusPowerUser},
		13001: {gpPercent: 10, group: rewardBonusBackpack},
		13002: {gpPercent: 10, group: rewardBonusBackpack},
		13003: {gpPercent: 15, group: rewardBonusBackpack},
		13004: {gpPercent: 15, group: rewardBonusBackpack},
		13005: {gpPercent: 15, group: rewardBonusBackpack},
		13006: {gpPercent: 20, group: rewardBonusBackpack},
		13007: {gpPercent: 3, group: rewardBonusBackpack},
		13008: {gpPercent: 5, group: rewardBonusBackpack},
		13009: {gpPercent: 20, group: rewardBonusBackpack},
		14005: {goldPercent: 30, gpPercent: 30, group: rewardBonusBooster},
	}

	addCharacters := func(goldPercent, gpPercent uint32, suffixes ...int) {
		for _, suffix := range suffixes {
			result[30000+suffix] = rewardItemBonus{
				goldPercent: goldPercent,
				gpPercent:   gpPercent,
				group:       rewardBonusCharacter,
			}
		}
	}
	addCharacters(30, 0, 1, 3, 7, 9, 15, 17, 28, 30)
	addCharacters(0, 30, 2, 4, 8, 10, 16, 18, 27, 29)
	addCharacters(30, 30,
		11, 12, 13, 14,
		23, 24, 25, 26,
		48, 49, 50, 51, 52,
		69, 70, 71, 72,
	)
	addCharacters(40, 0,
		19, 20, 21, 22,
		31, 33, 35, 37, 39, 41,
		54, 56, 58, 60, 62, 64, 66, 68,
		73, 75, 77, 78, 79, 80,
	)
	addCharacters(0, 40,
		32, 34, 36, 38, 40, 42,
		43, 44, 45, 46, 47,
		53, 55, 57, 59, 61, 63, 65, 67,
		74, 76,
	)
	return result
}()

func (s *Server) calculateMatchReward(
	ctx context.Context,
	player MatchPlayer,
	won bool,
	userID int,
	burning rewardItemBonus,
) matchReward {
	baseGold, baseGP := MatchAward(player.Stats, won)
	result := matchReward{gold: baseGold, gp: baseGP}
	groups := make(map[rewardBonusGroup]rewardItemBonus, 5)
	if burning.goldPercent > 0 || burning.gpPercent > 0 {
		burning.group = rewardBonusBurning
		groups[rewardBonusBurning] = burning
	}
	appliedBoosters := make(map[int]struct{})

	applyItem := func(itemID int) {
		bonus, ok := rewardItemBonuses[itemID]
		if !ok {
			return
		}
		current := groups[bonus.group]
		if bonus.group == rewardBonusBooster {
			// Different active booster items stack. Multiple inventory rows of
			// the same item represent renewed duration/quantity, not additional
			// copies of the percentage bonus.
			if _, applied := appliedBoosters[itemID]; applied {
				return
			}
			appliedBoosters[itemID] = struct{}{}
			current.goldPercent = addUint32(current.goldPercent, bonus.goldPercent)
			current.gpPercent = addUint32(current.gpPercent, bonus.gpPercent)
			current.group = bonus.group
			groups[bonus.group] = current
			return
		}
		if bonus.goldPercent > current.goldPercent {
			current.goldPercent = bonus.goldPercent
		}
		if bonus.gpPercent > current.gpPercent {
			current.gpPercent = bonus.gpPercent
		}
		current.group = bonus.group
		groups[bonus.group] = current
	}

	items, itemsErr := s.users.Items(ctx, userID)
	if itemsErr != nil {
		s.logBackendFallback("load match reward items", userID, itemsErr)
	} else {
		for _, item := range items {
			if bonus, ok := rewardItemBonuses[item.ID]; ok &&
				(bonus.group == rewardBonusBooster || bonus.group == rewardBonusPowerUser) {
				applyItem(item.ID)
			}
		}

		loadout, err := s.users.CharacterSelection(ctx, userID)
		if err != nil {
			s.logBackendFallback("load match reward character", userID, err)
		} else {
			applyItem(resolveCharacter(player.Team, loadout, characterOwnership(items)))
		}
	}

	equipment, err := s.users.Equipment(ctx, userID)
	if err != nil {
		s.logBackendFallback("load match reward equipment", userID, err)
	} else {
		for _, uniqueID := range equipment[24:] {
			itemID := equipmentItemID(uniqueID)
			if bonus, ok := rewardItemBonuses[itemID]; ok && bonus.group == rewardBonusBackpack {
				applyItem(itemID)
			}
		}
	}

	// Older/custom stores may expose only the entitlement query. Preserve the
	// Power User award in that configuration while preferring the item snapshot.
	if _, found := groups[rewardBonusPowerUser]; !found &&
		s.hasActiveItem(ctx, userID, powerUserItemID) {
		applyItem(powerUserItemID)
	}

	var goldPercent, gpPercent uint32
	for group, bonus := range groups {
		goldPercent += bonus.goldPercent
		gpPercent += bonus.gpPercent
		if group == rewardBonusPowerUser {
			result.bonusFlags |= endGameBonusPowerUser
			continue
		}
		if group == rewardBonusBurning {
			result.bonusFlags |= endGameBonusBurning
			continue
		}
		if bonus.goldPercent > 0 {
			result.bonusFlags |= endGameBonusGoldPlus
		}
		if bonus.gpPercent > 0 {
			result.bonusFlags |= endGameBonusGPPlus
		}
	}
	result.bonusGold = percentageOf(baseGold, goldPercent)
	result.bonusGP = percentageOf(baseGP, gpPercent)
	return result
}

// channelBurningBonus maps the world-info GP and Gold bonus fields.
func (s *Server) channelBurningBonus(ctx context.Context) rewardItemBonus {
	channels, ok := s.users.(store.ChannelSource)
	if !ok {
		return rewardItemBonus{}
	}
	configured, err := channels.Channels(ctx)
	if err != nil {
		s.logBackendFallback("load channel Burning bonus", 0, err)
		return rewardItemBonus{}
	}
	for _, channel := range configured {
		if channel.Port != s.matchPort {
			continue
		}
		return rewardItemBonus{
			goldPercent: channelBonusPercent(channel.GoldBonusPercent),
			gpPercent:   channelBonusPercent(channel.GPBonusPercent),
			group:       rewardBonusBurning,
		}
	}
	return rewardItemBonus{}
}

func channelBonusPercent(value int) uint32 {
	if value <= 0 {
		return 0
	}
	if value > 0xff {
		return 0xff
	}
	return uint32(value)
}

func percentageOf(value, percent uint32) uint32 {
	return saturatingUint32(uint64(value) * uint64(percent) / 100)
}

func addUint32(left, right uint32) uint32 {
	return saturatingUint32(uint64(left) + uint64(right))
}

func saturatingUint32(value uint64) uint32 {
	const maximum = ^uint32(0)
	if value > uint64(maximum) {
		return maximum
	}
	return uint32(value)
}
