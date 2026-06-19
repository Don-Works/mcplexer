package scheduler

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Supported job kinds — declared as constants so cross-package callers
// can reference them without typo-prone string literals.
const (
	KindCron      = "cron"
	KindInterval  = "interval"
	KindFileWatch = "file_watch"
	KindGitHook   = "git_hook"
	// KindWorker schedules a mcplexer Worker (in-process AI agent). The
	// ScheduledJob.WorkerID column points to the Worker row; the scheduler
	// dispatches to the worker runner instead of execing j.Command.
	KindWorker = "worker"
	// KindAuditPrune is the built-in retention job that deletes old
	// audit_records + worker_runs. Spec uses cron semantics. Dispatch
	// branches to the wired PruneExecutor instead of execing a shell
	// command — there is no external process; the work is one DELETE
	// statement per table.
	KindAuditPrune = "audit_prune"
)

// SpecManual is the sentinel ScheduleSpec value that means "no
// scheduler firing — this job runs only via event triggers (mesh
// dispatch, manual RunNow, etc.)". Admin validators accept it
// without parsing and the workers schedule bridge skips creating a
// scheduled_jobs row (or deletes any pre-existing one) so the heap
// never picks it up.
const SpecManual = "manual"

// IsManualSpec reports whether spec is the manual sentinel
// (whitespace-trimmed, case-insensitive).
func IsManualSpec(spec string) bool {
	return strings.EqualFold(strings.TrimSpace(spec), SpecManual)
}

// ErrEventDrivenKind is returned by NextRun for kinds (file_watch,
// git_hook) that don't have a fixed time-based next-run. Callers must
// treat these as "not scheduled by the heap" and rely on the event
// source (M4 — fsnotify, git-hook shims) instead.
var ErrEventDrivenKind = errors.New("scheduler: kind has no time-based next run")

// NextRun returns the next scheduled time after `after` for a job of
// the given kind+spec.
//
// Supported subset (intentionally small — extended in M3.5):
//   - kind="cron" — five fields (minute hour day-of-month month day-of-week).
//     Each field: "*", a single number, or "*/N" stepping. No ranges,
//     no lists — anything else returns a parse error.
//   - kind="interval" — Go time.Duration string ("5m" / "1h" / "30s").
//   - kind="file_watch" / "git_hook" — returns the zero Time and
//     ErrEventDrivenKind so the scheduler skips them in the heap.
func NextRun(kind, spec string, after time.Time) (time.Time, error) {
	switch kind {
	case KindCron, KindAuditPrune:
		// audit_prune uses cron semantics — the spec column carries
		// a normal cron expression.
		return nextCronRun(spec, after)
	case KindInterval:
		return nextIntervalRun(spec, after)
	case KindWorker:
		// Worker-kind jobs carry EITHER a cron expression or a Go duration
		// ("5m") in their spec — mirror the schedule bridge's resolution
		// (cron first, interval fallback) so fire() and persistTerminal can
		// compute the NEXT run and re-arm the heap. Without this case,
		// KindWorker fell through to the default below and errored, so a
		// scheduled worker fired EXACTLY ONCE and then never re-armed
		// (NextRunAt got nilled on every fire).
		if t, cerr := nextCronRun(spec, after); cerr == nil {
			return t, nil
		}
		return nextIntervalRun(spec, after)
	case KindFileWatch, KindGitHook:
		return time.Time{}, ErrEventDrivenKind
	default:
		return time.Time{}, fmt.Errorf("scheduler: unknown kind %q", kind)
	}
}

func nextIntervalRun(spec string, after time.Time) (time.Time, error) {
	d, err := time.ParseDuration(strings.TrimSpace(spec))
	if err != nil {
		return time.Time{}, fmt.Errorf("interval spec %q: %w", spec, err)
	}
	if d <= 0 {
		return time.Time{}, fmt.Errorf("interval spec %q: must be > 0", spec)
	}
	return after.Add(d).UTC(), nil
}

// cronField is one parsed 5-field column.
type cronField struct {
	star bool
	step int // 0 = no step (single value or "*")
	val  int // when !star && step==0, the literal value
}

func (f cronField) match(v int) bool {
	if f.star {
		if f.step <= 1 {
			return true
		}
		return v%f.step == 0
	}
	return v == f.val
}

// parseCronField parses one of: "*", "N", or "*/N".
// minVal/maxVal bound the literal-N form.
func parseCronField(s string, minVal, maxVal int) (cronField, error) {
	s = strings.TrimSpace(s)
	if s == "*" {
		return cronField{star: true}, nil
	}
	if strings.HasPrefix(s, "*/") {
		n, err := strconv.Atoi(s[2:])
		if err != nil || n <= 0 {
			return cronField{}, fmt.Errorf("invalid step %q", s)
		}
		return cronField{star: true, step: n}, nil
	}
	// "N" — single literal value.
	n, err := strconv.Atoi(s)
	if err != nil {
		return cronField{}, fmt.Errorf("invalid field %q", s)
	}
	if n < minVal || n > maxVal {
		return cronField{}, fmt.Errorf("field %q out of range [%d,%d]", s, minVal, maxVal)
	}
	return cronField{val: n}, nil
}

type cronSpec struct {
	minute, hour, dom, month, dow cronField
}

func parseCronSpec(spec string) (cronSpec, error) {
	fields := strings.Fields(strings.TrimSpace(spec))
	if len(fields) != 5 {
		return cronSpec{}, fmt.Errorf("cron spec must have 5 fields, got %d", len(fields))
	}
	var c cronSpec
	var err error
	if c.minute, err = parseCronField(fields[0], 0, 59); err != nil {
		return c, fmt.Errorf("minute: %w", err)
	}
	if c.hour, err = parseCronField(fields[1], 0, 23); err != nil {
		return c, fmt.Errorf("hour: %w", err)
	}
	if c.dom, err = parseCronField(fields[2], 1, 31); err != nil {
		return c, fmt.Errorf("day-of-month: %w", err)
	}
	if c.month, err = parseCronField(fields[3], 1, 12); err != nil {
		return c, fmt.Errorf("month: %w", err)
	}
	if c.dow, err = parseCronField(fields[4], 0, 6); err != nil {
		return c, fmt.Errorf("day-of-week: %w", err)
	}
	return c, nil
}

// nextCronRun walks forward minute-by-minute from `after+1min` until a
// matching slot is found. Bounded by 366*24*60 iterations so a wholly
// unsatisfiable spec returns an error instead of looping forever.
func nextCronRun(spec string, after time.Time) (time.Time, error) {
	c, err := parseCronSpec(spec)
	if err != nil {
		return time.Time{}, err
	}
	// Start at next minute boundary in UTC.
	t := after.UTC().Add(time.Minute).Truncate(time.Minute)
	const maxSteps = 366 * 24 * 60
	for i := 0; i < maxSteps; i++ {
		if cronMatches(c, t) {
			return t, nil
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("cron spec %q has no match within a year", spec)
}

// cronMatches returns true when t satisfies every field of c. The
// day-of-month and day-of-week semantics follow the classic Vixie cron
// rule: when both are non-"*", a match in EITHER is sufficient. When
// one is "*" the other must match.
func cronMatches(c cronSpec, t time.Time) bool {
	if !c.minute.match(t.Minute()) {
		return false
	}
	if !c.hour.match(t.Hour()) {
		return false
	}
	if !c.month.match(int(t.Month())) {
		return false
	}
	domStar := c.dom.star && c.dom.step <= 1
	dowStar := c.dow.star && c.dow.step <= 1
	domHit := c.dom.match(t.Day())
	dowHit := c.dow.match(int(t.Weekday()))
	switch {
	case domStar && dowStar:
		return true
	case domStar:
		return dowHit
	case dowStar:
		return domHit
	default:
		return domHit || dowHit
	}
}
