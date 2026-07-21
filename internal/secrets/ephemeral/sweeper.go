package ephemeral

import (
	"context"
	"log/slog"
	"os"
	"time"
)

// sweepLoop runs every 30s and hard-deletes any file whose row has expired.
// Pending rows past expires_at are also transitioned to status=timeout.
func (m *Manager) sweepLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-t.C:
			m.sweepOnce(ctx)
		}
	}
}

// sweepOnce deletes every expired file. Pending rows are timed out.
func (m *Manager) sweepOnce(ctx context.Context) {
	now := time.Now().UTC()
	rows, err := m.store.ListExpiredSecretPrompts(ctx, now)
	if err != nil {
		slog.Warn("ephemeral sweep: list expired", "error", err)
		return
	}
	for _, row := range rows {
		if row.Status == "pending" {
			m.timeoutPrompt(ctx, row.ID)
			continue
		}
		if row.FilePath == "" {
			continue
		}
		if err := os.Remove(row.FilePath); err != nil && !os.IsNotExist(err) {
			slog.Warn("ephemeral sweep: remove file",
				"id", row.ID, "error", err)
		}
	}
}
