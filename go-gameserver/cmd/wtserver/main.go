package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"wtserver-go/internal/app"
	"wtserver-go/internal/config"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg, err := config.FromEnv()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("WT Server Go migration starting", "parity", "launcher,broker,game,match-udp,relay,active-f1-ai")
	if err := app.New(cfg, logger).Run(ctx); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
