package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/don-works/mcplexer/internal/config"
	"gopkg.in/natefinch/lumberjack.v2"
)

// loadLogRotationConfig reads the log_rotation block from mcplexer.yaml at
// configPath and merges it with DefaultLogRotationConfig. Missing files,
// parse errors, and missing block all fall back to defaults silently — log
// rotation is best-effort and should never block daemon boot.
func loadLogRotationConfig(configPath string) LogRotationConfig {
	cfg := DefaultLogRotationConfig()
	if configPath == "" {
		return cfg
	}
	if _, err := os.Stat(configPath); err != nil {
		return cfg
	}
	file, err := config.LoadFile(configPath)
	if err != nil {
		return cfg
	}
	if file.LogRotation == nil {
		return cfg
	}
	if file.LogRotation.MaxSizeMB > 0 {
		cfg.MaxSizeMB = file.LogRotation.MaxSizeMB
	}
	if file.LogRotation.MaxBackups > 0 {
		cfg.MaxBackups = file.LogRotation.MaxBackups
	}
	if file.LogRotation.MaxAgeDays > 0 {
		cfg.MaxAgeDays = file.LogRotation.MaxAgeDays
	}
	if file.LogRotation.Compress != nil {
		cfg.Compress = *file.LogRotation.Compress
	}
	return cfg
}

// buildSlogWriter returns the io.Writer slog should write through.
// When logPath is non-empty the writer is a rotation-aware lumberjack
// wrapper; the file is chmod'd to 0600 on first open. When empty the
// caller falls back to os.Stderr.
//
// On failure (e.g. directory missing) the function logs a single warning
// via the default slog and returns os.Stderr so we never silently lose
// logs — the daemon still emits to launchd's StandardErrorPath as a
// safety net.
func buildSlogWriter(logPath string, cfg LogRotationConfig) io.Writer {
	if logPath == "" {
		return os.Stderr
	}
	w, err := openRotatingLog(logPath, cfg)
	if err != nil {
		// Best-effort: fall back to stderr so logs aren't lost. We log
		// via slog.Default since SetDefault hasn't happened yet —
		// stderr will catch it under launchd's StandardErrorPath.
		slog.Warn("log rotation init failed; falling back to stderr",
			"path", logPath, "error", err)
		return os.Stderr
	}
	return w
}

// LogRotationConfig captures the size/age/retention knobs for the on-disk
// log writer. Defaults are conservative for a developer-laptop daemon:
// 50MB max size per file, 5 backups, 30 day retention, gzip-compressed.
// All fields are overridable via mcplexer.yaml (log_rotation:) or env vars.
type LogRotationConfig struct {
	MaxSizeMB  int  `yaml:"max_size_mb"`
	MaxBackups int  `yaml:"max_backups"`
	MaxAgeDays int  `yaml:"max_age_days"`
	Compress   bool `yaml:"compress"`
}

// DefaultLogRotationConfig returns the baseline rotation policy. Used when
// no overrides exist in mcplexer.yaml.
func DefaultLogRotationConfig() LogRotationConfig {
	return LogRotationConfig{
		MaxSizeMB:  50,
		MaxBackups: 5,
		MaxAgeDays: 30,
		Compress:   true,
	}
}

// openRotatingLog ensures the log file exists with 0600 perms, then returns
// a rotation-aware writer wrapping it. The caller is responsible for closing
// the returned io.WriteCloser.
//
// Two-step open is deliberate:
//  1. Create the file with 0600 if it doesn't yet exist (lumberjack's own
//     create uses 0644 which is world-readable — log lines can include peer
//     IDs, paths, and imperfectly-redacted errors so we tighten this).
//  2. If the file already exists with looser perms (e.g. from a prior
//     0644-era daemon), chmod it down defensively. launchd reuses existing
//     perms when StandardOutPath already exists, so once we tighten it once
//     it stays tight across restarts.
func openRotatingLog(logPath string, cfg LogRotationConfig) (io.WriteCloser, error) {
	if err := ensureLogFile(logPath); err != nil {
		return nil, err
	}
	return &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    cfg.MaxSizeMB,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAgeDays,
		Compress:   cfg.Compress,
	}, nil
}

// ensureLogFile creates logPath with 0600 if missing and tightens an existing
// file's mode if looser. Returns nil on success. Idempotent — safe to call
// on every daemon start.
func ensureLogFile(logPath string) error {
	// Best-effort create-with-0600. If the file already exists, OpenFile
	// is a no-op on perms; we Stat + Chmod below to tighten any legacy
	// 0644 file from a pre-fix daemon.
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("ensure log file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close log file after ensure: %w", err)
	}

	st, err := os.Stat(logPath)
	if err != nil {
		return fmt.Errorf("stat log file: %w", err)
	}
	// Mask off the type bits — only the permission bits matter.
	if st.Mode().Perm() != 0600 {
		if err := os.Chmod(logPath, 0600); err != nil {
			return fmt.Errorf("chmod log file to 0600: %w", err)
		}
	}
	return nil
}
