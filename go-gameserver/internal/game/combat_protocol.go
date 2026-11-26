package game

import (
	"fmt"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

type ChangeFlagRequest struct {
	Flag  byte
	Owner byte
	Actor byte
}

func ParseChangeFlagRequest(payload []byte) (ChangeFlagRequest, error) {
	if len(payload) < 3 {
		return ChangeFlagRequest{}, fmt.Errorf("change-flag payload is too short")
	}
	return ChangeFlagRequest{
		Flag:  payload[0],
		Owner: payload[1],
		Actor: payload[2],
	}, nil
}

func PlayerDeadPayload(killer, victim byte, redScore, blueScore uint16, endRound bool) []byte {
	return protocol.NewWriter(8).U8(0).
		U16(redScore).U16(blueScore).
		U8(killer).U8(victim).Bool(endRound).
		Clone()
}

func RevivalPayload(slot, team, teamSlot, reviverSlot byte) []byte {
	// The payload contains revived slot, team, team row, reviver slot, and four
	// reserved bytes.
	// Ordinary respawn has no reviver and uses the 0xff sentinel.
	return protocol.NewWriter(9).U8(0).
		U8(slot).U8(team).U8(teamSlot).U8(reviverSlot).
		U32(0).
		Clone()
}

func ChangeFlagPayload() []byte { return []byte{0} }

func PlantBombPayload(seconds uint16) []byte {
	return protocol.NewWriter(3).U8(0).U16(seconds).Clone()
}

func RemoveBombPayload() []byte {
	// This message contains only the common status byte.
	return []byte{0}
}

func WeaponKillPayload() []byte {
	// This message contains only the common status byte.
	return []byte{0}
}

func DestroyTowerPayload(redScore, blueScore uint16) []byte {
	return protocol.NewWriter(5).U8(0).U16(redScore).U16(blueScore).Clone()
}

func LadderBossOutPayload() []byte {
	// This message contains only the common status byte.
	return []byte{0}
}

func FlagPointGetPayload(actor, personalPoints, team, teamPoints byte) []byte {
	return protocol.NewWriter(5).U8(0).
		U8(actor).U8(personalPoints).
		U8(team).U8(teamPoints).
		Clone()
}

func EndRoundPayload(round, winTeam byte, redScore, blueScore uint16) []byte {
	// The payload includes a duplicate score pair for alternate validation.
	return protocol.NewWriter(13).U8(0).
		U8(round).U8(winTeam).
		U8(byte(redScore)).U8(byte(blueScore)).
		U16(redScore).U16(blueScore).
		U16(redScore).U16(blueScore).
		Clone()
}

func RestartRoundPayload(round byte, seconds uint16, players []RoundPlayerState) []byte {
	writer := protocol.NewWriter(5 + len(players)*2).U8(0).U8(round).U16(roundTimerValue(seconds)).U8(byte(len(players)))
	for _, player := range players {
		writer.U8(player.Slot).U8(player.Team)
	}
	return writer.Clone()
}

type EndGamePlayer struct {
	Slot       byte
	Team       byte
	GoldReward uint32
	GPReward   uint32
	BonusGold  uint32
	BonusGP    uint32
	BonusFlags uint32
	Winner     bool
}

type EndGameResult struct {
	Info            store.AccountSnapshot
	PreviousGP      uint32
	PlayTimeSeconds uint32
	RedPrideMarkID  uint32
	BluePrideMarkID uint32
	RedPrideName    string
	BluePrideName   string
	RedPridePoints  uint32
	BluePridePoints uint32
	WeaponFlags     byte
	Players         []EndGamePlayer
}

// EndGamePayload encodes the variable end-game layout.
func EndGamePayload(result EndGameResult) ([]byte, error) {
	if len(result.Players) > 255 {
		return nil, fmt.Errorf("end-game player count exceeds one byte")
	}
	// ENDGAME_ACK copied each Pride name into a 40-byte buffer without a bound
	// check in 2012 (sub_34271600): keep the 19 UTF-16 unit cap.
	redPrideUnits := utf16.Encode([]rune(result.RedPrideName))
	if len(redPrideUnits) > 19 {
		redPrideUnits = redPrideUnits[:19]
	}
	bluePrideUnits := utf16.Encode([]rune(result.BluePrideName))
	if len(bluePrideUnits) > 19 {
		bluePrideUnits = bluePrideUnits[:19]
	}

	// The common ACK layer consumes the leading result/status byte.
	writer := protocol.NewWriter(64 + len(result.Players)*23).U8(0).
		U32(nonNegativeU32(result.Info.Gold)).U32(nonNegativeU32(store.LevelBaseThreshold(result.Info.Level))).
		U32(nonNegativeU32(result.Info.GP)).U32(nonNegativeU32(store.NextLevelThreshold(result.Info.Level))).
		U8(byte(result.Info.Level))
	// 2013 TR reads four u32 here (sub_342E3C30), as the 2013 reference
	// (2012 had six).
	writer.U32(result.PlayTimeSeconds).
		U32(0)
	if Client2012 {
		writer.U32(0).U32(0)
	}
	writer.U32(result.RedPrideMarkID).
		U32(result.BluePrideMarkID).
		U8(byte(len(redPrideUnits) * 2))
	for _, unit := range redPrideUnits {
		writer.U16(unit)
	}
	writer.U8(byte(len(bluePrideUnits) * 2))
	for _, unit := range bluePrideUnits {
		writer.U16(unit)
	}
	writer.U32(result.RedPridePoints).U32(result.BluePridePoints)
	// 2013 TR carries the room's weapon-option flags before the player count
	// (the 2012 client had no such byte).
	if !Client2012 {
		writer.U8(result.WeaponFlags)
	}
	writer.U8(byte(len(result.Players)))
	for _, player := range result.Players {
		writer.U8(player.Slot).U32(player.GoldReward).U32(player.GPReward).
			U32(player.BonusGold).U32(player.BonusGP).U32(player.BonusFlags).
			Bool(player.Winner).U8(player.Team)
	}
	return writer.Clone(), nil
}

func MatchAward(stats MatchPlayerStats, won bool) (gold, gp uint32) {
	objectives := stats.Plants + stats.Defuses
	gold = 50 + minU32(stats.Kills, 100)*10 + minU32(objectives, 20)*25
	gp = 100 + minU32(stats.Kills, 100)*20 + minU32(objectives, 20)*50
	if won {
		gold += 50
		gp += 100
	}
	return gold, gp
}

func minU32(value, maximum uint32) uint32 {
	if value > maximum {
		return maximum
	}
	return value
}

func nonNegativeU32(value int) uint32 {
	if value < 0 {
		return 0
	}
	return uint32(value)
}

// GamePointSlot is the receiver's own-slot record in GAMEPOINT_ACK; the 2012
// client stores it into the same slot fields the ENDGAME record fills.
type GamePointSlot struct {
	BonusGold  uint32
	BonusGP    uint32
	BonusFlags uint32
	Winner     bool
	Team       byte
}

// GamePointsPayload encodes 2013 TR CS_FD_GAMEPOINT_ACK (sub_342067D0): u32
// gold, u32, u32 GP, u32 next-level GP, u8 level, u32 play time (+2132, the
// USERINFO #12 field), u8 P.T. bonus index, own slot {u32 bonus gold, u32
// bonus GP, u32, u8, u8}, u32 kills (+128), u32 deaths (+36), then
// sub_3407BAE0: u8 n, n x {u16, u32}. 46 bytes with the result byte and n=0.
func GamePointsPayload(info store.AccountSnapshot, own GamePointSlot) []byte {
	w := protocol.NewWriter(46).U8(0).
		U32(nonNegativeU32(info.Gold)).U32(nonNegativeU32(store.LevelBaseThreshold(info.Level))).
		U32(nonNegativeU32(info.GP)).U32(nonNegativeU32(store.NextLevelThreshold(info.Level))).
		U8(byte(info.Level)).
		U32(info.PlayTimeSeconds)
	if !Client2012 {
		w.U8(0) // P.T. bonus index (2013 TR)
	}
	return w.
		U32(own.BonusGold).U32(own.BonusGP).U32(own.BonusFlags).
		Bool(own.Winner).U8(own.Team).
		U32(nonNegativeU32(info.Kills)).U32(nonNegativeU32(info.Deaths)).
		U8(0). // sub_3407BAE0 record count
		Clone()
}
