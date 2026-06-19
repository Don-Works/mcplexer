package sqlite_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestScheduledJobCRUD smokes the ScheduledJobStore: create, get, list,
// update, due-query, delete, and ErrNotFound on a missing id.
func TestScheduledJobCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	next := time.Now().UTC().Add(-1 * time.Minute) // already due
	j := &store.ScheduledJob{
		ID:                "sj-1",
		Name:              "nightly-prune",
		Kind:              "cron",
		Spec:              "0 3 * * *",
		Command:           "/usr/local/bin/prune",
		ArgsJSON:          `["--all"]`,
		EnvJSON:           `{"RUST_LOG":"info"}`,
		CWD:               "/tmp",
		Surface:           "schedule",
		Enabled:           true,
		SurviveDaemonDown: true,
		NativeDriver:      "launchd_label",
		NativeID:          "com.example.prune",
		NextRunAt:         &next,
		LastStatus:        "",
	}
	if err := db.CreateScheduledJob(ctx, j); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := db.GetScheduledJob(ctx, j.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "nightly-prune" || !got.Enabled || !got.SurviveDaemonDown {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.ArgsJSON != `["--all"]` || got.EnvJSON != `{"RUST_LOG":"info"}` {
		t.Fatalf("json blobs not preserved: args=%q env=%q", got.ArgsJSON, got.EnvJSON)
	}
	if got.NextRunAt == nil {
		t.Fatal("next_run_at should be set")
	}

	list, err := db.ListScheduledJobs(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v len=%d", err, len(list))
	}

	got.LastStatus = "success"
	got.Enabled = false
	if err := db.UpdateScheduledJob(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := db.GetScheduledJob(ctx, j.ID)
	if got2.LastStatus != "success" || got2.Enabled {
		t.Fatalf("update not persisted: %+v", got2)
	}

	// Due query must skip disabled rows.
	due, err := db.DueScheduledJobs(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("disabled job should not be due, got %d", len(due))
	}

	// Re-enable and try again.
	got2.Enabled = true
	if err := db.UpdateScheduledJob(ctx, got2); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	due, err = db.DueScheduledJobs(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("due 2: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("expected 1 due, got %d", len(due))
	}

	if err := db.DeleteScheduledJob(ctx, j.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetScheduledJob(ctx, j.ID); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
	if _, err := db.GetScheduledJob(ctx, "nope"); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound for unknown id, got %v", err)
	}
}

// TestSanitizerMetaCRUD smokes the SanitizerMetaStore: upsert (insert
// path), get, upsert (update path), list, increment counters, and
// ErrNotFound on a missing (scope, scope_id).
func TestSanitizerMetaCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	m := &store.SanitizerMeta{
		ID:                "sm-1",
		Scope:             "tool",
		ScopeID:           "github__create_issue",
		DenylistEnabled:   true,
		EnvelopeEnabled:   true,
		ClassifierEnabled: false,
		ActionOnMatch:     "block",
	}
	if err := db.UpsertSanitizerMeta(ctx, m); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := db.GetSanitizerMeta(ctx, "tool", "github__create_issue")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.DenylistEnabled || !got.EnvelopeEnabled || got.ClassifierEnabled {
		t.Fatalf("bool round-trip mismatch: %+v", got)
	}
	if got.ActionOnMatch != "block" {
		t.Fatalf("action mismatch: %q", got.ActionOnMatch)
	}

	// Update path: same id, different action.
	m.ActionOnMatch = "envelope"
	m.ClassifierEnabled = true
	m.ClassifierModel = "haiku-4"
	if err := db.UpsertSanitizerMeta(ctx, m); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	got, err = db.GetSanitizerMeta(ctx, "tool", "github__create_issue")
	if err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if got.ActionOnMatch != "envelope" || !got.ClassifierEnabled ||
		got.ClassifierModel != "haiku-4" {
		t.Fatalf("update didn't stick: %+v", got)
	}

	// Counters.
	for _, c := range []string{"detected_count", "redacted_count", "blocked_count"} {
		if err := db.IncrementSanitizerCounter(ctx, "tool", "github__create_issue", c); err != nil {
			t.Fatalf("incr %s: %v", c, err)
		}
	}
	got, _ = db.GetSanitizerMeta(ctx, "tool", "github__create_issue")
	if got.DetectedCount != 1 || got.RedactedCount != 1 || got.BlockedCount != 1 {
		t.Fatalf("counters not bumped: %+v", got)
	}
	if got.LastEventAt == nil {
		t.Fatal("last_event_at should be set after incr")
	}

	// Invalid counter name must error.
	if err := db.IncrementSanitizerCounter(ctx, "tool", "github__create_issue", "bogus_count"); err == nil {
		t.Fatal("expected error for invalid counter name")
	}

	// Insert a second scope row to exercise list ordering.
	if err := db.UpsertSanitizerMeta(ctx, &store.SanitizerMeta{
		ID: "sm-2", Scope: "global", ScopeID: "", ActionOnMatch: "envelope",
	}); err != nil {
		t.Fatalf("upsert global: %v", err)
	}
	list, err := db.ListSanitizerMeta(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v len=%d", err, len(list))
	}

	if _, err := db.GetSanitizerMeta(ctx, "tool", "missing"); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestInstalledClientAndReceipts smokes both InstalledClient and
// InstallReceipt CRUD paths.
func TestInstalledClientAndReceipts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	installedAt := time.Now().UTC()
	c := &store.InstalledClient{
		ID:             "claude_code",
		Name:           "Claude Code",
		ConfigPath:     "/Users/x/.claude/mcp.json",
		Installed:      true,
		HooksInstalled: true,
		ShimInstalled:  false,
		SandboxEnabled: true,
		InstalledAt:    &installedAt,
	}
	if err := db.UpsertInstalledClient(ctx, c); err != nil {
		t.Fatalf("upsert client: %v", err)
	}
	got, err := db.GetInstalledClient(ctx, "claude_code")
	if err != nil {
		t.Fatalf("get client: %v", err)
	}
	if !got.Installed || !got.HooksInstalled || got.ShimInstalled || !got.SandboxEnabled {
		t.Fatalf("client bools mismatch: %+v", got)
	}
	if got.InstalledAt == nil {
		t.Fatal("installed_at should round-trip")
	}

	// Update via second upsert.
	c.ShimInstalled = true
	if err := db.UpsertInstalledClient(ctx, c); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	got, _ = db.GetInstalledClient(ctx, "claude_code")
	if !got.ShimInstalled {
		t.Fatalf("shim flag should now be true")
	}

	list, err := db.ListInstalledClients(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list clients: %v len=%d", err, len(list))
	}

	// Receipts.
	r1 := &store.InstallReceipt{
		ID:          "rcpt-1",
		ClientID:    "claude_code",
		Action:      "write_file",
		TargetPath:  "/Users/x/.claude/mcp.json",
		BackupPath:  "/tmp/mcp.json.bak",
		ReverseData: `{"key":"mcpServers.mcplexer"}`,
	}
	if err := db.CreateInstallReceipt(ctx, r1); err != nil {
		t.Fatalf("create receipt: %v", err)
	}
	r2 := &store.InstallReceipt{
		ID: "rcpt-2", ClientID: "claude_code",
		Action: "add_to_etc_shells", ReverseData: `{"line":"/opt/x"}`,
	}
	if err := db.CreateInstallReceipt(ctx, r2); err != nil {
		t.Fatalf("create receipt 2: %v", err)
	}

	rcpts, err := db.ListInstallReceipts(ctx, "claude_code", false)
	if err != nil || len(rcpts) != 2 {
		t.Fatalf("list receipts: %v len=%d", err, len(rcpts))
	}

	// Mark one reversed; default list should now hide it.
	if err := db.MarkReceiptReversed(ctx, "rcpt-1", ""); err != nil {
		t.Fatalf("mark reversed: %v", err)
	}
	rcpts, _ = db.ListInstallReceipts(ctx, "claude_code", false)
	if len(rcpts) != 1 {
		t.Fatalf("expected 1 active receipt, got %d", len(rcpts))
	}
	rcpts, _ = db.ListInstallReceipts(ctx, "claude_code", true)
	if len(rcpts) != 2 {
		t.Fatalf("expected 2 with includeReversed, got %d", len(rcpts))
	}

	// MarkReceiptReversed on missing id => ErrNotFound.
	if err := db.MarkReceiptReversed(ctx, "missing", ""); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := db.GetInstalledClient(ctx, "missing"); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound for missing client, got %v", err)
	}
}

// TestApprovalRuleCRUD smokes the ApprovalRuleStore including hit-count
// increments and surface filtering.
func TestApprovalRuleCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	expires := time.Now().UTC().Add(24 * time.Hour)
	r := &store.ApprovalRule{
		ID:          "ar-1",
		Surface:     "shell",
		Pattern:     "git status*",
		Directory:   "/Users/x/repo",
		AISessionID: "sess-abc",
		Decision:    "allow",
		Priority:    50,
		ExpiresAt:   &expires,
		CreatedBy:   "user",
	}
	if err := db.CreateApprovalRule(ctx, r); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := db.GetApprovalRule(ctx, "ar-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Pattern != "git status*" || got.Priority != 50 ||
		got.Decision != "allow" || got.ExpiresAt == nil {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	// Second rule on a different surface to exercise filter.
	if err := db.CreateApprovalRule(ctx, &store.ApprovalRule{
		ID: "ar-2", Surface: "mcp", Pattern: "github__*",
		Decision: "prompt", Priority: 100,
	}); err != nil {
		t.Fatalf("create 2: %v", err)
	}
	shell, err := db.ListApprovalRules(ctx, "shell")
	if err != nil || len(shell) != 1 {
		t.Fatalf("list shell: %v len=%d", err, len(shell))
	}
	all, err := db.ListApprovalRules(ctx, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("list all: %v len=%d", err, len(all))
	}

	// Update.
	got.Priority = 10
	got.Decision = "deny"
	if err := db.UpdateApprovalRule(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := db.GetApprovalRule(ctx, "ar-1")
	if got2.Priority != 10 || got2.Decision != "deny" {
		t.Fatalf("update not persisted: %+v", got2)
	}

	// Hit-count increment.
	hitAt := time.Now().UTC()
	if err := db.IncrementHitCount(ctx, "ar-1", hitAt); err != nil {
		t.Fatalf("incr hit: %v", err)
	}
	got2, _ = db.GetApprovalRule(ctx, "ar-1")
	if got2.HitCount != 1 || got2.LastHitAt == nil {
		t.Fatalf("hit not bumped: %+v", got2)
	}

	// Delete + ErrNotFound.
	if err := db.DeleteApprovalRule(ctx, "ar-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetApprovalRule(ctx, "ar-1"); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := db.DeleteApprovalRule(ctx, "missing"); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound on delete missing, got %v", err)
	}
	if err := db.IncrementHitCount(ctx, "missing", hitAt); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound on incr missing, got %v", err)
	}
}

// TestToolApprovalSurfaceRoundTrip ensures the new `surface` column added by
// migration 047 round-trips through Create → Get and Create → ListPending.
// Zero value is preserved (empty string) so pre-Guards callers stay
// source-compatible.
func TestToolApprovalSurfaceRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	cases := []struct {
		id      string
		surface string
	}{
		{"ta-surface-empty", ""},
		{"ta-surface-shell", "shell"},
		{"ta-surface-schedule", "schedule"},
		{"ta-surface-sanitizer", "sanitizer"},
	}

	for _, c := range cases {
		a := &store.ToolApproval{
			ID:               c.id,
			Status:           "pending",
			ToolName:         "test__tool",
			RequestSessionID: "sess-" + c.id,
			TimeoutSec:       60,
			Surface:          c.surface,
		}
		if err := db.CreateToolApproval(ctx, a); err != nil {
			t.Fatalf("create %s: %v", c.id, err)
		}
		got, err := db.GetToolApproval(ctx, c.id)
		if err != nil {
			t.Fatalf("get %s: %v", c.id, err)
		}
		if got.Surface != c.surface {
			t.Errorf("%s: Surface = %q, want %q", c.id, got.Surface, c.surface)
		}
	}

	pending, err := db.ListPendingApprovals(ctx)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	seen := map[string]string{}
	for _, a := range pending {
		seen[a.ID] = a.Surface
	}
	for _, c := range cases {
		if got, ok := seen[c.id]; !ok {
			t.Errorf("%s missing from ListPendingApprovals", c.id)
		} else if got != c.surface {
			t.Errorf("ListPendingApprovals %s: Surface = %q, want %q", c.id, got, c.surface)
		}
	}
}

// TestToolApprovalEnvelopeRoundTrip exercises the three envelope columns
// added by migration 081 (originating_workspace, kind, summary) for
// cross-boundary share approvals. Validates round-trip through Create →
// Get and that an empty kind survives a round-trip unchanged (so legacy
// MCP tool-call rows continue to render).
func TestToolApprovalEnvelopeRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	cases := []struct {
		id                   string
		kind                 string
		originatingWorkspace string
		summary              string
	}{
		{"ta-env-empty", "", "", ""},
		{"ta-env-skill", "skill_share", "ws-alpha", "share skill spec-project v3"},
		{"ta-env-memory", "memory_share", "ws-beta", "share memory \"vercel best practices\""},
		{"ta-env-task", "task_offer", "ws-alpha", "offer task: ship 0.21 release"},
		{"ta-env-direct", "mesh_direct", "ws-beta", "Hey can you run X?"},
		{"ta-env-consent", "mesh_grant_consent", "ws-alpha", "Granted mesh.skill_request to peer 12D3..."},
	}

	for _, c := range cases {
		a := &store.ToolApproval{
			ID:                   c.id,
			Status:               "pending",
			ToolName:             "share__test",
			RequestSessionID:     "sess-" + c.id,
			TimeoutSec:           60,
			Kind:                 c.kind,
			OriginatingWorkspace: c.originatingWorkspace,
			Summary:              c.summary,
		}
		if err := db.CreateToolApproval(ctx, a); err != nil {
			t.Fatalf("create %s: %v", c.id, err)
		}
		got, err := db.GetToolApproval(ctx, c.id)
		if err != nil {
			t.Fatalf("get %s: %v", c.id, err)
		}
		if got.Kind != c.kind {
			t.Errorf("%s: Kind = %q, want %q", c.id, got.Kind, c.kind)
		}
		if got.OriginatingWorkspace != c.originatingWorkspace {
			t.Errorf("%s: OriginatingWorkspace = %q, want %q",
				c.id, got.OriginatingWorkspace, c.originatingWorkspace)
		}
		if got.Summary != c.summary {
			t.Errorf("%s: Summary = %q, want %q", c.id, got.Summary, c.summary)
		}
	}

	pending, err := db.ListPendingApprovals(ctx)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	byID := map[string]store.ToolApproval{}
	for _, a := range pending {
		byID[a.ID] = a
	}
	for _, c := range cases {
		got, ok := byID[c.id]
		if !ok {
			t.Errorf("%s missing from ListPendingApprovals", c.id)
			continue
		}
		if got.Kind != c.kind || got.Summary != c.summary ||
			got.OriginatingWorkspace != c.originatingWorkspace {
			t.Errorf("%s envelope mismatch in list: got kind=%q origin=%q summary=%q",
				c.id, got.Kind, got.OriginatingWorkspace, got.Summary)
		}
	}
}

// TestToolApprovalEnvelopeJSONShape locks the JSON envelope so the UI
// can rely on snake_case field names and omitempty for legacy rows.
func TestToolApprovalEnvelopeJSONShape(t *testing.T) {
	// Populated share row.
	share := store.ToolApproval{
		ID:                   "share-1",
		Status:               "pending",
		Kind:                 "skill_share",
		OriginatingWorkspace: "ws-alpha",
		Summary:              "share skill foo v2",
	}
	buf, err := json.Marshal(share)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		`"kind":"skill_share"`,
		`"originating_workspace":"ws-alpha"`,
		`"summary":"share skill foo v2"`,
	} {
		if !strings.Contains(string(buf), want) {
			t.Errorf("share JSON missing %s; got %s", want, string(buf))
		}
	}

	// Legacy row — three envelope fields should be omitted entirely.
	legacy := store.ToolApproval{
		ID:       "legacy-1",
		Status:   "pending",
		ToolName: "github__get_issue",
	}
	lbuf, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, banned := range []string{`"kind"`, `"originating_workspace"`, `"summary"`} {
		if strings.Contains(string(lbuf), banned) {
			t.Errorf("legacy JSON unexpectedly contains %s; got %s", banned, string(lbuf))
		}
	}
}
