package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// savedSearchEvalInterval is how often the evaluator scans enabled saved
// searches. 1 minute matches the lease-sweep cadence — frequent enough to
// catch a threshold cross promptly, cheap enough on a quiet gateway (one
// COUNT(*) per enabled search). Per-search debounce (last_fired_at vs
// window) prevents re-firing within a search's own window.
const savedSearchEvalInterval = time.Minute

// auditSavedSearchLoop periodically evaluates persisted audit saved
// searches; when a search's match count over its rolling window crosses
// its threshold it publishes a Signal-tray notification. Non-fatal:
// errors are logged and the loop continues. When bus is nil the evaluator
// still runs (stamping last_fired_at) but no notification is emitted.
func auditSavedSearchLoop(ctx context.Context, db *sqlite.DB, bus *notify.Bus) {
	t := time.NewTicker(savedSearchEvalInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			c, cancel := context.WithTimeout(ctx, 30*time.Second)
			fired, err := db.EvaluateSavedSearches(c, now.UTC())
			cancel()
			if err != nil {
				slog.Warn("audit saved-search eval failed", "error", err)
				continue
			}
			for _, f := range fired {
				slog.Info("audit saved-search fired",
					"id", f.Search.ID, "name", f.Search.Name, "count", f.Count)
				if bus != nil {
					bus.Publish(savedSearchEvent(f, now.UTC()))
				}
			}
		}
	}
}

// savedSearchEvent maps a fired saved search to a notify.Event. Link deep-
// links into the audit page filtered by the saved search's query.
func savedSearchEvent(f store.FiredSavedSearch, now time.Time) notify.Event {
	link := "/audit"
	if f.Search.Q != "" {
		link = "/audit?q=" + url.QueryEscape(f.Search.Q)
	}
	return notify.Event{
		MessageID: fmt.Sprintf("audit-saved-search:%s:%d", f.Search.ID, now.Unix()),
		Source:    "system",
		AgentName: "audit",
		Kind:      "alert",
		Priority:  "high",
		Title:     "Saved search fired: " + f.Search.Name,
		Body: fmt.Sprintf("%d matching audit records in the last %ds (threshold %d).",
			f.Count, f.Search.WindowSec, f.Search.ThresholdCount),
		Link:      link,
		CreatedAt: now,
	}
}
