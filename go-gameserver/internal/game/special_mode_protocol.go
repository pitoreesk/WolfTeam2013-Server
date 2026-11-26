package game

import (
	"fmt"

	"wtserver-go/internal/protocol"
)

type WolfHuntTeamState struct {
	Slot     byte
	Team     byte
	TeamSlot byte
}

func WolfHuntTeamSortPayload(players []WolfHuntTeamState) ([]byte, error) {
	if len(players) > 18 {
		return nil, fmt.Errorf("wolf-hunt team-sort player count exceeds protocol limit")
	}
	// The player count follows the common success byte.
	writer := protocol.NewWriter(2 + len(players)*3).U8(0).U8(byte(len(players)))
	for _, player := range players {
		if player.Team != 1 && player.Team != 2 {
			return nil, fmt.Errorf("wolf-hunt player %d has invalid team %d", player.Slot, player.Team)
		}
		writer.U8(player.Slot).U8(player.Team).U8(player.TeamSlot)
	}
	return writer.Clone(), nil
}

// WolfKingPayload is two counted slot lists. The first list receives the King
// flag and the second contains normal wolves; omitted active players are
// assigned to the human team.
func WolfKingPayload(kings, wolves []byte) ([]byte, error) {
	if len(kings) > 255 || len(wolves) > 255 {
		return nil, fmt.Errorf("wolf role list exceeds one-byte protocol limit")
	}
	writer := protocol.NewWriter(3 + len(kings) + len(wolves)).U8(0).U8(byte(len(kings))).Bytes(kings).U8(byte(len(wolves))).Bytes(wolves)
	return writer.Clone(), nil
}

func ChangeCreatureTeamPayload(slot, team byte) []byte {
	return []byte{0, slot, team}
}

func ChangeRolePayload(role byte) []byte {
	return []byte{0, role}
}

// SwitchTeamPayload encodes 2012 SWITCHTEAM_ACK (sub_341CCD60): the common
// result byte, then the switched flag. The client swaps scores locally.
func SwitchTeamPayload(switched bool) []byte {
	return protocol.NewWriter(2).U8(0).Bool(switched).Clone()
}

type LadderRoundPlayer struct {
	Slot       byte
	RoundScore uint16
	TotalScore uint32
}

func LadderRoundEndPayload(players []LadderRoundPlayer) ([]byte, error) {
	if len(players) > 255 {
		return nil, fmt.Errorf("ladder round player count exceeds one-byte protocol limit")
	}
	writer := protocol.NewWriter(2 + len(players)*7).U8(0).U8(byte(len(players)))
	for _, player := range players {
		writer.U8(player.Slot).U16(player.RoundScore).U32(player.TotalScore)
	}
	return writer.Clone(), nil
}

type RemainLifeNotice struct {
	Slot  byte
	Lives byte
	Kind  byte
}

func NoticeRemainLifePayload(players []RemainLifeNotice) ([]byte, error) {
	if len(players) > 255 {
		return nil, fmt.Errorf("remaining-life player count exceeds one-byte protocol limit")
	}
	writer := protocol.NewWriter(2 + len(players)*4).U8(0).U8(byte(len(players)))
	for _, player := range players {
		writer.U8(player.Slot).U8(0) // reserved
		// Kind 1 selects Hero lives and kind 2 selects WolfHunt lives.
		writer.U8(player.Lives).U8(player.Kind)
	}
	return writer.Clone(), nil
}

// HeroSelectPayload identifies the selected hero's team and the slots that
// receive Hero state. The first field is a team, not a room slot.
func HeroSelectPayload(heroTeam byte, heroSlots []byte) ([]byte, error) {
	if len(heroSlots) > 255 {
		return nil, fmt.Errorf("hero slot count exceeds one-byte protocol limit")
	}
	writer := protocol.NewWriter(3 + len(heroSlots)).U8(0).U8(heroTeam).U8(byte(len(heroSlots))).Bytes(heroSlots)
	return writer.Clone(), nil
}

func HeroRemainLifePayload(slot, team byte, active bool, lives, kind byte, value uint16) []byte {
	// Respawn duration is not serialized here. Kind 1 uses a fixed five-second
	// value.
	return protocol.NewWriter(8).U8(0).U8(slot).U8(team).Bool(active).
		U8(lives).U8(kind).U16(value).Clone()
}

// HeroModeRecord updates one of the two team rows. TeamIndex 1 selects the
// second row; every other value selects the first.
type HeroModeRecord struct {
	TeamIndex       byte
	ShowHeroGauge   bool
	NormalUserTotal byte
	NormalUserCount byte
	HeroUserTotal   byte
	HeroUserCount   byte
}

func HeroModeStatePayload(records []HeroModeRecord) ([]byte, error) {
	if len(records) > 255 {
		return nil, fmt.Errorf("hero-mode state count exceeds one-byte protocol limit")
	}
	writer := protocol.NewWriter(2 + len(records)*6).U8(0).U8(byte(len(records)))
	for _, record := range records {
		writer.U8(record.TeamIndex).Bool(record.ShowHeroGauge).
			U8(record.NormalUserTotal).U8(record.NormalUserCount).
			U8(record.HeroUserTotal).U8(record.HeroUserCount)
	}
	return writer.Clone(), nil
}

type HeroOptionPlayer struct {
	Slot  byte
	State byte
}

func HeroOptionStatePayload(redOption, blueOption byte, players []HeroOptionPlayer) ([]byte, error) {
	if len(players) > 255 {
		return nil, fmt.Errorf("hero-option player count exceeds one-byte protocol limit")
	}
	writer := protocol.NewWriter(4 + len(players)*2).U8(0).U8(redOption).U8(blueOption).U8(byte(len(players)))
	for _, player := range players {
		writer.U8(player.Slot).U8(player.State)
	}
	return writer.Clone(), nil
}
