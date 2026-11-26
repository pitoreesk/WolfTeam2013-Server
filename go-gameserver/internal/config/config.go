package config

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config contains the externally visible service settings.
type Config struct {
	ListenHost       string
	LoginPort        int
	BrokerPort       int
	GamePort         int
	BuddyPort        int
	// RankingPort serves the in-game ranking web module (0x050E); 0 = off.
	RankingPort      int
	RelayPort        int
	BotPeerPortBase  int
	RelayPortBase    int
	PeerEndpointMode int
	AdvertisedIP     netip.Addr
	PublicIP         netip.Addr
	DatabaseURL      string
	ShopDataPath     string
	LuckyBoxDataPath string
	// WPackEnabled puts the 2012 K-Paket (W-Pack) products on sale. Off by
	// default: the 2012 client ships no Wpack_bg / Wpack_button_yes/no
	// textures, so its pack dialog has no background and no buttons.
	WPackEnabled bool
	// Client2012 switches wire packet layouts from 2013 TR to 2012 TR.
	Client2012 bool
	// EventTop selects the event_topN.dtx lobby banner (0 hides it).
	EventTop int
	Launcher Launcher
	Patcher  Patcher
}

type Launcher struct {
	ChannelID   uint32
	TLSCertFile string
	TLSKeyFile  string
}

type Patcher struct {
	Enabled         bool
	Port            int
	LauncherVersion int
	GameVersion     int
	PatchDir        string
	PublicURL       string
	ApexWebDir      string
	NoticeWebDir    string
}

func Default() Config {
	return Config{
		ListenHost:       "0.0.0.0",
		LoginPort:        8444,
		BrokerPort:       40706,
		GamePort:         40707,
		BuddyPort:        40709,
		RelayPort:        40710,
		BotPeerPortBase:  40708,
		RelayPortBase:    41000,
		PeerEndpointMode: 1,
		AdvertisedIP:     netip.MustParseAddr("127.0.0.1"),
		PublicIP:         netip.MustParseAddr("127.0.0.1"),
		DatabaseURL:      "postgres://gameserver:gameserver_local_password@127.0.0.1:5432/gameserver?sslmode=disable",
		Launcher: Launcher{
			ChannelID: 0x13,
		},
		Patcher: Patcher{
			Enabled: true, Port: 8080, LauncherVersion: 20, GameVersion: 377,
			PatchDir: "patches",
		},
	}
}

// FromEnv overlays WT_* environment variables onto the local defaults.
func FromEnv() (Config, error) {
	cfg := Default()
	var err error

	if value := os.Getenv("WT_LISTEN_HOST"); value != "" {
		cfg.ListenHost = value
	}
	for key, target := range map[string]*int{
		"WT_LOGIN_PORT":             &cfg.LoginPort,
		"WT_BROKER_PORT":            &cfg.BrokerPort,
		"WT_GAME_PORT":              &cfg.GamePort,
		"WT_BUDDY_PORT":             &cfg.BuddyPort,
		"WT_RANKING_PORT":           &cfg.RankingPort,
		"WT_RELAY_PORT":             &cfg.RelayPort,
		"WT_BOT_PEER_PORT_BASE":     &cfg.BotPeerPortBase,
		"WT_RELAY_PORT_BASE":        &cfg.RelayPortBase,
		"WT_PATCH_PORT":             &cfg.Patcher.Port,
		"WT_PATCH_LAUNCHER_VERSION": &cfg.Patcher.LauncherVersion,
		"WT_PATCH_GAME_VERSION":     &cfg.Patcher.GameVersion,
	} {
		if *target, err = envInt(key, *target); err != nil {
			return Config{}, err
		}
	}
	if os.Getenv("WT_BOT_PEER_PORT_BASE") == "" {
		cfg.BotPeerPortBase = cfg.GamePort + 1
	}
	if cfg.AdvertisedIP, err = envAddr("WT_ADVERTISED_IP", cfg.AdvertisedIP); err != nil {
		return Config{}, err
	}
	if cfg.PublicIP, err = envAddr("WT_PUBLIC_IP", cfg.PublicIP); err != nil {
		return Config{}, err
	}
	if value := os.Getenv("WT_DATABASE_URL"); value != "" {
		cfg.DatabaseURL = value
	}
	if value := os.Getenv("WT_SHOPDATA_PATH"); value != "" {
		cfg.ShopDataPath = value
	}
	if value := os.Getenv("WT_LUCKYBOXDATA_PATH"); value != "" {
		cfg.LuckyBoxDataPath = value
	}
	if value := os.Getenv("WT_PATCH_DIR"); value != "" {
		cfg.Patcher.PatchDir = value
	}
	if value := os.Getenv("WT_PATCH_PUBLIC_URL"); value != "" {
		cfg.Patcher.PublicURL = value
	}
	if value := os.Getenv("WT_APEX_WEB_DIR"); value != "" {
		cfg.Patcher.ApexWebDir = value
	}
	if value := os.Getenv("WT_NOTICE_WEB_DIR"); value != "" {
		cfg.Patcher.NoticeWebDir = value
	}
	if cfg.PeerEndpointMode, err = envInt("WT_PEER_ENDPOINT_MODE", cfg.PeerEndpointMode); err != nil {
		return Config{}, err
	}
	if value := os.Getenv("WT_LAUNCHER_CHANNEL_ID"); value != "" {
		parsed, parseErr := strconv.ParseUint(value, 0, 32)
		if parseErr != nil {
			return Config{}, fmt.Errorf("WT_LAUNCHER_CHANNEL_ID: %w", parseErr)
		}
		cfg.Launcher.ChannelID = uint32(parsed)
	}
	if value := os.Getenv("WT_LAUNCHER_TLS_CERT"); value != "" {
		cfg.Launcher.TLSCertFile = value
	}
	if value := os.Getenv("WT_LAUNCHER_TLS_KEY"); value != "" {
		cfg.Launcher.TLSKeyFile = value
	}
	if cfg.Patcher.Enabled, err = envBool("WT_PATCH_ENABLED", cfg.Patcher.Enabled); err != nil {
		return Config{}, err
	}
	if cfg.WPackEnabled, err = envBool("WT_WPACK_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.Client2012, err = envBool("WT_CLIENT_2012", false); err != nil {
		return Config{}, err
	}
	cfg.EventTop = 0
	if value := os.Getenv("WT_EVENT_TOP"); value != "" {
		top, convErr := strconv.Atoi(value)
		if convErr != nil || top < 0 || top > 255 {
			return Config{}, fmt.Errorf("WT_EVENT_TOP must be 0..255, got %q", value)
		}
		cfg.EventTop = top
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if net.ParseIP(c.ListenHost) == nil {
		return fmt.Errorf("listen host %q is not an IP address", c.ListenHost)
	}
	ports := map[string]int{
		"login":              c.LoginPort,
		"broker":             c.BrokerPort,
		"game":               c.GamePort,
		"buddy":              c.BuddyPort,
		"bot peer bootstrap": c.GamePort + 1,
		"relay":              c.RelayPort,
		"relay base":         c.RelayPortBase,
	}
	if c.RankingPort != 0 {
		ports["ranking web"] = c.RankingPort
	}
	if c.Patcher.Enabled {
		ports["launcher patch HTTP"] = c.Patcher.Port
		if c.Patcher.LauncherVersion < 0 || c.Patcher.GameVersion < 0 {
			return fmt.Errorf("launcher and game patch versions must not be negative: %d/%d", c.Patcher.LauncherVersion, c.Patcher.GameVersion)
		}
		if strings.TrimSpace(c.Patcher.PatchDir) == "" {
			return fmt.Errorf("launcher patch directory must not be empty")
		}
		if c.Patcher.PublicURL != "" {
			publicURL, err := url.Parse(c.Patcher.PublicURL)
			if err != nil || (publicURL.Scheme != "http" && publicURL.Scheme != "https") || publicURL.Host == "" || publicURL.User != nil || publicURL.RawQuery != "" || publicURL.Fragment != "" || strings.ContainsAny(c.Patcher.PublicURL, ";\r\n") {
				return fmt.Errorf("patch public URL %q must be an absolute HTTP(S) URL without credentials, query, or fragment", c.Patcher.PublicURL)
			}
		}
	}
	if c.GamePort >= 65535 {
		return fmt.Errorf("game port %d leaves no port for the bot-peer bootstrap listener", c.GamePort)
	}
	if c.BotPeerPortBase != c.GamePort+1 {
		return fmt.Errorf("bot peer port %d must equal game port + 1 (%d)", c.BotPeerPortBase, c.GamePort+1)
	}
	if c.RelayPortBase > 65036 {
		return fmt.Errorf("relay base %d leaves fewer than 500 relay ports", c.RelayPortBase)
	}
	if c.PeerEndpointMode < 0 || c.PeerEndpointMode > 2 {
		return fmt.Errorf("peer endpoint mode %d is outside 0..2", c.PeerEndpointMode)
	}
	if (c.Launcher.TLSCertFile == "") != (c.Launcher.TLSKeyFile == "") {
		return fmt.Errorf("WT_LAUNCHER_TLS_CERT and WT_LAUNCHER_TLS_KEY must be configured together")
	}
	seen := make(map[int]string, len(ports))
	for name, port := range ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("%s port %d is outside 1..65535", name, port)
		}
		if previous, ok := seen[port]; ok {
			return fmt.Errorf("%s and %s both use port %d", previous, name, port)
		}
		seen[port] = name
	}
	if !c.AdvertisedIP.Is4() || !c.PublicIP.Is4() {
		return fmt.Errorf("advertised and public IP addresses must be IPv4")
	}
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return fmt.Errorf("PostgreSQL database URL must not be empty")
	}
	return nil
}

func (c Config) ListenAddress(port int) string {
	return net.JoinHostPort(c.ListenHost, strconv.Itoa(port))
}

func envInt(key string, fallback int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return parsed, nil
}

func envAddr(key string, fallback netip.Addr) (netip.Addr, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("%s: %w", key, err)
	}
	return parsed, nil
}

func envBool(key string, fallback bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	switch strings.ToLower(value) {
	case "1", "true", "on", "yes":
		return true, nil
	case "0", "false", "off", "no":
		return false, nil
	default:
		return false, fmt.Errorf("%s: invalid boolean %q", key, value)
	}
}
