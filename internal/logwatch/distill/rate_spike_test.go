package distill

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/collect"
	"github.com/don-works/mcplexer/internal/store"
)

// errorLinesAt builds n distinct error-class lines (same masked
// template, different db attempt numbers) all timestamped at ts.
func errorLinesAt(ts time.Time, n int) []collect.Line {
	lines := make([]collect.Line, n)
	for i := range lines {
		lines[i] = collect.Line{TS: ts, Text: fmt.Sprintf("ERROR pgx: connection refused host=db-%d attempt=%d", i, i)}
	}
	return lines
}

func mustIngest(t *testing.T, d *Distiller, src *store.LogSource, host *store.RemoteHost, lines []collect.Line) {
	t.Helper()
	if err := d.Ingest(context.Background(), src, host, lines); err != nil {
		t.Fatal(err)
	}
}

func assertNotifyCount(t *testing.T, notifier *captureNotifier, want int, msg string) {
	t.Helper()
	if got := len(notifier.notes); got != want {
		t.Fatalf("%s: got %d notifications, want %d", msg, got, want)
	}
}

// TestDistiller_RateSpike walks the hysteresis latch through all four
// required transitions with a controllable clock: below threshold (no
// fire), first spike (fire), repeat suppression (still elevated, no
// re-fire), and recovery + re-arm (rate drops, then spikes again).
// The known-error template is pre-seeded so the unrelated new-template
// detector (fireAnomaly) never fires here — this test is scoped to the
// rate-spike latch alone.
func TestDistiller_RateSpike(t *testing.T) {
	fs := &fakeDistillStore{}
	notifier := &captureNotifier{}
	var clock time.Time
	d := &Distiller{store: fs, notifier: notifier, now: func() time.Time { return clock }}

	src := &store.LogSource{ID: "s1", WorkspaceID: "ws", Name: "api",
		Kind: store.LogSourceKindDocker, RetentionDays: 7, RetentionMB: 50}
	host := &store.RemoteHost{ID: "h1", Name: "ip-prod-1", SSHHost: "10.0.0.1"}
	t0 := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)

	tplID := TemplateID(src.ID, Normalize("ERROR pgx: connection refused host=db-0 attempt=0"))
	fs.templates = map[string]int64{tplID: 1}
	fs.templateSev = map[string]string{tplID: store.SeverityError}

	clock = t0
	mustIngest(t, d, src, host, errorLinesAt(t0, 9))
	assertNotifyCount(t, notifier, 0, "9 lines is below the min-count floor")

	clock = t0.Add(time.Minute)
	mustIngest(t, d, src, host, errorLinesAt(clock, 15))
	assertNotifyCount(t, notifier, 1, "24 lines against a zero baseline must spike")
	if got := notifier.notes[0].TemplateID; got != "ratespike:s1" {
		t.Fatalf("spike key: got %q, want ratespike:s1", got)
	}

	clock = t0.Add(2 * time.Minute)
	mustIngest(t, d, src, host, errorLinesAt(clock, 1))
	assertNotifyCount(t, notifier, 1, "sustained spike must not re-fire")

	// 30 minutes on, every earlier error line has aged out of the 5m
	// current window; an info line keeps Ingest non-empty without
	// adding to the error tally.
	clock = t0.Add(30 * time.Minute)
	mustIngest(t, d, src, host, []collect.Line{{TS: clock, Text: "GET /healthz 200"}})
	assertNotifyCount(t, notifier, 1, "recovery must not notify")
	if active, err := fs.GetLogSourceErrorSpikeActive(context.Background(), src.ID); err != nil || active {
		t.Fatalf("latch should clear on recovery: active=%v err=%v", active, err)
	}

	// A fresh burst re-crosses the floor; the 25 earlier lines are now
	// purely baseline (25/60min), well under the >5x re-arm floor.
	clock = t0.Add(31 * time.Minute)
	mustIngest(t, d, src, host, errorLinesAt(clock, 15))
	assertNotifyCount(t, notifier, 2, "re-armed latch must spike again")
}

func TestDistiller_RateSpikeFailedDeliveryDoesNotArmLatch(t *testing.T) {
	storeFake := &fakeDistillStore{}
	notifier := &captureNotifier{err: errors.New("channel unavailable")}
	clock := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	distiller := &Distiller{store: storeFake, notifier: notifier, now: func() time.Time { return clock }}
	source := &store.LogSource{ID: "s1", WorkspaceID: "ws", Name: "api",
		Kind: store.LogSourceKindDocker, RetentionDays: 7, RetentionMB: 50}
	host := &store.RemoteHost{ID: "h1", Name: "prod", SSHHost: "10.0.0.1"}
	templateID := TemplateID(source.ID, Normalize("ERROR pgx: connection refused host=db-0 attempt=0"))
	storeFake.templates = map[string]int64{templateID: 1}
	storeFake.templateSev = map[string]string{templateID: store.SeverityError}

	mustIngest(t, distiller, source, host, errorLinesAt(clock, rateSpikeMinCount))
	if active, _ := storeFake.GetLogSourceErrorSpikeActive(context.Background(), source.ID); active {
		t.Fatal("failed notification armed the spike latch")
	}
	notifier.err = nil
	clock = clock.Add(time.Minute)
	mustIngest(t, distiller, source, host, errorLinesAt(clock, 1))
	if active, _ := storeFake.GetLogSourceErrorSpikeActive(context.Background(), source.ID); !active {
		t.Fatal("successful retry did not arm the spike latch")
	}
	if len(notifier.notes) != 2 {
		t.Fatalf("notification attempts=%d want=2", len(notifier.notes))
	}
}
