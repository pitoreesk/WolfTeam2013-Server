package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wtserver-go/internal/broker"
	"wtserver-go/internal/config"
	"wtserver-go/internal/game"
	"wtserver-go/internal/launcher"
	"wtserver-go/internal/protocol"
	"wtserver-go/internal/shopdata"
	"wtserver-go/internal/store"
	"wtserver-go/internal/testsupport"
	"wtserver-go/internal/transport"
)

type smokeBackend struct{ testsupport.GameRepository }

func (smokeBackend) Channels(context.Context) ([]store.Channel, error) {
	return []store.Channel{{
		Name: "New Players", Port: 40707, Open: true,
		ServerDeaths: 10000, GoldBonusPercent: 20,
	}}, nil
}
func (smokeBackend) Login(_ context.Context, username, password string) (int, error) {
	if (username == "user" && password == "pass") || (username == "admin" && password == "test123") {
		return 42, nil
	}
	return -1, nil
}
func (smokeBackend) AccountSnapshot(context.Context, int) (store.AccountSnapshot, error) {
	return store.AccountSnapshot{Callsign: "Player", Level: 7, Gold: 100, WC: 200, Cash: 300}, nil
}

func TestFullStackSocketSmoke(t *testing.T) {
	ports := reserveServicePorts(t)
	userFiles := t.TempDir()
	cfg := config.Config{
		ListenHost:       "127.0.0.1",
		LoginPort:        ports.login,
		BrokerPort:       ports.broker,
		GamePort:         ports.game,
		BuddyPort:        ports.buddy,
		RelayPort:        ports.relay,
		BotPeerPortBase:  ports.botBase,
		RelayPortBase:    ports.relayBase,
		PeerEndpointMode: 2,
		AdvertisedIP:     netip.MustParseAddr("127.0.0.1"),
		PublicIP:         netip.MustParseAddr("127.0.0.1"),
		DatabaseURL:      "postgres://unused",
		ShopDataPath:     filepath.Join(userFiles, "shopdata.dat"),
		LuckyBoxDataPath: filepath.Join(userFiles, "bighitboxitem.dat"),
		Launcher:         config.Launcher{ChannelID: 0x13},
		Patcher:          config.Patcher{Enabled: false},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- NewWithBackend(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), smokeBackend{}).Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("app shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("app did not stop")
		}
	})

	launcherReady := dialEventually(t, cfg.ListenAddress(cfg.LoginPort))
	_ = launcherReady.Close()
	launcherAddress := "http://" + cfg.ListenAddress(cfg.LoginPort)
	loginResponse, err := http.Post(
		launcherAddress+launcher.SessionPath,
		"application/json",
		strings.NewReader(`{"username":"admin","password":"test123"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer loginResponse.Body.Close()
	if loginResponse.StatusCode != http.StatusCreated {
		t.Fatalf("launcher login status = %d", loginResponse.StatusCode)
	}
	var launcherLogin struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(loginResponse.Body).Decode(&launcherLogin); err != nil {
		t.Fatal(err)
	}
	startRequest, err := http.NewRequest(http.MethodPost, launcherAddress+launcher.StartSessionPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	startRequest.Header.Set("Authorization", "Bearer "+launcherLogin.Token)
	startResponse, err := http.DefaultClient.Do(startRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer startResponse.Body.Close()
	if startResponse.StatusCode != http.StatusOK {
		t.Fatalf("launcher start status = %d", startResponse.StatusCode)
	}
	var start struct {
		ChannelID uint32 `json:"channelId"`
	}
	if err := json.NewDecoder(startResponse.Body).Decode(&start); err != nil {
		t.Fatal(err)
	}
	if start.ChannelID != 0x13 {
		t.Fatalf("launcher channel = %#x", start.ChannelID)
	}

	brokerConn := dialEventually(t, cfg.ListenAddress(cfg.BrokerPort))
	defer brokerConn.Close()
	_ = brokerConn.SetDeadline(time.Now().Add(3 * time.Second))
	brokerWire := transport.NewEncryptedConn(brokerConn, nil)
	if _, err := brokerWire.WriteMessage(broker.MessageWorldListRequest, nil); err != nil {
		t.Fatal(err)
	}
	world := readEncrypted(t, brokerWire, broker.MessageWorldListReply)
	if len(world.Payload) < 17 || world.Payload[1] != 1 || !bytes.Equal(world.Payload[2:6], []byte{127, 0, 0, 1}) {
		t.Fatalf("broker world list = %x", world.Payload)
	}
	if _, err := brokerWire.WriteMessage(broker.MessageRelayListRequest, nil); err != nil {
		t.Fatal(err)
	}
	relays := readEncrypted(t, brokerWire, broker.MessageRelayListReply)
	if len(relays.Payload) < 13 || binary.LittleEndian.Uint32(relays.Payload[2:6]) != 1 ||
		!bytes.Equal(relays.Payload[7:11], []byte{127, 0, 0, 1}) ||
		binary.BigEndian.Uint16(relays.Payload[11:13]) != uint16(cfg.GamePort) {
		t.Fatalf("broker hybrid relay list = %x, want 127.0.0.1:%d", relays.Payload, cfg.GamePort)
	}

	gameConn := dialEventually(t, cfg.ListenAddress(cfg.GamePort))
	defer gameConn.Close()
	_ = gameConn.SetDeadline(time.Now().Add(3 * time.Second))
	gameWire := transport.NewEncryptedConn(gameConn, nil)
	login := []byte{0, 0, 0, 4, 'u', 's', 'e', 'r', 4, 'p', 'a', 's', 's'}
	if _, err := gameWire.WriteMessage(game.RequestChannelLogin, login); err != nil {
		t.Fatal(err)
	}
	loginReply := readEncrypted(t, gameWire, game.ReplyChannelLogin)
	if loginReply.Payload[0] != 0 || binary.LittleEndian.Uint32(loginReply.Payload[3:7]) != 42 {
		t.Fatalf("game login = %x", loginReply.Payload)
	}
	buddyEndpoint := readEncrypted(t, gameWire, game.ReplyBuddyServerEndpoint)
	wantBuddyEndpoint := []byte{0, 127, 0, 0, 1, byte(cfg.BuddyPort >> 8), byte(cfg.BuddyPort)}
	if len(buddyEndpoint.Payload) < len(wantBuddyEndpoint) || !bytes.Equal(buddyEndpoint.Payload[:len(wantBuddyEndpoint)], wantBuddyEndpoint) {
		t.Fatalf("buddy endpoint = %x, want %x", buddyEndpoint.Payload, wantBuddyEndpoint)
	}
	buddyConn := dialEventually(t, cfg.ListenAddress(cfg.BuddyPort))
	_ = buddyConn.Close()

	for _, port := range []int{cfg.GamePort, cfg.GamePort + 1} {
		response := queryUDPUntilReady(t, port)
		reader := protocol.NewBitReader(response)
		token, tokenErr := reader.Bits(32)
		message, messageErr := reader.Bits(protocol.LTMessageBits)
		if tokenErr != nil || messageErr != nil || uint32(token) != protocol.LTToken || message != protocol.LTMessageQueryResponse {
			t.Fatalf("UDP %d query response = %x", port, response)
		}
	}

	select {
	case err := <-done:
		t.Fatalf("app stopped before cancellation: %v", err)
	default:
	}
}

func TestBrokerRelayPortByEndpointMode(t *testing.T) {
	cfg := config.Config{GamePort: 40707, RelayPort: 40710}
	for _, test := range []struct {
		mode int
		want uint16
	}{
		{mode: 0, want: 40710},
		{mode: 1, want: 40710},
		{mode: 2, want: 40707},
	} {
		cfg.PeerEndpointMode = test.mode
		if got := brokerRelayPort(cfg); got != test.want {
			t.Errorf("endpoint mode %d broker relay port = %d, want %d", test.mode, got, test.want)
		}
	}
}

func TestMissingOptionalCatalogsWarnAndContinue(t *testing.T) {
	directory := t.TempDir()
	shopPath := filepath.Join(directory, "shopdata.dat")
	luckyBoxPath := filepath.Join(directory, "bighitboxitem.dat")
	cfg := config.Config{ShopDataPath: shopPath, LuckyBoxDataPath: luckyBoxPath}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	backend := smokeBackend{}
	server := game.NewServer(
		"unused",
		40707,
		40708,
		netip.MustParseAddr("127.0.0.1"),
		netip.MustParseAddr("127.0.0.1"),
		backend,
		logger,
	)

	if err := NewWithBackend(cfg, logger, backend).configureOptionalCatalogs(server, backend); err != nil {
		t.Fatalf("missing optional catalogs prevented startup: %v", err)
	}
	output := logs.String()
	for _, expected := range []string{
		"optional shop catalog files are missing",
		"shopdata.dat",
		"setitemdata.dat",
		"optional lucky-box catalog file is missing",
		"bighitboxitem.dat",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("startup warning does not contain %q: %s", expected, output)
		}
	}
}

func TestMissingSetItemCatalogWarningNamesMissingFile(t *testing.T) {
	directory := t.TempDir()
	shopPath := filepath.Join(directory, "shopdata.dat")
	if err := os.WriteFile(shopPath, make([]byte, shopdata.RecordSize), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ShopDataPath: shopPath}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	backend := smokeBackend{}
	server := game.NewServer(
		"unused",
		40707,
		40708,
		netip.MustParseAddr("127.0.0.1"),
		netip.MustParseAddr("127.0.0.1"),
		backend,
		logger,
	)

	if err := NewWithBackend(cfg, logger, backend).configureOptionalCatalogs(server, backend); err != nil {
		t.Fatalf("missing set-item catalog prevented startup: %v", err)
	}
	output := logs.String()
	if !strings.Contains(output, "setitemdata.dat") {
		t.Fatalf("startup warning does not name the missing set-item catalog: %s", output)
	}
	if strings.Contains(output, shopPath) {
		t.Fatalf("startup warning incorrectly reports the present shop catalog as missing: %s", output)
	}
}

func TestMalformedOptionalCatalogsRemainFatal(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(string) config.Config
	}{
		{
			name: "shop",
			configure: func(path string) config.Config {
				return config.Config{ShopDataPath: path}
			},
		},
		{
			name: "lucky box",
			configure: func(path string) config.Config {
				return config.Config{LuckyBoxDataPath: path}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "malformed.dat")
			if err := os.WriteFile(path, []byte{1}, 0o600); err != nil {
				t.Fatal(err)
			}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			backend := smokeBackend{}
			server := game.NewServer(
				"unused",
				40707,
				40708,
				netip.MustParseAddr("127.0.0.1"),
				netip.MustParseAddr("127.0.0.1"),
				backend,
				logger,
			)
			app := NewWithBackend(test.configure(path), logger, backend)
			if err := app.configureOptionalCatalogs(server, backend); err == nil {
				t.Fatal("malformed catalog was accepted")
			}
		})
	}
}

type servicePorts struct {
	login     int
	broker    int
	game      int
	buddy     int
	relay     int
	botBase   int
	relayBase int
}

func reserveServicePorts(t *testing.T) servicePorts {
	t.Helper()
	used := make(map[int]bool)
	unique := func(max int) int {
		for {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			_ = listener.Close()
			if port <= max && !used[port] {
				used[port] = true
				return port
			}
		}
	}
	gamePort := 0
	for gamePort == 0 {
		candidate := unique(65534)
		if used[candidate+1] {
			continue
		}
		first, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: candidate})
		if err != nil {
			continue
		}
		second, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: candidate + 1})
		_ = first.Close()
		if err != nil {
			continue
		}
		_ = second.Close()
		gamePort = candidate
		used[candidate+1] = true
	}
	return servicePorts{
		login:     unique(65535),
		broker:    unique(65535),
		game:      gamePort,
		buddy:     unique(65535),
		relay:     unique(65535),
		botBase:   gamePort + 1,
		relayBase: unique(65036),
	}
}

func dialEventually(t *testing.T, address string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		connection, err := net.DialTimeout("tcp4", address, 200*time.Millisecond)
		if err == nil {
			return connection
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial %s: %v", address, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readEncrypted(t *testing.T, wire *transport.EncryptedConn, messageID uint16) transport.Frame {
	t.Helper()
	frame, err := wire.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if frame.MessageID != messageID {
		t.Fatalf("message ID = %#x, want %#x", frame.MessageID, messageID)
	}
	return frame
}

func queryUDPUntilReady(t *testing.T, port int) []byte {
	t.Helper()
	remote := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
	connection, err := net.DialUDP("udp4", nil, remote)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var query protocol.BitWriter
	query.U32(protocol.LTToken)
	query.Bits(protocol.LTMessageQuery, protocol.LTMessageBits)
	deadline := time.Now().Add(5 * time.Second)
	buffer := make([]byte, 256)
	for {
		_ = connection.SetDeadline(time.Now().Add(200 * time.Millisecond))
		if _, err := connection.Write(query.Clone()); err == nil {
			if count, readErr := connection.Read(buffer); readErr == nil {
				return append([]byte(nil), buffer[:count]...)
			}
		}
		if time.Now().After(deadline) {
			t.Fatal(fmt.Errorf("UDP %d did not answer query", port))
		}
	}
}
