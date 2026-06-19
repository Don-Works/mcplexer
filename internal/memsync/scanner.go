// Package memsync provides a background scanner that periodically checks
// harness-native memory directories for new or modified files and imports
// them into the mcplexer memory store. This is the safety net: even when
// a harness ignores the "use mcplexer memory" directive and writes to
// its native memory files, the scanner catches those writes and imports
// them so mcplexer stays the single source of truth.
package memsync

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/memory/harnessimport"
)

// Scanner periodically checks harness-native memory directories and
// imports new or modified files into the mcplexer memory store.
type Scanner struct {
	store    harnessimport.MemoryWriter
	homeDir  string
	interval time.Duration
	logger   *slog.Logger

	lastImported int
	lastSkipped  int
	stopCh       chan struct{}
	closeOnce    sync.Once
}

// NewScanner creates a Scanner. interval is how often to check; 0
// defaults to 5 minutes.
func NewScanner(s harnessimport.MemoryWriter, homeDir string, interval time.Duration, logger *slog.Logger) *Scanner {
	if interval == 0 {
		interval = 5 * time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Scanner{
		store:    s,
		homeDir:  homeDir,
		interval: interval,
		logger:   logger,
		stopCh:   make(chan struct{}),
	}
}

// Start begins the background scanning loop. Returns immediately.
func (s *Scanner) Start() {
	go s.loop()
}

// Stop signals the scanner to exit. Idempotent.
func (s *Scanner) Stop() {
	s.closeOnce.Do(func() { close(s.stopCh) })
}

// Stats returns the last scan's import counts.
func (s *Scanner) Stats() (imported, skipped int) {
	return s.lastImported, s.lastSkipped
}

func (s *Scanner) loop() {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	// Run once immediately on start
	s.scan()
	for {
		select {
		case <-ticker.C:
			s.scan()
		case <-s.stopCh:
			return
		}
	}
}

func (s *Scanner) scan() {
	ctx := context.Background()
	results, err := harnessimport.ImportAll(ctx, s.store, s.homeDir)
	if err != nil {
		s.logger.Warn("memsync: scan error", "error", err)
		return
	}
	totalImported, totalSkipped := 0, 0
	for _, r := range results {
		totalImported += r.Imported
		totalSkipped += r.Skipped
		if r.Imported > 0 {
			s.logger.Info("memsync: imported harness memory",
				"harness", r.Harness,
				"imported", r.Imported,
				"skipped", r.Skipped)
		}
		if len(r.Errors) > 0 {
			for _, e := range r.Errors {
				s.logger.Warn("memsync: import error",
					"harness", r.Harness, "error", e)
			}
		}
	}
	s.lastImported = totalImported
	s.lastSkipped = totalSkipped
}

// NewScannerFromEnv creates a scanner using environment variables:
//   - MCPLEXER_SYNC_INTERVAL: scan interval in minutes (default 5)
//   - MCPLEXER_SYNC_ENABLED: set to "0" to disable
func NewScannerFromEnv(s harnessimport.MemoryWriter) *Scanner {
	if os.Getenv("MCPLEXER_SYNC_ENABLED") == "0" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	interval := 5 * time.Minute
	// Could parse MCPLEXER_SYNC_INTERVAL here if needed
	return NewScanner(s, home, interval, nil)
}
