package store

import "context"

// ChannelType is the lobby/gameplay category selected during channel login.
type ChannelType uint16

const (
	ChannelTypeNormal ChannelType = iota
	ChannelTypeSpat
	ChannelTypePrideBattle
	ChannelTypeLadder
	ChannelTypePrivateLadder
)

func (t ChannelType) Valid() bool {
	return t <= ChannelTypePrivateLadder
}

type Channel struct {
	Name             string
	Port             uint16
	Open             bool
	Type             ChannelType
	ServerKills      int
	ServerDeaths     int
	GPBonusPercent   int
	GoldBonusPercent int
}

type ChannelSource interface {
	Channels(context.Context) ([]Channel, error)
}
