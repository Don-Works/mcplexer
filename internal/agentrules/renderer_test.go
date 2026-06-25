package agentrules

import (
	"strings"
	"testing"
)

// TestRenderV1Structure is a golden-ish test for Render(1). We assert
// structural invariants (markers, ≤80 lines, presence of every tool
// family + the precedence sentence) rather than byte-for-byte content,
// so editorial tweaks don't break the test — only structural
// regressions do.
func TestRenderV1Structure(t *testing.T) {
	out := Render(1)

	wantPrefixContains := "<!-- MCPLEXER:BEGIN v1 -->"
	if !strings.Contains(out, wantPrefixContains) {
		t.Errorf("Render(1) missing BEGIN marker: %q", out)
	}
	if !strings.HasSuffix(out, "<!-- MCPLEXER:END -->\n") {
		t.Errorf("Render(1) does not end with END marker + newline")
	}

	// Line budget: ≤80 lines per W1 spec.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) > 80 {
		t.Errorf("Render(1) is %d lines; budget is 80", len(lines))
	}

	mustContain := []string{
		"mcpx__",
		"mesh__",
		"task__",
		"memory__",
		"secret__",
		"skill__",
		"http://localhost:3333",
		"prefer mcpx",
		"versioned, mesh-shared, telemetered",
		"~/.claude/skills/",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("Render(1) missing required substring %q", s)
		}
	}
}

// TestRenderUnknownVersionFallsBack ensures asking for a future
// version doesn't blow up — defensive fallback to CurrentVersion's
// body. Marker still reflects the requested version so the dashboard
// can detect the "ahead of schema" state if needed.
func TestRenderUnknownVersionFallsBack(t *testing.T) {
	out := Render(99)
	if !strings.Contains(out, "<!-- MCPLEXER:BEGIN v99 -->") {
		t.Errorf("Render(99) should keep the v99 marker; got %q", out)
	}
	if !strings.Contains(out, "prefer mcpx") {
		t.Errorf("Render(99) should fall back to a v1+ body")
	}
}

// TestRenderV4HasTaskDiscipline pins the v4 contract: the new
// task-discipline section is present, the lifecycle is named, and
// the harness-reminder anti-pattern is called out by name. These are
// the load-bearing strings — editorial tweaks are fine as long as
// these survive.
func TestRenderV4HasTaskDiscipline(t *testing.T) {
	out := Render(4)
	mustContain := []string{
		"<!-- MCPLEXER:BEGIN v4 -->",
		"Task discipline",
		"ledger IS the work",
		"`review` IS a state",
		"task__list({state:\"open\"})",
		// v4 must inherit v3's surface.
		"Mesh ↔ task",
		"Slim tool surface",
		// Anti-pattern: harness reminder is explicitly to be ignored.
		"harness's session-local",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("Render(4) missing required substring %q", s)
		}
	}
}

// TestCurrentVersionIsV9 — guard rail so a future bump that forgets
// to add the matching contentVN switch case fails fast in tests
// rather than silently shipping a stale block.
func TestCurrentVersionIsV9(t *testing.T) {
	if CurrentVersion != 9 {
		t.Fatalf("CurrentVersion=%d; expected 9. If you bumped it, add the matching contentVN + test coverage.", CurrentVersion)
	}
}

// TestRenderV5HasClaimHeartbeatRelease pins the v5 contract: the
// lease-API section is present, names the three primitives, and
// teaches the disconnect-release server-side guarantee. v5 must also
// inherit everything from v4.
func TestRenderV5HasClaimHeartbeatRelease(t *testing.T) {
	out := Render(5)
	mustContain := []string{
		"<!-- MCPLEXER:BEGIN v5 -->",
		"Claim, heartbeat, release",
		"`task__claim`",
		"`task__heartbeat`",
		"Auto-release on disconnect",
		"ReleaseSessionTasks",
		// v5 must inherit v4's task-discipline rules.
		"Task discipline",
		"`review` IS a state",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("Render(5) missing required substring %q", s)
		}
	}
}

// TestRenderV6HasShellGuard pins the v6 contract: the Bash shell-guard
// section is present, names the hard-blocked metachar set + the source
// hook, and teaches the zero-cost workarounds. v6 must also inherit
// everything from v5.
func TestRenderV6HasShellGuard(t *testing.T) {
	out := Render(6)
	mustContain := []string{
		"<!-- MCPLEXER:BEGIN v6 -->",
		"shell-guard",
		"HARD-BLOCKED",
		"shell command contains metacharacter",
		"/v1/hooks/pretool",
		"separate Bash calls",
		"AllowMetachars",
		// v6 must inherit v5's lease API + v4's task discipline.
		"Claim, heartbeat, release",
		"Task discipline",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("Render(6) missing required substring %q", s)
		}
	}
}

// TestRenderV7HasDelegationFirst pins the v7 contract: generated agent rules
// tell agents to use mcplexer delegation as the default for token-heavy work.
func TestRenderV7HasDelegationFirst(t *testing.T) {
	out := Render(7)
	mustContain := []string{
		"<!-- MCPLEXER:BEGIN v7 -->",
		"Delegation-first",
		"Delegation is the default execution path",
		"`mcpx__delegate_worker`",
		"`mcpx__list_delegations`",
		"`mcpx__review_delegation`",
		"isolated git worktrees",
		"must not touch `~/.mcplexer/` directly",
		// v7 must inherit v6's shell guard.
		"shell-guard",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("Render(7) missing required substring %q", s)
		}
	}
}

// TestRenderV8HasMemoryContract pins the v8 contract: generated agent
// rules tell agents memory recall/capture is gateway-enforced via the
// session hook, names the recall-first/capture-last steps, and points at
// the source hook. v8 must inherit v7's delegation-first section.
func TestRenderV8HasMemoryContract(t *testing.T) {
	out := Render(8)
	mustContain := []string{
		"<!-- MCPLEXER:BEGIN v8 -->",
		"Memory contract",
		"recall first, capture last",
		"gateway-enforced",
		"/v1/hooks/session",
		"internal/api/hooks_session.go",
		"RECALL BEFORE ACTING",
		"CAPTURE AFTER",
		"memory.recall",
		"memory.save",
		// v8 must inherit v7's delegation-first + v6's shell guard.
		"Delegation-first",
		"shell-guard",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("Render(8) missing required substring %q", s)
		}
	}
}

// TestRenderV9HasBrowserAutomation pins the v9 contract: generated
// agent rules tell agents to prefer brw for browser control when the
// namespace is installed, and to fetch an installed browser skill for
// non-trivial browser workflows.
func TestRenderV9HasBrowserAutomation(t *testing.T) {
	out := Render(9)
	mustContain := []string{
		"<!-- MCPLEXER:BEGIN v9 -->",
		"Browser automation",
		"`brw`/browser tools first",
		"mcplexer browser-control surface",
		"browser skill",
		"mcpx.skill_search",
		"generic-browser-operator",
		"playwright-browser",
		"cmux-browser",
		// v9 must inherit v8's memory contract + v7's delegation-first.
		"Memory contract",
		"Delegation-first",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("Render(9) missing required substring %q", s)
		}
	}
}

func TestRenderDeterministic(t *testing.T) {
	a := Render(1)
	b := Render(1)
	if a != b {
		t.Errorf("Render is non-deterministic")
	}
}
