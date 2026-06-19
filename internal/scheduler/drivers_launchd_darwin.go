//go:build darwin

package scheduler

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// launchdLabelPrefix scopes every promoted job under one dotted-domain
// namespace so collisions with other tools can't happen and a sweep is
// always one launchctl wildcard.
const launchdLabelPrefix = "com.mcplexer.scheduled."

// launchdDriver installs scheduled jobs as user-scope launchd agents
// via plist files in ~/Library/LaunchAgents. Each job becomes one
// label; (re-)installing the same job id is idempotent — we rewrite
// the plist and bootstrap/bootout cleanly.
type launchdDriver struct {
	plistDir     string
	binaryPath   string
	launchctlBin string
}

// SelectDriver returns the launchd driver on macOS.
func SelectDriver() Driver { return newLaunchdDriver() }

func newLaunchdDriver() Driver {
	home, err := os.UserHomeDir()
	if err != nil {
		return noopDriver{}
	}
	bin, _ := os.Executable()
	if bin == "" {
		bin = "mcplexer" // resolve via PATH at fire time
	}
	return &launchdDriver{
		plistDir:     filepath.Join(home, "Library", "LaunchAgents"),
		binaryPath:   bin,
		launchctlBin: "/bin/launchctl",
	}
}

// Name returns the canonical driver name.
func (l *launchdDriver) Name() string { return "launchd_label" }

// Available checks that launchctl is reachable.
func (l *launchdDriver) Available() bool {
	if _, err := os.Stat(l.launchctlBin); err != nil {
		return false
	}
	return true
}

// Install writes the plist + bootstrap. Idempotent: same job id always
// returns the same label and a second call overwrites + reloads.
func (l *launchdDriver) Install(ctx context.Context, job store.ScheduledJob) (string, error) {
	if !l.Available() {
		return "", errDriverUnavailable
	}
	label := launchdLabelPrefix + sanitizeID(job.ID)
	plistPath := filepath.Join(l.plistDir, label+".plist")
	if err := os.MkdirAll(l.plistDir, 0o755); err != nil {
		return "", fmt.Errorf("launchd plist dir: %w", err)
	}
	body, err := l.renderPlist(label, job)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(plistPath, []byte(body), 0o644); err != nil {
		return "", fmt.Errorf("write plist: %w", err)
	}
	// Bootout silently if a previous version was loaded — ignore errors
	// since "not loaded" is the common case on first install.
	_ = l.bootout(ctx, label, plistPath)
	if err := l.bootstrap(ctx, plistPath); err != nil {
		return label, fmt.Errorf("launchctl bootstrap: %w", err)
	}
	return label, nil
}

// Uninstall bootouts the agent and removes the plist file. Idempotent.
func (l *launchdDriver) Uninstall(ctx context.Context, nativeID string) error {
	if nativeID == "" {
		return nil
	}
	plistPath := filepath.Join(l.plistDir, nativeID+".plist")
	_ = l.bootout(ctx, nativeID, plistPath)
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

func (l *launchdDriver) bootstrap(ctx context.Context, plistPath string) error {
	uid := strconv.Itoa(os.Getuid())
	return exec.CommandContext(ctx, l.launchctlBin, "bootstrap", "gui/"+uid, plistPath).Run()
}

func (l *launchdDriver) bootout(ctx context.Context, label, plistPath string) error {
	uid := strconv.Itoa(os.Getuid())
	// Prefer bootout-by-plist (paired with bootstrap); fall back to the
	// label form when the file is already gone.
	if _, err := os.Stat(plistPath); err == nil {
		return exec.CommandContext(ctx, l.launchctlBin, "bootout", "gui/"+uid, plistPath).Run()
	}
	return exec.CommandContext(ctx, l.launchctlBin, "bootout", "gui/"+uid+"/"+label).Run()
}

// renderPlist builds the launchd plist XML for one job. We delegate
// execution to `mcplexer run-job <id>` so the same code path handles
// the daemon-up + daemon-down cases.
func (l *launchdDriver) renderPlist(label string, job store.ScheduledJob) (string, error) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" `)
	b.WriteString(`"http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\"><dict>\n")
	b.WriteString("<key>Label</key><string>" + xmlEscape(label) + "</string>\n")
	b.WriteString("<key>ProgramArguments</key><array>\n")
	b.WriteString("  <string>" + xmlEscape(l.binaryPath) + "</string>\n")
	b.WriteString("  <string>run-job</string>\n")
	b.WriteString("  <string>" + xmlEscape(job.ID) + "</string>\n")
	b.WriteString("</array>\n")
	b.WriteString("<key>RunAtLoad</key><false/>\n")
	if err := writeLaunchdSchedule(&b, job); err != nil {
		return "", err
	}
	b.WriteString("</dict></plist>\n")
	return b.String(), nil
}

// writeLaunchdSchedule emits the StartInterval / StartCalendarInterval
// element depending on the job kind.
//
// KindInterval → StartInterval seconds.
// KindCron     → one or more StartCalendarInterval dicts. We expand */N
//
//	steps into N literal entries (launchd doesn't accept
//	step syntax inline). Fields with `*` are omitted from
//	each dict ("any"); fields with literal N emit one
//	entry per dict. Multi-step combos can produce a large
//	array — bounded by the per-field expansion (60 mins ×
//	24 hours = 1440 dicts max, which launchd handles fine).
//
// KindFileWatch / event-driven → fall back to a 60s polling interval
// so the in-process scheduler still ticks; these kinds aren't time-
// driven on launchd anyway, the FileWatcher fires from fsnotify.
func writeLaunchdSchedule(b *strings.Builder, job store.ScheduledJob) error {
	switch job.Kind {
	case KindInterval:
		d, err := parseDurationFallback(job.Spec)
		if err != nil {
			return err
		}
		sec := int(d.Seconds())
		if sec < 1 {
			sec = 1
		}
		b.WriteString("<key>StartInterval</key><integer>")
		b.WriteString(strconv.Itoa(sec))
		b.WriteString("</integer>\n")
	case KindCron:
		c, err := parseCronSpec(job.Spec)
		if err != nil {
			return fmt.Errorf("cron %q: %w", job.Spec, err)
		}
		writeLaunchdCalendarInterval(b, c)
	default:
		// Event-driven kinds — daemon must be the firing path.
		b.WriteString("<key>StartInterval</key><integer>60</integer>\n")
	}
	return nil
}

// writeLaunchdCalendarInterval expands a cronSpec into a launchd
// StartCalendarInterval array. Each emitted dict represents one
// fully-literal calendar match. We expand step fields (`*/N`) by
// enumerating every matching value; star fields are omitted from the
// dict so launchd treats them as "any".
//
// The cartesian product is bounded by the per-field domain size, so
// even worst-case `*/1 *` (every minute every hour) stays under launchd's
// dict-array limits.
func writeLaunchdCalendarInterval(b *strings.Builder, c cronSpec) {
	minutes := expandField(c.minute, 0, 59)
	hours := expandField(c.hour, 0, 23)
	doms := expandField(c.dom, 1, 31)
	months := expandField(c.month, 1, 12)
	dows := expandField(c.dow, 0, 6)

	b.WriteString("<key>StartCalendarInterval</key>\n<array>\n")
	for _, m := range minutes {
		for _, h := range hours {
			for _, d := range doms {
				for _, mo := range months {
					for _, w := range dows {
						b.WriteString("  <dict>\n")
						appendCalKey(b, "Minute", m)
						appendCalKey(b, "Hour", h)
						appendCalKey(b, "Day", d)
						appendCalKey(b, "Month", mo)
						appendCalKey(b, "Weekday", w)
						b.WriteString("  </dict>\n")
					}
				}
			}
		}
	}
	b.WriteString("</array>\n")
}

// expandField returns the values a cron field matches. A pure `*`
// returns a single sentinel (-1) meaning "omit"; literals return one
// value; step expands across the range; literal-N returns one value.
func expandField(f cronField, lo, hi int) []int {
	if f.star {
		if f.step <= 0 {
			return []int{-1}
		}
		out := make([]int, 0, (hi-lo)/f.step+1)
		for v := lo; v <= hi; v += f.step {
			out = append(out, v)
		}
		return out
	}
	return []int{f.val}
}

// appendCalKey emits one <key>Name</key><integer>v</integer> pair only
// when v is a real value (not the -1 "omit" sentinel from expandField).
func appendCalKey(b *strings.Builder, name string, v int) {
	if v < 0 {
		return
	}
	b.WriteString("    <key>")
	b.WriteString(name)
	b.WriteString("</key><integer>")
	b.WriteString(strconv.Itoa(v))
	b.WriteString("</integer>\n")
}

// sanitizeID moved to drivers.go so it's available to every build target
// (launchd label, systemd unit name).

func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}
