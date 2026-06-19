//go:build linux

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

// systemdUnitPrefix scopes every promoted job under one filename
// namespace so collisions can't happen.
const systemdUnitPrefix = "mcplexer-scheduled-"

// systemdDriver promotes survive-daemon-down jobs into systemd-user
// timers via unit files under ~/.config/systemd/user/. Skeleton only —
// not exercised in CI on this branch.
type systemdDriver struct {
	unitDir      string
	binaryPath   string
	systemctlBin string
}

// SelectDriver returns the systemd driver on Linux.
func SelectDriver() Driver { return newSystemdDriver() }

func newSystemdDriver() Driver {
	home, err := os.UserHomeDir()
	if err != nil {
		return noopDriver{}
	}
	bin, _ := os.Executable()
	if bin == "" {
		bin = "mcplexer"
	}
	return &systemdDriver{
		unitDir:      filepath.Join(home, ".config", "systemd", "user"),
		binaryPath:   bin,
		systemctlBin: "systemctl",
	}
}

// Name returns the canonical driver name.
func (s *systemdDriver) Name() string { return "systemd_timer" }

// Available checks that systemctl is on PATH.
func (s *systemdDriver) Available() bool {
	if _, err := exec.LookPath(s.systemctlBin); err != nil {
		return false
	}
	return true
}

// Install writes the .service + .timer units and enables them.
// Idempotent: same job id overwrites + reload.
func (s *systemdDriver) Install(ctx context.Context, job store.ScheduledJob) (string, error) {
	if !s.Available() {
		return "", errDriverUnavailable
	}
	base := systemdUnitPrefix + sanitizeID(job.ID)
	svc := base + ".service"
	tmr := base + ".timer"
	if err := os.MkdirAll(s.unitDir, 0o755); err != nil {
		return "", fmt.Errorf("systemd unit dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.unitDir, svc), []byte(s.renderService(job)), 0o644); err != nil {
		return "", fmt.Errorf("write service: %w", err)
	}
	timerBody, err := s.renderTimer(job)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(s.unitDir, tmr), []byte(timerBody), 0o644); err != nil {
		return "", fmt.Errorf("write timer: %w", err)
	}
	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", "--now", tmr},
	} {
		if err := exec.CommandContext(ctx, s.systemctlBin, args...).Run(); err != nil {
			return base, fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
		}
	}
	return base, nil
}

// Uninstall stops + disables the timer and removes the units.
// Idempotent.
func (s *systemdDriver) Uninstall(ctx context.Context, nativeID string) error {
	if nativeID == "" {
		return nil
	}
	svc := nativeID + ".service"
	tmr := nativeID + ".timer"
	_ = exec.CommandContext(ctx, s.systemctlBin, "--user", "disable", "--now", tmr).Run()
	for _, p := range []string{tmr, svc} {
		if err := os.Remove(filepath.Join(s.unitDir, p)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	_ = exec.CommandContext(ctx, s.systemctlBin, "--user", "daemon-reload").Run()
	return nil
}

func (s *systemdDriver) renderService(job store.ScheduledJob) string {
	return "" +
		"[Unit]\n" +
		"Description=mcplexer scheduled job " + job.Name + "\n" +
		"\n" +
		"[Service]\n" +
		"Type=oneshot\n" +
		"ExecStart=" + s.binaryPath + " run-job " + job.ID + "\n"
}

func (s *systemdDriver) renderTimer(job store.ScheduledJob) (string, error) {
	header := "" +
		"[Unit]\n" +
		"Description=mcplexer timer " + job.Name + "\n" +
		"\n" +
		"[Timer]\n"
	body := ""
	switch job.Kind {
	case KindInterval:
		d, err := parseDurationFallback(job.Spec)
		if err != nil {
			return "", err
		}
		sec := int(d.Seconds())
		if sec < 1 {
			sec = 1
		}
		body = "OnUnitActiveSec=" + strconv.Itoa(sec) + "s\n" +
			"OnBootSec=" + strconv.Itoa(sec) + "s\n"
	case KindCron:
		// Translate the 5-field cron into one OnCalendar line. systemd's
		// OnCalendar accepts a superset of the cron syntax our parser
		// supports (`*`, literal, step). We validate first so a malformed
		// spec produces a clear error at Install time rather than a
		// timer that systemd silently refuses to load.
		if _, err := parseCronSpec(job.Spec); err != nil {
			return "", fmt.Errorf("cron %q: %w", job.Spec, err)
		}
		body = "OnCalendar=" + cronToOnCalendar(job.Spec) + "\nPersistent=true\n"
	default:
		body = "OnUnitActiveSec=60s\nOnBootSec=60s\n"
	}
	footer := "\n[Install]\nWantedBy=timers.target\n"
	return header + body + footer, nil
}

// cronToOnCalendar converts a 5-field Vixie cron spec into a systemd
// OnCalendar expression. systemd expects: DOW YYYY-MM-DD HH:MM:SS.
// Each field accepts `*`, a literal, or `start/step`. Our cron parser
// emits the same three forms, so this is a per-field rewrite.
//
// Day-of-week mapping: cron uses 0=Sunday..6=Saturday, systemd uses
// Mon..Sun. We use the abbreviated names systemd accepts; star-DOW is
// omitted entirely (systemd treats absent DOW as any).
func cronToOnCalendar(spec string) string {
	fields := strings.Fields(strings.TrimSpace(spec))
	if len(fields) != 5 {
		return spec // shouldn't happen — caller validated; fall through verbatim
	}
	dow := cronFieldToSystemdDOW(fields[4])
	prefix := ""
	if dow != "" {
		prefix = dow + " "
	}
	return prefix + "*-" +
		cronFieldToSystemd(fields[3]) + "-" + // Month
		cronFieldToSystemd(fields[2]) + " " + // Day-of-month
		cronFieldToSystemd(fields[1]) + ":" + // Hour
		cronFieldToSystemd(fields[0]) + ":00" // Minute
}

// cronFieldToSystemd rewrites `*`, `N`, or `*/N` into the systemd
// equivalent (`*`, `N`, `0/N`). Pad single-digit literals to two chars
// so the resulting OnCalendar string parses cleanly.
func cronFieldToSystemd(f string) string {
	f = strings.TrimSpace(f)
	if f == "*" {
		return "*"
	}
	if strings.HasPrefix(f, "*/") {
		return "0/" + f[2:]
	}
	if len(f) == 1 {
		return "0" + f
	}
	return f
}

// cronFieldToSystemdDOW renders the day-of-week field. Star → empty
// (systemd omits the DOW prefix). Literal N → abbreviated name. Step
// values get rendered as a comma list of names.
func cronFieldToSystemdDOW(f string) string {
	f = strings.TrimSpace(f)
	if f == "*" {
		return ""
	}
	names := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	if strings.HasPrefix(f, "*/") {
		step, err := strconv.Atoi(f[2:])
		if err != nil || step <= 0 {
			return ""
		}
		var picked []string
		for i := 0; i < 7; i += step {
			picked = append(picked, names[i])
		}
		return strings.Join(picked, ",")
	}
	n, err := strconv.Atoi(f)
	if err != nil || n < 0 || n > 6 {
		return ""
	}
	return names[n]
}
