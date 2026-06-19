package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/don-works/mcplexer/internal/harnesssync"
)

// cmdHarness dispatches `mcplexer harness <sync|check|diff> [--harness H]`.
func cmdHarness(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mcplexer harness <sync|check|diff> [--harness %s]", harnessKeyUsage())
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "sync":
		return harnessSync(rest)
	case "check":
		return harnessCheck(rest)
	case "diff":
		return harnessDiff(rest)
	default:
		return fmt.Errorf("unknown harness subcommand %q (want sync|check|diff)", sub)
	}
}

func harnessSync(args []string) error {
	fs := flag.NewFlagSet("harness sync", flag.ContinueOnError)
	h := fs.String("harness", "", "harness key ("+harnessKeyUsage()+"); empty = all")
	home := fs.String("home", defaultHome(), "home dir to derive target paths")
	ver := fs.Int("version", 1, "registry version of using-mcplexer skill")
	if err := fs.Parse(args); err != nil {
		return err
	}
	keys := harnessKeys(*h)
	for _, k := range keys {
		changed, _, err := harnesssync.Install(*home, k, *ver)
		if err != nil {
			return fmt.Errorf("harness sync %s: %w", k, err)
		}
		p := harnesssync.TargetPath(*home, k)
		if changed {
			fmt.Printf("installed/updated %s (v%d) -> %s\n", k, *ver, p)
		} else {
			fmt.Printf("no change for %s (v%d) at %s\n", k, *ver, p)
		}
		if k == harnesssync.Claude {
			fmt.Printf("  (also wrote skill sidecar %s)\n", harnesssync.ClaudeSkillPath(*home))
			warnAccretion(harnesssync.DetectAccretion(*home))
		}
	}
	return nil
}

// warnAccretion prints the registry-bypass warning for local Claude
// skills/commands that re-bloat session context. Silent when clean.
func warnAccretion(acc harnesssync.AccretionReport) {
	if acc.Empty() {
		return
	}
	if n := len(acc.ExtraSkills); n > 0 {
		fmt.Printf("  WARNING: %d local skill dir(s) bypass the registry: %s\n", n, strings.Join(acc.ExtraSkills, ", "))
		fmt.Printf("           drain with: mcplexer migrate-skills --source <dir>, or archive/remove the local skill dirs\n")
	}
	if n := len(acc.ExtraCommands); n > 0 {
		fmt.Printf("  WARNING: %d local command file(s) bypass the registry: %s\n", n, strings.Join(acc.ExtraCommands, ", "))
		fmt.Printf("           drain with: mcplexer migrate-commands --source <dir>, or archive/remove the local command files\n")
	}
}

func harnessCheck(args []string) error {
	fs := flag.NewFlagSet("harness check", flag.ContinueOnError)
	h := fs.String("harness", "", "harness key or empty for all")
	home := fs.String("home", defaultHome(), "home dir")
	ver := fs.Int("version", 1, "expected registry version")
	if err := fs.Parse(args); err != nil {
		return err
	}
	keys := harnessKeys(*h)
	anyBad := false
	for _, k := range keys {
		st, err := harnesssync.Recheck(*home, k, *ver)
		if err != nil {
			return fmt.Errorf("check %s: %w", k, err)
		}
		p := harnesssync.TargetPath(*home, k)
		switch {
		case !st.BootstrapInstalled:
			fmt.Printf("%s: not installed (v%d) at %s\n", k, *ver, p)
			anyBad = true
		case st.Drifted:
			fmt.Printf("%s: drifted (installed v%d, want v%d) at %s\n", k, derefInt(st.BootstrapVersion), *ver, p)
			anyBad = true
		default:
			fmt.Printf("%s: up to date (v%d) at %s\n", k, derefInt(st.BootstrapVersion), p)
		}
		if st.Accretion != nil {
			warnAccretion(*st.Accretion)
		}
	}
	if anyBad {
		os.Exit(1)
	}
	return nil
}

func harnessDiff(args []string) error {
	fs := flag.NewFlagSet("harness diff", flag.ContinueOnError)
	h := fs.String("harness", "", "harness key")
	home := fs.String("home", defaultHome(), "home dir")
	ver := fs.Int("version", 1, "version to render")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *h == "" {
		return fmt.Errorf("--harness required for diff")
	}
	k := harnesssync.HarnessKey(*h)
	want := harnesssync.Render(k, *ver)
	p := harnesssync.TargetPath(*home, k)
	got, err := readHarnessBlockOrEmpty(p, k)
	if err != nil {
		return err
	}
	if want == got {
		fmt.Printf("no diff (v%d matches %s)\n", *ver, p)
		return nil
	}
	fmt.Printf("diff for %s (v%d):\n", k, *ver)
	fmt.Printf("--- installed\n+++ rendered v%d\n%s\n", *ver, unifiedSimpleDiff(got, want))
	return nil
}

func harnessKeys(h string) []harnesssync.HarnessKey {
	if h == "" {
		return harnesssync.AllKeys()
	}
	return []harnesssync.HarnessKey{harnesssync.HarnessKey(h)}
}

func harnessKeyUsage() string {
	keys := harnesssync.AllKeys()
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, string(k))
	}
	return strings.Join(parts, "|")
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func defaultHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func readHarnessBlockOrEmpty(path string, k harnesssync.HarnessKey) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	s := string(data)
	for _, pattern := range []string{
		fmt.Sprintf(`(?ms)^<!-- MCPLEXER:HARNESS-SYNC:BEGIN v\d+ \(%s\) -->.*?^<!-- MCPLEXER:HARNESS-SYNC:END -->`, regexp.QuoteMeta(string(k))),
		fmt.Sprintf(`(?ms)^# MCPLEXER:HARNESS-SYNC:BEGIN v\d+ \(%s\)\s*\n.*?^# MCPLEXER:HARNESS-SYNC:END`, regexp.QuoteMeta(string(k))),
	} {
		re := regexp.MustCompile(pattern)
		if m := re.FindString(s); m != "" {
			return strings.TrimRight(m, "\n") + "\n", nil
		}
	}
	return "", nil
}

func unifiedSimpleDiff(a, b string) string {
	return "- " + strings.ReplaceAll(a, "\n", "\n- ") + "\n+ " + strings.ReplaceAll(b, "\n", "\n+ ")
}
