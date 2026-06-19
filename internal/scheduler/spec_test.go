package scheduler

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestIsManualSpec(t *testing.T) {
	cases := []struct {
		spec string
		want bool
	}{
		{"manual", true},
		{"Manual", true},
		{"  manual  ", true},
		{"MANUAL", true},
		{"", false},
		{"manual-ish", false},
		{"5m", false},
		{"*/5 * * * *", false},
	}
	for _, c := range cases {
		if got := IsManualSpec(c.spec); got != c.want {
			t.Errorf("IsManualSpec(%q) = %v, want %v", c.spec, got, c.want)
		}
	}
}

func TestNextRunInterval(t *testing.T) {
	base := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		spec string
		want time.Duration
	}{
		{"30s", 30 * time.Second},
		{"5m", 5 * time.Minute},
		{"1h", 1 * time.Hour},
	}
	for _, c := range cases {
		got, err := NextRun(KindInterval, c.spec, base)
		if err != nil {
			t.Fatalf("%s: unexpected err: %v", c.spec, err)
		}
		if !got.Equal(base.Add(c.want)) {
			t.Errorf("%s: got %v want %v", c.spec, got, base.Add(c.want))
		}
	}
}

func TestNextRunIntervalErrors(t *testing.T) {
	base := time.Now().UTC()
	for _, spec := range []string{"", "0s", "-5m", "five minutes"} {
		if _, err := NextRun(KindInterval, spec, base); err == nil {
			t.Errorf("expected error for %q", spec)
		}
	}
}

func TestNextRunCronStar(t *testing.T) {
	// * * * * * (every minute)
	base := time.Date(2026, 5, 20, 12, 0, 30, 0, time.UTC)
	got, err := NextRun(KindCron, "* * * * *", base)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := time.Date(2026, 5, 20, 12, 1, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestNextRunCronFixedDaily(t *testing.T) {
	// 0 3 * * * — daily at 03:00 UTC
	base := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	got, err := NextRun(KindCron, "0 3 * * *", base)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := time.Date(2026, 5, 21, 3, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestNextRunCronStep(t *testing.T) {
	// */5 * * * * — every 5 minutes
	base := time.Date(2026, 5, 20, 12, 1, 0, 0, time.UTC)
	got, err := NextRun(KindCron, "*/5 * * * *", base)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := time.Date(2026, 5, 20, 12, 5, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestNextRunCronDayOfWeek(t *testing.T) {
	// 0 9 * * 1 — Mondays at 09:00. 2026-05-20 is a Wednesday;
	// next Monday is 2026-05-25.
	base := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	got, err := NextRun(KindCron, "0 9 * * 1", base)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := time.Date(2026, 5, 25, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestNextRunCronErrors(t *testing.T) {
	cases := []string{
		"",
		"too many fields here for cron",
		"a b c d e",
		"0 25 * * *",  // hour out of range
		"60 * * * *",  // minute out of range
		"*/0 * * * *", // zero step
	}
	for _, spec := range cases {
		if _, err := NextRun(KindCron, spec, time.Now().UTC()); err == nil {
			t.Errorf("expected error for cron %q", spec)
		}
	}
}

func TestNextRunEventDriven(t *testing.T) {
	for _, kind := range []string{KindFileWatch, KindGitHook} {
		_, err := NextRun(kind, "ignored", time.Now())
		if !errors.Is(err, ErrEventDrivenKind) {
			t.Errorf("%s: want ErrEventDrivenKind, got %v", kind, err)
		}
	}
}

func TestNextRunUnknownKind(t *testing.T) {
	_, err := NextRun("flux", "x", time.Now())
	if err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Errorf("want unknown-kind error, got %v", err)
	}
}

// TestNextRunWorker is the regression guard for the scheduled-worker
// fire-once bug: NextRun must resolve KindWorker specs (cron OR interval)
// so fire()/persistTerminal can re-arm the heap. Previously KindWorker hit
// the default case and errored, leaving NextRunAt nil after the first fire.
func TestNextRunWorker(t *testing.T) {
	base := time.Date(2026, 5, 20, 12, 0, 30, 0, time.UTC)
	// cron spec
	got, err := NextRun(KindWorker, "* * * * *", base)
	if err != nil {
		t.Fatalf("worker cron: unexpected err: %v", err)
	}
	if want := time.Date(2026, 5, 20, 12, 1, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("worker cron next: got %v want %v", got, want)
	}
	// interval spec
	got, err = NextRun(KindWorker, "5m", base)
	if err != nil {
		t.Fatalf("worker interval: unexpected err: %v", err)
	}
	if want := base.Add(5 * time.Minute); !got.Equal(want) {
		t.Errorf("worker interval next: got %v want %v", got, want)
	}
	// garbage spec still errors
	if _, err := NextRun(KindWorker, "not-a-spec", base); err == nil {
		t.Error("worker garbage spec: expected error")
	}
}
