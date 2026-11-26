package config

import "testing"

func TestDefaultConfiguration(t *testing.T) {
	cfg := Default()
	if cfg.LoginPort != 8444 || cfg.BrokerPort != 40706 || cfg.GamePort != 40707 || cfg.BotPeerPortBase != 40708 || cfg.BuddyPort != 40709 {
		t.Fatalf("unexpected service ports: %+v", cfg)
	}
	if got := cfg.AdvertisedIP.String(); got != "127.0.0.1" {
		t.Fatalf("advertised IP = %s", got)
	}
	if got := cfg.PublicIP.String(); got != "127.0.0.1" {
		t.Fatalf("public IP = %s", got)
	}
	if cfg.Launcher.ChannelID != 0x13 {
		t.Fatalf("launcher channel = %#x", cfg.Launcher.ChannelID)
	}
	if cfg.PeerEndpointMode != 1 {
		t.Fatalf("peer endpoint mode = %d", cfg.PeerEndpointMode)
	}
	if cfg.Patcher.LauncherVersion != 20 || cfg.Patcher.GameVersion != 377 {
		t.Fatalf("launcher/game patch versions = %d/%d", cfg.Patcher.LauncherVersion, cfg.Patcher.GameVersion)
	}
	if cfg.Patcher.PatchDir != "patches" || cfg.Patcher.PublicURL != "" {
		t.Fatalf("patch location defaults = %q / %q", cfg.Patcher.PatchDir, cfg.Patcher.PublicURL)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config is invalid: %v", err)
	}
}

func TestCustomPatchVersion(t *testing.T) {
	t.Setenv("WT_PATCH_LAUNCHER_VERSION", "21")
	t.Setenv("WT_PATCH_GAME_VERSION", "1")
	t.Setenv("WT_PATCH_DIR", "custom-patches")
	t.Setenv("WT_PATCH_PUBLIC_URL", "https://patch.example/game")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Patcher.LauncherVersion != 21 || cfg.Patcher.GameVersion != 1 {
		t.Fatalf("launcher/game patch versions = %d/%d", cfg.Patcher.LauncherVersion, cfg.Patcher.GameVersion)
	}
	if cfg.Patcher.PatchDir != "custom-patches" || cfg.Patcher.PublicURL != "https://patch.example/game" {
		t.Fatalf("patch location = %q / %q", cfg.Patcher.PatchDir, cfg.Patcher.PublicURL)
	}
}

func TestCustomShopDataPath(t *testing.T) {
	t.Setenv("WT_SHOPDATA_PATH", `C:\GameClient\attributes\shopdata.dat`)
	t.Setenv("WT_LUCKYBOXDATA_PATH", `C:\GameClient\attributes\bighitboxitem.dat`)
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ShopDataPath != `C:\GameClient\attributes\shopdata.dat` {
		t.Fatalf("shopdata path = %q", cfg.ShopDataPath)
	}
	if cfg.LuckyBoxDataPath != `C:\GameClient\attributes\bighitboxitem.dat` {
		t.Fatalf("lucky-box data path = %q", cfg.LuckyBoxDataPath)
	}
}

func TestRejectsInvalidPatchPublicURL(t *testing.T) {
	cfg := Default()
	cfg.Patcher.PublicURL = "ftp://user@example.com/patches?bad=1"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid patch public URL")
	}
}

func TestRejectsPortCollision(t *testing.T) {
	cfg := Default()
	cfg.RelayPort = cfg.GamePort
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected a port collision error")
	}
}

func TestRejectsBotBootstrapCollision(t *testing.T) {
	cfg := Default()
	cfg.RelayPort = cfg.GamePort + 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected collision with game-port+1")
	}
}

func TestCustomBuddyPort(t *testing.T) {
	t.Setenv("WT_BUDDY_PORT", "45002")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BuddyPort != 45002 {
		t.Fatalf("custom buddy port = %d", cfg.BuddyPort)
	}
}

func TestCustomGamePortMovesBotPeerPort(t *testing.T) {
	t.Setenv("WT_GAME_PORT", "45000")
	t.Setenv("WT_BOT_PEER_PORT_BASE", "")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GamePort != 45000 || cfg.BotPeerPortBase != 45001 {
		t.Fatalf("custom game ports = %d/%d", cfg.GamePort, cfg.BotPeerPortBase)
	}
}

func TestLauncherTLSFilesMustBeConfiguredTogether(t *testing.T) {
	cfg := Default()
	cfg.Launcher.TLSCertFile = "launcher.crt"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected incomplete launcher TLS configuration to be rejected")
	}

	cfg.Launcher.TLSKeyFile = "launcher.key"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("complete launcher TLS configuration: %v", err)
	}
}
