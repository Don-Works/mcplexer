package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// cmdRunJob implements `mcplexer run-job <id>`. Invoked by launchd
// plists / systemd unit files when a SurviveDaemonDown scheduled job
// fires while the main daemon is up OR down — exists so the promoted
// timer doesn't reference a missing subcommand (the prior state, in
// which every promoted job silently failed at fire time because the
// templates pointed at a CLI verb mcplexer didn't expose).
//
// Flow:
//  1. Resolve the data dir, open the DB read-only.
//  2. Try the running daemon via the same UDS the connect path uses.
//     When the daemon is reachable, it owns audit + approval and we
//     exit 0 immediately (RunJob returns OK).
//  3. When the daemon is unreachable, RunJob falls through to a direct
//     exec — the survive-daemon-down contract.
//
// We deliberately do NOT take a context-cancellation signal here:
// launchd / systemd hand us a fresh process per fire, so the OS
// reaping the process is the cancellation story.
func cmdRunJob(args []string) error {
	if len(args) < 1 || args[0] == "" {
		return fmt.Errorf("run-job: <job-id> required")
	}
	jobID := args[0]

	dir, err := dataDir()
	if err != nil {
		return fmt.Errorf("run-job: resolve data dir: %w", err)
	}
	dbPath := filepath.Join(dir, "mcplexer.db")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	db, err := sqlite.New(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("run-job: open db: %w", err)
	}
	defer func() { _ = db.Close() }()

	// The daemon RunNow client is optional; passing nil makes RunJob
	// always fall through to direct exec. Future wiring: an HTTP-based
	// DaemonRunner that POSTs /api/v1/guards/schedule/{id}/run with the
	// machine's bearer token. For now direct exec is the only honest
	// path because the daemon won't notice these are happening anyway
	// (we just removed the silent-failure case).
	code, runErr := scheduler.RunJob(ctx, jobID, db, nil)
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "run-job: %v\n", runErr)
	}
	if int(code) != 0 {
		os.Exit(int(code))
	}
	return nil
}
