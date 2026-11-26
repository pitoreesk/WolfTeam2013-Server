package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"wtserver-go/internal/broker"
	"wtserver-go/internal/buddy"
	"wtserver-go/internal/config"
	"wtserver-go/internal/game"
	"wtserver-go/internal/launcher"
	"wtserver-go/internal/lootbox"
	"wtserver-go/internal/patcher"
	"wtserver-go/internal/shopdata"
	"wtserver-go/internal/store"
)

type runtimeBackend interface {
	store.GameRepository
	store.ChannelSource
}

type luckyBoxCatalogConfigurer interface {
	ConfigureLuckyBoxCatalog(*lootbox.Catalog)
}

type App struct {
	config  config.Config
	logger  *slog.Logger
	backend runtimeBackend
}

func New(cfg config.Config, logger *slog.Logger) *App {
	return &App{config: cfg, logger: logger}
}

func NewWithBackend(cfg config.Config, logger *slog.Logger, backend runtimeBackend) *App {
	return &App{config: cfg, logger: logger, backend: backend}
}

func (a *App) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	backend := a.backend
	if backend == nil {
		postgres, err := store.OpenPostgres(ctx, a.config.DatabaseURL)
		if err != nil {
			return err
		}
		defer postgres.Close()
		backend = postgres
	}

	launcherServer := launcher.NewServer(
		a.config.ListenAddress(a.config.LoginPort),
		launcher.Options{
			ChannelID:   a.config.Launcher.ChannelID,
			TLSCertFile: a.config.Launcher.TLSCertFile,
			TLSKeyFile:  a.config.Launcher.TLSKeyFile,
		},
		backend,
		a.logger.With("service", "launcher"),
	)
	brokerServer := broker.NewServer(
		a.config.ListenAddress(a.config.BrokerPort),
		a.config.AdvertisedIP,
		brokerRelayPort(a.config),
		backend,
		a.logger.With("service", "broker"),
	)
	gameServer := game.NewServer(
		a.config.ListenAddress(a.config.GamePort),
		uint16(a.config.GamePort),
		uint16(a.config.BotPeerPortBase),
		a.config.AdvertisedIP,
		a.config.PublicIP,
		backend,
		a.logger.With("service", "game"),
	)
	gameServer.ConfigureRelay(
		a.config.AdvertisedIP,
		uint16(a.config.RelayPort),
		uint16(a.config.RelayPortBase),
		a.config.PeerEndpointMode,
	)
	gameServer.ConfigureGameVersion(a.config.Patcher.GameVersion)
	gameServer.ConfigureBuddy(uint16(a.config.BuddyPort))
	gameServer.ConfigureRankingWeb(uint16(a.config.RankingPort))
	if err := a.configureOptionalCatalogs(gameServer, backend); err != nil {
		return err
	}
	var buddyBackend store.BuddyStore
	if persistentBuddyBackend, ok := backend.(store.BuddyStore); ok {
		buddyBackend = persistentBuddyBackend
	}
	buddyServer := buddy.NewServer(
		a.config.ListenAddress(a.config.BuddyPort),
		buddyBackend,
		a.logger.With("service", "buddy"),
	)

	type result struct {
		service string
		err     error
	}
	services := []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "launcher", run: launcherServer.Run},
		{name: "broker", run: brokerServer.Run},
		{name: "game", run: gameServer.Run},
		{name: "game-udp", run: gameServer.RunUDP},
		{name: "game-relay", run: func(ctx context.Context) error {
			return gameServer.RunRelay(ctx, a.config.ListenAddress(a.config.RelayPort))
		}},
	}
	if a.config.RankingPort != 0 {
		services = append(services, struct {
			name string
			run  func(context.Context) error
		}{name: "ranking-web", run: func(ctx context.Context) error {
			return gameServer.RunRankingWeb(ctx, a.config.ListenAddress(a.config.RankingPort))
		}})
	}
	if a.config.Patcher.Enabled {
		registrar, _ := backend.(patcher.AccountRegistrar)
		patchServer, err := patcher.NewServer(
			a.config.ListenAddress(a.config.Patcher.Port),
			patcher.Options{
				LauncherVersion: a.config.Patcher.LauncherVersion,
				GameVersion:     a.config.Patcher.GameVersion,
				PatchDir:        a.config.Patcher.PatchDir,
				PublicURL:       a.config.Patcher.PublicURL,
				ApexWebDir:      a.config.Patcher.ApexWebDir,
				NoticeWebDir:    a.config.Patcher.NoticeWebDir,
				Registrar:       registrar,
			},
			a.logger.With("service", "patcher"),
		)
		if err != nil {
			return fmt.Errorf("patcher catalog: %w", err)
		}
		services = append(services, struct {
			name string
			run  func(context.Context) error
		}{name: "patcher", run: patchServer.Run})
	}
	serviceCount := len(services) + 1
	results := make(chan result, serviceCount)
	buddyReady := make(chan struct{})
	go func() { results <- result{service: "buddy", err: buddyServer.RunReady(ctx, buddyReady)} }()
	select {
	case <-buddyReady:
	case early := <-results:
		cancel()
		if early.err != nil {
			return fmt.Errorf("%s service: %w", early.service, early.err)
		}
		return nil
	}
	for _, service := range services {
		service := service
		go func() { results <- result{service: service.name, err: service.run(ctx)} }()
	}

	first := <-results
	cancel()
	allResults := []result{first}
	for range serviceCount - 1 {
		allResults = append(allResults, <-results)
	}
	for _, serviceResult := range allResults {
		if serviceResult.err != nil {
			return fmt.Errorf("%s service: %w", serviceResult.service, serviceResult.err)
		}
	}
	return nil
}

func (a *App) configureOptionalCatalogs(gameServer *game.Server, backend runtimeBackend) error {
	game.Client2012 = a.config.Client2012
	game.EventTopBanner = byte(a.config.EventTop)
	if a.config.ShopDataPath != "" {
		catalog, err := shopdata.Load(a.config.ShopDataPath)
		switch {
		case err == nil:
			// Without contents no ITEMPRICE override is sent, so the packs keep
			// their shopdata "not on sale" flag and stay hidden.
			if packs, ok := backend.(store.WPackContentStore); ok && a.config.WPackEnabled {
				a.attachWPackContents(catalog, packs)
			}
			gameServer.ConfigureShopCatalog(catalog)
			a.logger.Info("shop catalog loaded", "path", a.config.ShopDataPath,
				"items", catalog.ItemCount(), "options", catalog.OptionCount())
		case errors.Is(err, fs.ErrNotExist):
			a.logger.Warn(
				"optional shop catalog files are missing; server is starting with catalog validation disabled",
				"missing_files", missingShopCatalogFiles(a.config.ShopDataPath),
				"error", err,
			)
		default:
			return fmt.Errorf("shop catalog: %w", err)
		}
	}
	if a.config.LuckyBoxDataPath != "" {
		catalog, err := lootbox.Load(a.config.LuckyBoxDataPath)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			a.logger.Warn(
				"optional lucky-box catalog file is missing; server is starting with lucky boxes disabled",
				"file", a.config.LuckyBoxDataPath,
				"error", err,
			)
		case err != nil:
			return fmt.Errorf("lucky-box catalog: %w", err)
		default:
			configurer, ok := backend.(luckyBoxCatalogConfigurer)
			if !ok {
				return fmt.Errorf("configured backend does not support lucky-box catalogs")
			}
			configurer.ConfigureLuckyBoxCatalog(catalog)
			a.logger.Info("lucky-box catalog loaded", "path", a.config.LuckyBoxDataPath,
				"boxes", catalog.BoxCount(), "rewards", catalog.RewardCount())
		}
	}
	return nil
}

// attachWPackContents overlays the DB-defined W-Pack contents on the shop
// catalog. Failures leave the packs empty (the pre-2012 behaviour).
func (a *App) attachWPackContents(catalog *shopdata.Catalog, packs store.WPackContentStore) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	contents, err := packs.WPackContents(ctx)
	if err != nil {
		a.logger.Warn("W-Pack contents unavailable; packs stay empty", "error", err)
		return
	}
	converted := make(map[int][]shopdata.SetComponent, len(contents))
	for pack, components := range contents {
		for _, component := range components {
			converted[pack] = append(converted[pack], shopdata.SetComponent{ItemID: component.ItemID, Days: component.Days})
		}
	}
	if err := catalog.AttachWPackContents(converted); err != nil {
		a.logger.Warn("invalid W-Pack contents; packs stay empty", "error", err)
		return
	}
	a.logger.Info("W-Pack contents loaded", "packs", len(converted))
}

func missingShopCatalogFiles(shopPath string) []string {
	expected := []string{
		shopPath,
		filepath.Join(filepath.Dir(shopPath), "setitemdata.dat"),
	}
	missing := make([]string, 0, len(expected))
	for _, path := range expected {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			missing = append(missing, path)
		}
	}
	return missing
}

func brokerRelayPort(cfg config.Config) uint16 {
	if cfg.PeerEndpointMode == 2 {
		// Keep relay discovery on the game UDP socket, where peers establish
		// their initial Docker/NAT mapping. Once they join a room, its roster
		// advertises the room-scoped dedicated relay port for each peer.
		return uint16(cfg.GamePort)
	}
	return uint16(cfg.RelayPort)
}
