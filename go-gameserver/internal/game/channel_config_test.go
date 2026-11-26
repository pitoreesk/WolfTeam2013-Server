package game

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"

	"wtserver-go/internal/store"
	"wtserver-go/internal/transport"
)

type channelConfigUsers struct {
	*fakeUsers
	channels []store.Channel
}

func (u *channelConfigUsers) Channels(context.Context) ([]store.Channel, error) {
	return append([]store.Channel(nil), u.channels...), nil
}

func TestGameLoginUsesConfiguredChannelType(t *testing.T) {
	serverNet, clientNet := net.Pipe()
	defer serverNet.Close()
	defer clientNet.Close()

	loopback := netip.MustParseAddr("127.0.0.1")
	users := &channelConfigUsers{
		fakeUsers: &fakeUsers{},
		channels: []store.Channel{
			{Name: "Other", Port: 40708, Type: store.ChannelTypeNormal},
			{Name: "Pride", Port: 40707, Type: store.ChannelTypePrideBattle},
		},
	}
	server := NewServer(
		"unused", 40707, 40708, loopback, loopback, users,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	session, err := newSession(transport.NewEncryptedConn(serverNet, nil), loopback, loopback)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- server.handleLogin(context.Background(), session, []byte{
			0, 0, 0, 4, 'u', 's', 'e', 'r', 4, 'p', 'a', 's', 's',
		})
	}()
	reply, err := transport.NewEncryptedConn(clientNet, nil).ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if reply.MessageID != ReplyChannelLogin {
		t.Fatalf("login reply ID = %#x", reply.MessageID)
	}
	if got := binary.LittleEndian.Uint16(reply.Payload[15:17]); got != uint16(store.ChannelTypePrideBattle) {
		t.Fatalf("login channel type = %d, want %d", got, store.ChannelTypePrideBattle)
	}
	if got := session.channelType(); got != store.ChannelTypePrideBattle {
		t.Fatalf("session channel type = %d, want %d", got, store.ChannelTypePrideBattle)
	}
}

func TestConflictingChannelTypesOnOnePortUseNormal(t *testing.T) {
	users := &channelConfigUsers{
		fakeUsers: &fakeUsers{},
		channels: []store.Channel{
			{Name: "First", Port: 40707, Type: store.ChannelTypePrideBattle},
			{Name: "Second", Port: 40707, Type: store.ChannelTypeLadder},
		},
	}
	server := &Server{
		matchPort: 40707,
		users:     users,
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if got := server.configuredChannelType(context.Background()); got != store.ChannelTypeNormal {
		t.Fatalf("conflicting channel type = %d, want %d", got, store.ChannelTypeNormal)
	}
}

type prideChannelLoginUsers struct {
	*fakeUsers
	store.PrideStore
	channels []store.Channel
	member   bool
}

func (u *prideChannelLoginUsers) Channels(context.Context) ([]store.Channel, error) {
	return append([]store.Channel(nil), u.channels...), nil
}

func (u *prideChannelLoginUsers) PrideForUser(_ context.Context, userID int) (store.Pride, store.PrideMember, bool, error) {
	if !u.member {
		return store.Pride{}, store.PrideMember{}, false, nil
	}
	return store.Pride{ID: 9, Name: "Wolf"}, store.PrideMember{UserID: userID}, true, nil
}

// A Pride Battle channel refuses accounts without a Pride with result 38,
// which the server-list section shows as stringtable 4037 (2011
// sub_341CDBE0; same in 2012/2013). Members, and everyone on other channels, log in normally.
func TestPrideChannelLoginRequiresPride(t *testing.T) {
	for _, test := range []struct {
		name       string
		channel    store.ChannelType
		member     bool
		wantResult byte
	}{
		{"pride channel without pride", store.ChannelTypePrideBattle, false, prideChannelNoPrideStatus},
		{"pride channel member", store.ChannelTypePrideBattle, true, 0},
		{"normal channel without pride", store.ChannelTypeNormal, false, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			serverNet, clientNet := net.Pipe()
			defer serverNet.Close()
			defer clientNet.Close()
			loopback := netip.MustParseAddr("127.0.0.1")
			users := &prideChannelLoginUsers{
				fakeUsers: &fakeUsers{},
				channels:  []store.Channel{{Name: "C", Port: 40707, Type: test.channel}},
				member:    test.member,
			}
			server := NewServer("unused", 40707, 40708, loopback, loopback, users,
				slog.New(slog.NewTextHandler(io.Discard, nil)))
			session, err := newSession(transport.NewEncryptedConn(serverNet, nil), loopback, loopback)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- server.handleLogin(context.Background(), session, []byte{
					0, 0, 0, 4, 'u', 's', 'e', 'r', 4, 'p', 'a', 's', 's',
				})
			}()
			reply, err := transport.NewEncryptedConn(clientNet, nil).ReadFrame()
			if err != nil {
				t.Fatal(err)
			}
			if test.wantResult == 0 {
				// A successful login also pushes follow-up frames; drain them.
				go func() {
					for {
						if _, err := transport.NewEncryptedConn(clientNet, nil).ReadFrame(); err != nil {
							return
						}
					}
				}()
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if reply.MessageID != ReplyChannelLogin || reply.Payload[0] != test.wantResult {
				t.Fatalf("login reply %#x = %x, want result %d", reply.MessageID, reply.Payload, test.wantResult)
			}
			if test.wantResult != 0 {
				if _, _, userID, _ := session.identity(); userID != 0 {
					t.Fatalf("refused session is authenticated as %d", userID)
				}
			}
		})
	}
}
