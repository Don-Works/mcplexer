package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/control"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func cmdControlServer() error {
	ctx, cancel := signal.NotifyContext(
		context.Background(), syscall.SIGINT, syscall.SIGTERM,
	)
	defer cancel()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	logWriter := buildSlogWriter(cfg.LogPath, loadLogRotationConfig(cfg.ConfigFile))
	base := slog.NewJSONHandler(logWriter, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	})
	logger := slog.New(audit.NewContextHandler(base))
	slog.SetDefault(logger)

	db, err := sqlite.New(ctx, cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	readOnly := os.Getenv("MCPLEXER_CONTROL_READONLY") != "false"
	srv := control.New(db, readOnly)
	return srv.RunStdio(ctx)
}
