package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// cmdBrw dispatches `mcplexer brw <subcommand>`.
func cmdBrw(args []string) error {
	if len(args) < 1 || args[0] == "" {
		return fmt.Errorf("usage: mcplexer brw sync [flags]")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "sync":
		return cmdBrwSync(rest)
	default:
		return fmt.Errorf("unknown brw subcommand %q (expected: sync)", sub)
	}
}

// multiFlag collects a repeatable, optionally comma-separated string flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*m = append(*m, p)
		}
	}
	return nil
}

func cmdBrwSync(args []string) error {
	fs := flag.NewFlagSet("brw sync", flag.ContinueOnError)
	from := fs.String("from", "", "read `brwctl daemons` JSON from <file> or '-' for stdin; empty execs 'brwctl daemons'")
	apply := fs.Bool("apply", false, "apply changes (default: dry-run, write nothing)")
	dryRun := fs.Bool("dry-run", false, "explicitly request a dry-run (the default; wins over --apply)")
	prune := fs.Bool("prune", false, "delete source=brw servers/routes absent from the input")
	brwdPath := fs.String("brwd-path", "", "path to the brwd binary (default: live brw install path)")
	policy := fs.String("policy", "", "path to browser-profiles policy json (--profile-policy)")
	dbPath := fs.String("db", defaultDataPath("mcplexer.db"), "sqlite database path")
	jsonOut := fs.Bool("json", false, "emit the plan as JSON")
	var workspaces multiFlag
	fs.Var(&workspaces, "workspace", "target workspace id for routes (repeatable or comma-separated)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	daemons, err := loadBrwDaemons(*from)
	if err != nil {
		return err
	}

	ctx := context.Background()
	db, err := sqlite.New(ctx, *dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	svc := config.NewService(db)
	plan, err := config.SyncBrwProfiles(ctx, svc, db, daemons, config.SyncOptions{
		DryRun:     *dryRun || !*apply,
		Workspaces: workspaces,
		BrwdPath:   *brwdPath,
		PolicyPath: *policy,
		Prune:      *prune,
	})
	if err != nil {
		return err
	}

	if *jsonOut {
		out, merr := json.MarshalIndent(plan, "", "  ")
		if merr != nil {
			return fmt.Errorf("marshal plan: %w", merr)
		}
		fmt.Println(string(out))
		return nil
	}

	printBrwPlan(plan, len(daemons), workspaces)
	return nil
}

// loadBrwDaemons reads the `brwctl daemons` JSON array from a file, stdin
// ("-"), or by executing `brwctl daemons` when from is empty. The exec +
// parse helpers live in internal/config so the gateway boot wiring shares
// exactly one roster loader with this CLI.
func loadBrwDaemons(from string) ([]config.BrwDaemon, error) {
	switch from {
	case "":
		return config.LoadBrwRoster(context.Background(), "")
	case "-":
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, err
		}
		return config.ParseBrwDaemons(raw)
	default:
		raw, err := os.ReadFile(from)
		if err != nil {
			return nil, err
		}
		return config.ParseBrwDaemons(raw)
	}
}

func printBrwPlan(plan config.SyncPlan, daemonCount int, workspaces []string) {
	mode := "apply"
	if plan.DryRun {
		mode = "dry-run"
	}
	wsLabel := "none"
	if len(workspaces) > 0 {
		wsLabel = strings.Join(workspaces, ", ")
	}
	fmt.Printf("brw sync (%s) — %d daemon(s), workspaces: [%s]\n\n", mode, daemonCount, wsLabel)

	if len(plan.Actions) == 0 {
		fmt.Println("  (no actions)")
		return
	}

	for _, a := range plan.Actions {
		line := fmt.Sprintf("  %-9s %-6s %s", strings.ToUpper(a.Action), a.Kind, a.ID)
		if a.Namespace != "" {
			line += fmt.Sprintf("  ns=%s", a.Namespace)
		}
		if a.Detail != "" {
			line += fmt.Sprintf("  (%s)", a.Detail)
		}
		fmt.Println(line)
	}

	fmt.Println()
	fmt.Println("Summary:")
	for _, s := range summarizeBrwPlan(plan) {
		fmt.Printf("  %s\n", s)
	}
	if plan.DryRun {
		fmt.Println("\n(dry-run — no changes written; re-run with --apply to persist)")
	}
}

// summarizeBrwPlan returns stable, sorted "N action kind" count lines.
func summarizeBrwPlan(plan config.SyncPlan) []string {
	counts := map[string]int{}
	for _, a := range plan.Actions {
		counts[a.Action+" "+a.Kind] = counts[a.Action+" "+a.Kind] + 1
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%d %s", counts[k], k))
	}
	return out
}
