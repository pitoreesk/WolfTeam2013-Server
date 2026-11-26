package broker

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
	"wtserver-go/internal/store"
)

const (
	MessageWorldListRequest uint16 = 0x1100
	MessageWorldListReply   uint16 = 0x1101
	MessageChainListRequest uint16 = 0x1102
	MessageChainListReply   uint16 = 0x1103
	MessageRelayListRequest uint16 = 0x1104
	MessageRelayListReply   uint16 = 0x1105
	MessageWorldInfoRequest uint16 = 0x1106
	MessageWorldInfoReply   uint16 = 0x1107
)

func ChainListPayload() []byte {
	const chainCount = 14
	writer := protocol.NewWriter(2 + chainCount).U8(0).U8(chainCount)
	for range chainCount {
		writer.U8(0)
	}
	return writer.Clone()
}

// LayoutChannelsForBroker arranges channels into CShell.dll's tab layout:
// Tab 1 (id 50): slots 0..13 (Normal / Spat / Private channels)
// Tab 2 (id 51): slots 14..27
// Tab 3 (id 52): slots 28..41
// Tab 4 (id 53, LADDER): slots 42..55 (Pride Battle / Ladder channels)
func LayoutChannelsForBroker(channels []store.Channel) []store.Channel {
	if len(channels) == 0 {
		return channels
	}
	var normal []store.Channel
	var ladder []store.Channel
	defaultPort := uint16(40707)
	for _, ch := range channels {
		if ch.Port != 0 {
			defaultPort = ch.Port
		}
		if ch.Type == store.ChannelTypePrideBattle || ch.Type == store.ChannelTypeLadder || ch.Type == store.ChannelTypePrivateLadder {
			ladder = append(ladder, ch)
		} else {
			normal = append(normal, ch)
		}
	}
	if len(ladder) == 0 {
		return normal
	}
	result := make([]store.Channel, 42+len(ladder))
	copy(result, normal)
	for i := len(normal); i < 42; i++ {
		result[i] = store.Channel{
			Name: "",
			Port: defaultPort,
			Open: false,
			Type: store.ChannelTypeNormal,
		}
	}
	for i, ch := range ladder {
		result[42+i] = ch
	}
	return result
}

func RelayListPayload(advertisedIP netip.Addr, relayPort uint16, channels []store.Channel) ([]byte, error) {
	if !advertisedIP.Is4() {
		return nil, fmt.Errorf("advertised IP must be IPv4")
	}
	if relayPort == 0 {
		return nil, fmt.Errorf("relay port must be non-zero")
	}
	if len(channels) > 255 {
		return nil, fmt.Errorf("channel count %d exceeds one-byte relay index limit", len(channels))
	}

	// Encode the relay response as a counted sequence of endpoints.
	writer := protocol.NewWriter(6 + 7*len(channels))
	writer.U8(0)
	writer.U8(0)
	writer.U32(uint32(len(channels)))
	ip := advertisedIP.As4()
	for index := range channels {
		writer.U8(byte(index))
		writer.Bytes(ip[:])
		var port [2]byte
		binary.BigEndian.PutUint16(port[:], relayPort)
		writer.Bytes(port[:])
	}
	return writer.Clone(), nil
}

func WorldListPayload(advertisedIP netip.Addr, channels []store.Channel) ([]byte, error) {
	if !advertisedIP.Is4() {
		return nil, fmt.Errorf("advertised IP must be IPv4")
	}
	if len(channels) > 253 {
		return nil, fmt.Errorf("channel count %d exceeds one-byte protocol limit", len(channels))
	}
	writer := protocol.NewWriter(2 + 15*len(channels))
	writer.U8(0)
	writer.U8(byte(len(channels)))
	ip := advertisedIP.As4()
	for _, channel := range channels {
		if channel.Port == 0 {
			return nil, fmt.Errorf("channel %q port must be non-zero", channel.Name)
		}
		writer.Bytes(ip[:])
		var port [2]byte
		binary.BigEndian.PutUint16(port[:], channel.Port)
		writer.Bytes(port[:])
		if channel.Open {
			writer.U8(3) // 3 = open / available in CShell.dll
			// The wire layout carries two capacity pairs.
			writer.U16(2).U16(500).U16(17).U16(500)
		} else {
			writer.U8(4) // 4 = closed / disabled in CShell.dll
			writer.U16(0).U16(0).U16(0).U16(0)
		}
	}
	return writer.Clone(), nil
}

func WorldInfoPayload(channels []store.Channel) ([]byte, error) {
	if len(channels) > 255 {
		return nil, fmt.Errorf("channel count %d exceeds one-byte protocol limit", len(channels))
	}
	writer := protocol.NewWriter(256)
	writer.U8(0)
	writer.U8(byte(len(channels)))
	for _, channel := range channels {
		units := utf16.Encode([]rune(channel.Name))
		if len(units)*2 > 255 {
			return nil, fmt.Errorf("channel name %q exceeds one-byte UTF-16 length", channel.Name)
		}
		if channel.ServerKills < 0 || channel.ServerKills > 0xffff {
			return nil, fmt.Errorf("channel %q server kills %d exceeds uint16 range", channel.Name, channel.ServerKills)
		}
		if channel.ServerDeaths < 0 || channel.ServerDeaths > 0xffff {
			return nil, fmt.Errorf("channel %q server deaths %d exceeds uint16 range", channel.Name, channel.ServerDeaths)
		}
		if channel.GPBonusPercent < 0 || channel.GPBonusPercent > 0xff {
			return nil, fmt.Errorf("channel %q GP bonus %d exceeds uint8 range", channel.Name, channel.GPBonusPercent)
		}
		if channel.GoldBonusPercent < 0 || channel.GoldBonusPercent > 0xff {
			return nil, fmt.Errorf("channel %q Gold bonus %d exceeds uint8 range", channel.Name, channel.GoldBonusPercent)
		}
		writer.U8(byte(len(units) * 2))
		writer.UTF16LE(channel.Name)
		// Four display/restriction fields follow the channel name.
		writer.U16(uint16(channel.ServerKills))
		writer.U16(uint16(channel.ServerDeaths))
		writer.U8(byte(channel.GPBonusPercent))
		writer.U8(byte(channel.GoldBonusPercent))
	}
	return writer.Clone(), nil
}
