package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/don-works/mcplexer/internal/agentrules"
	"github.com/don-works/mcplexer/internal/config"
)

// cmdRules dispatches `mcplexer rules <sync|check|diff>`. Wired from
// main.go's switch. Keeps the dispatch table tight — the bulk of the
// surface lives in the agentrules package.
func cmdRules(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mcplexer rules <sync|check|diff> [--path PATH] [--version N]")
	}
	sub := args[0]
	rest := args[1:]

	switch sub {
	case "sync":
		return rulesSync(rest)
	case "check":
		return rulesCheck(rest)
	case "diff":
		return rulesDiff(rest)
	default:
		return fmt.Errorf("unknown rules subcommand %q (want sync|check|diff)", sub)
	}
}

func rulesSync(args []string) error {
	fs := flag.NewFlagSet("rules sync", flag.ContinueOnError)
	path := fs.String("path", defaultRulesPath(), "path to the agent rules file")
	version := fs.Int("version", agentrules.CurrentVersion, "block version to render")
	if err := fs.Parse(args); err != nil {
		return err
	}

	existed := fileExists(*path)
	changed, err := agentrules.SyncWithDashboard(*path, *version, resolveDashboardURL())
	if err != nil {
		return fmt.Errorf("sync %s: %w", *path, err)
	}

	switch {
	case !changed:
		fmt.Printf("no change (%s already at v%d)\n", *path, *version)
	case !existed:
		fmt.Printf("created %s with mcplexer block v%d\n", *path, *version)
	default:
		fmt.Printf("updated %s to v%d\n", *path, *version)
	}
	return nil
}

func rulesCheck(args []string) error {
	fs := flag.NewFlagSet("rules check", flag.ContinueOnError)
	path := fs.String("path", defaultRulesPath(), "path to the agent rules file")
	version := fs.Int("version", agentrules.CurrentVersion, "block version to compare against")
	if err := fs.Parse(args); err != nil {
		return err
	}

	present, current, upToDate, err := agentrules.Status(*path, *version)
	if err != nil {
		return fmt.Errorf("check %s: %w", *path, err)
	}

	switch {
	case !present:
		fmt.Printf("%s: mcplexer block not installed (latest v%d)\n", *path, *version)
		os.Exit(1)
	case !upToDate:
		fmt.Printf("%s: mcplexer block out of date (installed v%d, latest v%d)\n", *path, current, *version)
		os.Exit(1)
	default:
		fmt.Printf("%s: mcplexer block up to date (v%d)\n", *path, current)
	}
	return nil
}

func rulesDiff(args []string) error {
	fs := flag.NewFlagSet("rules diff", flag.ContinueOnError)
	path := fs.String("path", defaultRulesPath(), "path to the agent rules file")
	version := fs.Int("version", agentrules.CurrentVersion, "block version to compare against")
	if err := fs.Parse(args); err != nil {
		return err
	}

	want := agentrules.Render(*version)
	got, err := readBlockOrEmpty(*path)
	if err != nil {
		return err
	}

	if want == got {
		fmt.Printf("no diff (v%d block matches what's installed at %s)\n", *version, *path)
		return nil
	}

	fmt.Println(unifiedDiff(got, want, "installed", fmt.Sprintf("rendered v%d", *version)))
	return nil
}

// readBlockOrEmpty extracts the currently-installed block (markers
// included) so the diff has something concrete to render. Returns
// empty string when the file is missing or marker-less; the diff then
// shows "added all lines".
func readBlockOrEmpty(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	// Reuse the same regex the writer uses via a tiny re-extract. We
	// could expose it from agentrules, but keeping the API surface tight
	// (Render/Sync/Status) — diff is a CLI convenience.
	const begin = "<!-- MCPLEXER:BEGIN v"
	const end = "<!-- MCPLEXER:END -->"
	s := string(data)
	bi := strings.Index(s, begin)
	if bi < 0 {
		return "", nil
	}
	ei := strings.Index(s[bi:], end)
	if ei < 0 {
		return "", nil
	}
	return s[bi:bi+ei+len(end)] + "\n", nil
}

// unifiedDiff is a tiny line-diff renderer — sufficient for showing a
// human "here's what would change". Not a full unified-diff
// implementation (no hunk headers / line numbers); for the dashboard
// + CLI use case the +/-  per-line shape is what matters. Pulling in
// a real diff library would be overkill for ≤80-line blocks.
func unifiedDiff(a, b, aLabel, bLabel string) string {
	var out strings.Builder
	out.WriteString("--- " + aLabel + "\n")
	out.WriteString("+++ " + bLabel + "\n")

	aLines := splitLinesKeepEmpty(a)
	bLines := splitLinesKeepEmpty(b)
	// LCS-free naive diff: emit all "a" lines as removed, all "b" lines
	// as added. Good enough — block bodies are short, the user mostly
	// wants to know "is it different and what's the new version".
	for _, l := range aLines {
		out.WriteString("- " + l + "\n")
	}
	for _, l := range bLines {
		out.WriteString("+ " + l + "\n")
	}
	return out.String()
}

func splitLinesKeepEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

// resolveDashboardURL points the synced block at the port the running daemon
// actually bound (from its published runtime descriptor) or a configured
// public URL, falling back to the compiled default when nothing is known.
func resolveDashboardURL() string {
	cfg, err := loadConfig()
	if err != nil {
		return ""
	}
	httpAddr, publicURL := cfg.HTTPAddr, cfg.PublicURL
	if info, err := config.ReadRuntimeInfo(filepath.Dir(cfg.DBDSN)); err == nil && info != nil {
		if info.HTTPAddr != "" {
			httpAddr = info.HTTPAddr
		}
		if info.PublicURL != "" {
			publicURL = info.PublicURL
		}
	}
	return config.DashboardURL(httpAddr, publicURL)
}

func defaultRulesPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "CLAUDE.md")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
