package triggers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/store"
)

// ErrNoCrontab signals that `crontab -l` reported no entries for the
// current user. Not an error condition — the wizard surfaces it so
// callers can show "no crontab to import".
var ErrNoCrontab = errors.New("import: no crontab for current user")

// ErrUnsupportedOS is returned by platform-specific From* helpers when
// invoked on a kernel they don't apply to.
var ErrUnsupportedOS = errors.New("import: unsupported OS for this source")

// ImportWizard parses existing crontab entries, launchd plists, and
// systemd timers and proposes corresponding ScheduledJob rows. It NEVER
// writes them — returns Candidates the caller confirms one by one.
type ImportWizard struct {
	home string
	// run executes a sub-process and returns its stdout. Replaced in
	// tests with a stub that returns canned fixtures.
	run commandRunner
	// launchAgentsDir overrides the default ~/Library/LaunchAgents path
	// so tests can point the wizard at a tmpdir.
	launchAgentsDir string
	// systemdUserDir overrides the default XDG systemd user dir.
	systemdUserDir string
}

// commandRunner is the seam tests use to inject crontab output without
// shelling out.
type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Candidate is a proposed ScheduledJob plus its source so the wizard
// can present "this came from your crontab line 4" in the UI.
type Candidate struct {
	Job    store.ScheduledJob
	Source ImportSource
	// Warning, when non-empty, flags a candidate the wizard could
	// parse partially but not fully — e.g. an OnCalendar systemd
	// expression we don't translate. UI should show the warning and
	// pre-fill Spec="" so the human re-enters it.
	Warning string
}

// ImportSource identifies where a Candidate was lifted from.
type ImportSource struct {
	Kind    string // "crontab" | "launchd" | "systemd"
	Path    string // file path or launchd label
	Line    int    // 1-based for crontab; 0 elsewhere
	Excerpt string // raw line / snippet for the UI
}

// NewImportWizard anchors a wizard at the given home directory. Empty
// `home` resolves to the current user's home dir.
func NewImportWizard(home string) *ImportWizard {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	w := &ImportWizard{home: home, run: realRun}
	w.launchAgentsDir = filepath.Join(home, "Library", "LaunchAgents")
	w.systemdUserDir = filepath.Join(home, ".config", "systemd", "user")
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		w.systemdUserDir = filepath.Join(xdg, "systemd", "user")
	}
	return w
}

func realRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// FromUserCrontab reads `crontab -l` and returns one Candidate per
// executable schedule line. Skips comments and blank lines. Returns
// ErrNoCrontab when no crontab is set for the current user.
func (w *ImportWizard) FromUserCrontab(ctx context.Context) ([]Candidate, error) {
	out, err := w.run(ctx, "crontab", "-l")
	if err != nil {
		if isNoCrontabError(err) {
			return nil, ErrNoCrontab
		}
		return nil, fmt.Errorf("import: crontab -l: %w", err)
	}
	var cands []Candidate
	for i, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cand, ok := parseCrontabLine(trimmed, i+1)
		if !ok {
			continue
		}
		cands = append(cands, cand)
	}
	return cands, nil
}

// isNoCrontabError detects the "no crontab for X" exit-1 case so the
// caller can branch on it without inspecting stderr.
func isNoCrontabError(err error) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if strings.Contains(strings.ToLower(string(ee.Stderr)), "no crontab") {
			return true
		}
		return ee.ExitCode() == 1
	}
	// crontab binary missing is treated the same — "nothing to import".
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	return false
}

// parseCrontabLine splits a single non-comment crontab line into a
// Candidate. Returns (zero, false) when the line has fewer than 6
// whitespace-separated fields.
//
// KNOWN LIMITATION: argument splitting uses strings.Fields, so quoted
// args (`echo "hello world"`) are split on whitespace. Full POSIX
// shell quoting is its own project — flagged in the report.
func parseCrontabLine(line string, lineNum int) (Candidate, bool) {
	fields := strings.Fields(line)
	if len(fields) < 6 {
		return Candidate{}, false
	}
	spec := strings.Join(fields[:5], " ")
	cmd := fields[5]
	var argv []string
	if len(fields) > 6 {
		argv = append(argv, fields[6:]...)
	}
	return Candidate{
		Job: store.ScheduledJob{
			Name:     fmt.Sprintf("crontab-%d", lineNum),
			Kind:     scheduler.KindCron,
			Spec:     spec,
			Command:  cmd,
			Surface:  "schedule",
			Enabled:  true,
			ArgsJSON: marshalArgv(argv),
		},
		Source: ImportSource{
			Kind:    "crontab",
			Line:    lineNum,
			Excerpt: line,
		},
	}, true
}

// FromLaunchdUser walks ~/Library/LaunchAgents and returns one
// Candidate per plist whose StartInterval or StartCalendarInterval
// schedules it. macOS-only.
func (w *ImportWizard) FromLaunchdUser(ctx context.Context) ([]Candidate, error) {
	if runtime.GOOS != "darwin" {
		return nil, ErrUnsupportedOS
	}
	return scanDirForCandidates(w.launchAgentsDir, ".plist", w.candidateFromPlist)
}

// FromSystemdUser walks $XDG_CONFIG_HOME/systemd/user (or the default)
// and returns one Candidate per .timer file. Linux-only.
func (w *ImportWizard) FromSystemdUser(ctx context.Context) ([]Candidate, error) {
	if runtime.GOOS != "linux" {
		return nil, ErrUnsupportedOS
	}
	return scanDirForCandidates(w.systemdUserDir, ".timer", w.candidateFromTimer)
}

// scanDirForCandidates is the shared walker for launchd + systemd.
// Returns nil (no candidates) when the directory doesn't exist — that
// just means the user has no agents/timers configured.
func scanDirForCandidates(
	dir, suffix string, parse func(path string) (Candidate, bool, error),
) ([]Candidate, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("import: read %s: %w", dir, err)
	}
	var out []Candidate
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		cand, ok, perr := parse(path)
		if perr != nil {
			// Don't fail the whole scan on one bad plist — surface a
			// warning Candidate so the human knows we tried.
			out = append(out, Candidate{
				Source:  ImportSource{Kind: kindFromSuffix(suffix), Path: path, Excerpt: perr.Error()},
				Warning: perr.Error(),
			})
			continue
		}
		if !ok {
			continue
		}
		out = append(out, cand)
	}
	return out, nil
}

func kindFromSuffix(suffix string) string {
	if suffix == ".plist" {
		return "launchd"
	}
	return "systemd"
}
