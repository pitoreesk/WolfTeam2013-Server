package broker

import (
	"bytes"
	"net/netip"
	"testing"

	"wtserver-go/internal/store"
)

func TestChainListPayloadHasExpectedLayout(t *testing.T) {
	got := ChainListPayload()
	if len(got) != 16 || got[0] != 0 || got[1] != 0x0e || !bytes.Equal(got[2:], make([]byte, 14)) {
		t.Fatalf("chain list = %x", got)
	}
}

func TestRelayListPayloadHasExpectedLayout(t *testing.T) {
	got, err := RelayListPayload(
		netip.MustParseAddr("127.0.0.1"),
		40708,
		[]store.Channel{{Name: "New Players", Port: 40707, Open: true}},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 0, 1, 0, 0, 0, 0, 127, 0, 0, 1, 0x9f, 0x04}
	if !bytes.Equal(got, want) {
		t.Fatalf("relay list = %x, want %x", got, want)
	}
}

func TestWorldListPayloadHasExpectedLayout(t *testing.T) {
	channels := []store.Channel{{Name: "General", Port: 40707, Open: true}}
	got, err := WorldListPayload(netip.MustParseAddr("127.0.0.1"), channels)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 1, 0x7f, 0, 0, 1, 0x9f, 3, 3, 2, 0, 0xf4, 1, 0x11, 0, 0xf4, 1}
	if !bytes.Equal(got, want) {
		t.Fatalf("world list = %x, want %x", got, want)
	}
}

func TestWorldListPayloadMultipleChannelsOpen(t *testing.T) {
	channels := []store.Channel{
		{Name: "General 1", Port: 40707, Open: true},
		{Name: "General 2", Port: 40707, Open: true},
	}
	got, err := WorldListPayload(netip.MustParseAddr("127.0.0.1"), channels)
	if err != nil {
		t.Fatal(err)
	}
	if got[8] != 3 || got[23] != 3 {
		t.Fatalf("channel status bytes = %d, %d (want 3, 3 for open channels)", got[8], got[23])
	}
}

func TestLayoutChannelsForBroker(t *testing.T) {
	channels := []store.Channel{
		{Name: "Normal 1", Port: 40707, Open: true, Type: store.ChannelTypeNormal},
		{Name: "Normal 2", Port: 40707, Open: true, Type: store.ChannelTypeNormal},
		{Name: "Ladder", Port: 40711, Open: true, Type: store.ChannelTypeLadder},
	}
	slotted := LayoutChannelsForBroker(channels)
	if len(slotted) != 43 {
		t.Fatalf("slotted channels length = %d, want 43", len(slotted))
	}
	if slotted[0].Name != "Normal 1" || !slotted[0].Open {
		t.Fatalf("slot 0 = %+v", slotted[0])
	}
	if slotted[1].Name != "Normal 2" || !slotted[1].Open {
		t.Fatalf("slot 1 = %+v", slotted[1])
	}
	for i := 2; i < 42; i++ {
		if slotted[i].Open {
			t.Fatalf("slot %d should be closed, got %+v", i, slotted[i])
		}
	}
	if slotted[42].Name != "Ladder" || !slotted[42].Open || slotted[42].Port != 40711 {
		t.Fatalf("slot 42 (Ladder tab) = %+v", slotted[42])
	}
}

func TestLayoutChannelsForBrokerWithPrideAndLadder(t *testing.T) {
	channels := []store.Channel{
		{Name: "New Players", Port: 40707, Open: true, Type: store.ChannelTypeNormal},
		{Name: "Global Channel 1", Port: 40707, Open: true, Type: store.ChannelTypeNormal},
		{Name: "Ladder", Port: 40711, Open: true, Type: store.ChannelTypeLadder},
		{Name: "Pride Battle", Port: 40715, Open: true, Type: store.ChannelTypePrideBattle},
	}
	slotted := LayoutChannelsForBroker(channels)
	if len(slotted) != 44 {
		t.Fatalf("slotted channels length = %d, want 44", len(slotted))
	}
	if slotted[0].Name != "New Players" || !slotted[0].Open {
		t.Fatalf("slot 0 = %+v", slotted[0])
	}
	if slotted[1].Name != "Global Channel 1" || !slotted[1].Open {
		t.Fatalf("slot 1 = %+v", slotted[1])
	}
	for i := 2; i < 42; i++ {
		if slotted[i].Open {
			t.Fatalf("slot %d should be closed, got %+v", i, slotted[i])
		}
	}
	if slotted[42].Name != "Ladder" || !slotted[42].Open || slotted[42].Port != 40711 {
		t.Fatalf("slot 42 = %+v", slotted[42])
	}
	if slotted[43].Name != "Pride Battle" || !slotted[43].Open || slotted[43].Port != 40715 {
		t.Fatalf("slot 43 = %+v", slotted[43])
	}
}

func TestWorldListPayloadUsesConfiguredPort(t *testing.T) {
	got, err := WorldListPayload(netip.MustParseAddr("127.0.0.1"), []store.Channel{{Name: "Other", Port: 41000}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[6:8], []byte{0xa0, 0x28}) {
		t.Fatalf("world endpoint port = %x", got[6:8])
	}
}

func TestWorldInfoPayloadUsesUTF16(t *testing.T) {
	got, err := WorldInfoPayload([]store.Channel{{
		Name:             "A",
		ServerKills:      0x1234,
		ServerDeaths:     0xabcd,
		GPBonusPercent:   0x56,
		GoldBonusPercent: 0x78,
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 1, 2, 'A', 0, 0x34, 0x12, 0xcd, 0xab, 0x56, 0x78}
	if !bytes.Equal(got, want) {
		t.Fatalf("world info = %x, want %x", got, want)
	}
}

func TestWorldInfoPayloadRejectsOutOfRangeFields(t *testing.T) {
	tests := []struct {
		name    string
		channel store.Channel
	}{
		{name: "negative server kills", channel: store.Channel{Name: "A", ServerKills: -1}},
		{name: "large server kills", channel: store.Channel{Name: "A", ServerKills: 0x10000}},
		{name: "negative server deaths", channel: store.Channel{Name: "A", ServerDeaths: -1}},
		{name: "large server deaths", channel: store.Channel{Name: "A", ServerDeaths: 0x10000}},
		{name: "negative GP bonus", channel: store.Channel{Name: "A", GPBonusPercent: -1}},
		{name: "large GP bonus", channel: store.Channel{Name: "A", GPBonusPercent: 0x100}},
		{name: "negative Gold bonus", channel: store.Channel{Name: "A", GoldBonusPercent: -1}},
		{name: "large Gold bonus", channel: store.Channel{Name: "A", GoldBonusPercent: 0x100}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := WorldInfoPayload([]store.Channel{test.channel}); err == nil {
				t.Fatal("expected an out-of-range field error")
			}
		})
	}
}
