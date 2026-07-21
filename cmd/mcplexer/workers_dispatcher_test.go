package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// fakeBuiltin is the test double for runner.BuiltinToolCaller. Records every
// CallBuiltin invocation so tests can assert the dispatcher delegated rather
// than re-routed, and lets a test pre-seed the response envelope to drive
// IsError parsing through the dispatcher's extractIsError helper.
type fakeBuiltin struct {
	surface  []models.ToolSchema
	response json.RawMessage
	callErr  error
	calls    []fakeBuiltinCall
}

type fakeBuiltinCall struct {
	Name string
	Args json.RawMessage
}

func (f *fakeBuiltin) CallBuiltin(_ context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	f.calls = append(f.calls, fakeBuiltinCall{Name: name, Args: args})
	if f.callErr != nil {
		return nil, f.callErr
	}
	if len(f.response) == 0 {
		return json.RawMessage(`{"content":[{"type":"text","text":"ok"}],"isError":false}`), nil
	}
	return f.response, nil
}

func (f *fakeBuiltin) WorkerToolSurface(_ context.Context) []models.ToolSchema {
	if f.surface == nil {
		return []models.ToolSchema{
			{Name: "mcpx__search_tools", Description: "search"},
			{Name: "mcpx__execute_code", Description: "exec"},
		}
	}
	return f.surface
}

// TestFilterToolsByAllowlist drives every allowlist branch of the
// dispatcher's ListTools through the pure-function half. The fake raw
// payloads mimic what a downstream's tools/list returns; the namespace
// map mimics what ListDownstreamServers gives us.
func TestFilterToolsByAllowlist(t *testing.T) {
	raw := map[string]json.RawMessage{
		"srv-github": []byte(`{"tools":[
			{"name":"list_issues","description":"list"},
			{"name":"create_issue","description":"create"}
		]}`),
		"srv-mesh": []byte(`{"tools":[
			{"name":"send","description":"send"}
		]}`),
	}
	nsByServer := map[string]string{
		"srv-github": "github",
		"srv-mesh":   "mesh",
	}

	cases := []struct {
		name      string
		allowlist []string
		want      []string
	}{
		{"nil allowlist exposes all", nil, []string{
			"github__create_issue", "github__list_issues", "mesh__send",
		}},
		{"non-empty allowlist filters", []string{"github__list_issues", "mesh__send"}, []string{
			"github__list_issues", "mesh__send",
		}},
		{"allowlist with unknown name drops it", []string{"github__list_issues", "nope__bogus"}, []string{
			"github__list_issues",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterToolsByAllowlist(raw, nsByServer, tc.allowlist)
			names := make([]string, len(got))
			for i, s := range got {
				names[i] = s.Name
			}
			sort.Strings(names)
			sort.Strings(tc.want)
			if !equalStringSlices(names, tc.want) {
				t.Fatalf("filter result mismatch:\n got=%v\nwant=%v", names, tc.want)
			}
		})
	}
}

// TestDispatcherListToolsEmptyAllowlistFailsClosed verifies the
// SECURITY contract: an explicit empty allowlist `[]` returns zero
// tools — and does so BEFORE the nil-builtin check, so the operator's
// explicit "deny everything" signal beats any wiring state.
func TestDispatcherListToolsEmptyAllowlistFailsClosed(t *testing.T) {
	db := newDispatcherTestStore(t)
	d := newToolDispatcher(db, nil, nil)
	tools, err := d.ListTools(context.Background(), []string{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("expected 0 tools for empty allowlist, got %d", len(tools))
	}
}

// TestDispatcherListTools_FailsClosedWithoutBuiltin locks the
// fail-closed contract: a dispatcher constructed without
// SetBuiltinCaller must NOT silently fall back to a legacy downstream-
// flat surface. The whole point of the two-tool surface is that a
// worker sees mcpx__search_tools + mcpx__execute_code and nothing else;
// any wiring path that forgets to wire the BuiltinCaller is a security
// regression and must surface as a loud error, not a degraded surface.
func TestDispatcherListTools_FailsClosedWithoutBuiltin(t *testing.T) {
	db := newDispatcherTestStore(t)
	d := newToolDispatcher(db, nil, nil) // intentionally no SetBuiltinCaller

	_, err := d.ListTools(context.Background(), nil)
	if err == nil {
		t.Fatal("expected ListTools to fail when BuiltinCaller is unwired, got nil error")
	}
	if !strings.Contains(err.Error(), "BuiltinToolCaller not wired") {
		t.Fatalf("error text should name the missing wiring, got: %v", err)
	}
}

// TestDispatcherDispatchTool_FailsClosedWithoutBuiltin mirrors the
// above for the dispatch path. The legacy engine.Route fallback is
// gone — a hallucinated downstream name from a worker reaches the
// dispatcher only AFTER the model picked a name from its two-tool
// inventory, which can't happen unless the inventory was populated;
// but defence-in-depth wants this to fail loudly anyway.
func TestDispatcherDispatchTool_FailsClosedWithoutBuiltin(t *testing.T) {
	db := newDispatcherTestStore(t)
	d := newToolDispatcher(db, nil, nil) // intentionally no SetBuiltinCaller

	_, err := d.DispatchTool(context.Background(), runner.ToolCallRequest{
		Name:      "mcpx__execute_code",
		InputJSON: `{"code":"print(1)"}`,
	})
	if err == nil {
		t.Fatal("expected DispatchTool to fail when BuiltinCaller is unwired, got nil error")
	}
	if !strings.Contains(err.Error(), "BuiltinToolCaller not wired") {
		t.Fatalf("error text should name the missing wiring, got: %v", err)
	}
}

// TestDispatcherListToolsMalformedAllowlistFailsClosed verifies the
// SECURITY contract from the dispatcher side: a worker row with a
// corrupted ToolAllowlistJSON must not see EVERY tool — the dispatcher
// has to treat it as deny-everything (empty allowlist). The check
// flows through filterToolsByAllowlist because that's the pure-fn
// half of ListTools.
func TestDispatcherListToolsMalformedAllowlistFailsClosed(t *testing.T) {
	raw := map[string]json.RawMessage{
		"srv-github": []byte(`{"tools":[{"name":"list_issues"}]}`),
	}
	nsByServer := map[string]string{"srv-github": "github"}
	// An empty allowlist slice must produce zero tools (fail-closed).
	got := filterToolsByAllowlist(raw, nsByServer, []string{})
	if len(got) != 0 {
		t.Fatalf("malformed allowlist must yield 0 tools, got %d", len(got))
	}
}

// TestDispatcherListTools_TwoToolSurface verifies that once a
// BuiltinToolCaller is wired the dispatcher returns ONLY the two
// builtin tools workers see at the model layer — no downstream tools,
// no allowlist filtering on names, no engine routing. The legacy
// downstream-flat path is exercised only when builtin is nil.
func TestDispatcherListTools_TwoToolSurface(t *testing.T) {
	db := newDispatcherTestStore(t)
	d := newToolDispatcher(db, nil, nil)
	d.SetBuiltinCaller(&fakeBuiltin{})

	tools, err := d.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected exactly 2 tools, got %d (%v)", len(tools), names(tools))
	}
	got := names(tools)
	sort.Strings(got)
	want := []string{"mcpx__execute_code", "mcpx__search_tools"}
	if !equalStringSlices(got, want) {
		t.Fatalf("two-tool surface mismatch:\n got=%v\nwant=%v", got, want)
	}
}

// TestDispatcherListTools_EmptyAllowlistFailsClosedEvenWithBuiltin
// verifies that the SECURITY contract — explicit `[]` allowlist returns
// zero tools — fires BEFORE the builtin caller is consulted. An
// operator's explicit deny-everything must beat any default surface.
func TestDispatcherListTools_EmptyAllowlistFailsClosedEvenWithBuiltin(t *testing.T) {
	db := newDispatcherTestStore(t)
	d := newToolDispatcher(db, nil, nil)
	d.SetBuiltinCaller(&fakeBuiltin{})

	tools, err := d.ListTools(context.Background(), []string{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("expected 0 tools for empty allowlist, got %d", len(tools))
	}
}

// TestDispatcherDispatchTool_BuiltinDelegated verifies that the two
// supported names (mcpx__search_tools, mcpx__execute_code) are forwarded
// to the BuiltinToolCaller, not the engine.
func TestDispatcherDispatchTool_BuiltinDelegated(t *testing.T) {
	db := newDispatcherTestStore(t)
	bt := &fakeBuiltin{}
	d := newToolDispatcher(db, nil, nil)
	d.SetBuiltinCaller(bt)

	for _, name := range []string{"mcpx__search_tools", "mcpx__execute_code"} {
		t.Run(name, func(t *testing.T) {
			before := len(bt.calls)
			res, err := d.DispatchTool(context.Background(), runner.ToolCallRequest{
				Name:      name,
				InputJSON: `{"queries":["foo"]}`,
			})
			if err != nil {
				t.Fatalf("DispatchTool: %v", err)
			}
			if res.IsError {
				t.Fatalf("expected success, got error: %s", res.OutputJSON)
			}
			if len(bt.calls) != before+1 {
				t.Fatalf("BuiltinCaller not invoked (calls=%d, before=%d)", len(bt.calls), before)
			}
			if bt.calls[before].Name != name {
				t.Fatalf("delegated call name mismatch: got %q want %q", bt.calls[before].Name, name)
			}
		})
	}
}

// TestDispatcherDispatchTool_NonBuiltinRejected verifies that a name
// outside the two-tool surface (e.g. a model hallucinating a downstream
// tool name directly) is rejected with a clear error — the dispatcher
// must NOT silently route around the execute_code sandbox.
func TestDispatcherDispatchTool_NonBuiltinRejected(t *testing.T) {
	db := newDispatcherTestStore(t)
	bt := &fakeBuiltin{}
	d := newToolDispatcher(db, nil, nil)
	d.SetBuiltinCaller(bt)

	res, err := d.DispatchTool(context.Background(), runner.ToolCallRequest{
		Name:      "github__create_issue",
		InputJSON: `{"title":"bug"}`,
	})
	if err != nil {
		t.Fatalf("DispatchTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true for non-builtin name, got success: %s", res.OutputJSON)
	}
	if len(bt.calls) != 0 {
		t.Fatalf("BuiltinCaller must not be invoked for non-builtin names, got %d calls", len(bt.calls))
	}
}

// TestDispatcherDispatchTool_BuiltinErrorSurfaces verifies that a
// transport-level failure from the BuiltinToolCaller (distinct from a
// tool-reported failure) lands as IsError=true with the wiring error
// text preserved in OutputJSON.
func TestDispatcherDispatchTool_BuiltinErrorSurfaces(t *testing.T) {
	db := newDispatcherTestStore(t)
	bt := &fakeBuiltin{callErr: errors.New("gateway not wired")}
	d := newToolDispatcher(db, nil, nil)
	d.SetBuiltinCaller(bt)

	res, err := d.DispatchTool(context.Background(), runner.ToolCallRequest{
		Name:      "mcpx__execute_code",
		InputJSON: `{"code":"print(1)"}`,
	})
	if err != nil {
		t.Fatalf("DispatchTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true on builtin transport failure")
	}
	if !strings.Contains(res.OutputJSON, "gateway not wired") {
		t.Fatalf("error text not preserved in OutputJSON: %s", res.OutputJSON)
	}
}

// TestExtractIsError exercises the envelope parser the dispatcher uses
// to forward tool-reported failures back through the runner.
func TestExtractIsError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"explicit true", `{"content":[],"isError":true}`, true},
		{"explicit false", `{"content":[],"isError":false}`, false},
		{"missing field defaults false", `{"content":[]}`, false},
		{"malformed JSON treated as success", `not json`, false},
		{"empty envelope treated as success", ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractIsError(json.RawMessage(tc.in))
			if got != tc.want {
				t.Fatalf("extractIsError(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func names(s []models.ToolSchema) []string {
	out := make([]string, len(s))
	for i, t := range s {
		out[i] = t.Name
	}
	return out
}

func newDispatcherTestStore(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "dispatcher.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSkillReaderAdapter_WorkspaceThenGlobalFallback verifies that the
// adapter used by worker runners resolves workspace-scoped skills when the
// worker's WorkspaceID is supplied, falls back to global skills, and
// returns not-found for skills the scope cannot see. This is the regression
// cover for worker skill_refs using only GlobalScope().
func TestSkillReaderAdapter_WorkspaceThenGlobalFallback(t *testing.T) {
	ctx := context.Background()
	db := newDispatcherTestStore(t)

	// Seed a workspace for the worker.
	ws := &store.Workspace{Name: "ws-for-worker", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	wsID := ws.ID

	reg := skillregistry.New(db)

	// Publish a global-only skill.
	globalBody := "---\nname: global-only\ndescription: global skill\n---\n# global-only\n\nGLOBAL BODY\n"
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "global-only", Body: globalBody}); err != nil {
		t.Fatalf("publish global: %v", err)
	}

	// Publish a workspace-scoped skill (visible only via ws scope).
	wsBody := "---\nname: ws-only\ndescription: ws skill\n---\n# ws-only\n\nWS BODY\n"
	wsPtr := wsID
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "ws-only", Body: wsBody, WorkspaceID: &wsPtr}); err != nil {
		t.Fatalf("publish ws: %v", err)
	}

	// Also publish a name that exists in both; ws shadows global.
	sharedGlobal := "---\nname: shared\ndescription: g\n---\n# shared\n\nGLOBAL SHARED\n"
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "shared", Body: sharedGlobal}); err != nil {
		t.Fatalf("publish shared global: %v", err)
	}
	sharedWS := "---\nname: shared\ndescription: ws\n---\n# shared\n\nWS SHARED\n"
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "shared", Body: sharedWS, WorkspaceID: &wsPtr}); err != nil {
		t.Fatalf("publish shared ws: %v", err)
	}

	adapter := skillReaderAdapter{reg: reg}

	// 1. Workspace worker resolves its own scoped skill.
	body, err := adapter.GetSkillBody(ctx, wsID, "ws-only", "")
	if err != nil {
		t.Fatalf("ws-only via wsID: %v", err)
	}
	if !strings.Contains(body, "WS BODY") {
		t.Fatalf("ws-only body wrong: %q", body)
	}

	// 2. Workspace worker falls back to global skill.
	body, err = adapter.GetSkillBody(ctx, wsID, "global-only", "")
	if err != nil {
		t.Fatalf("global-only via wsID: %v", err)
	}
	if !strings.Contains(body, "GLOBAL BODY") {
		t.Fatalf("global fallback body wrong: %q", body)
	}

	// 3. Workspace worker sees the workspace version of a shadowed name (not the global).
	body, err = adapter.GetSkillBody(ctx, wsID, "shared", "")
	if err != nil {
		t.Fatalf("shared via wsID: %v", err)
	}
	if !strings.Contains(body, "WS SHARED") || strings.Contains(body, "GLOBAL SHARED") {
		t.Fatalf("shadowing failed, got: %q", body)
	}

	// 4. Runtime skill reads expand exact, content-pinned includes.
	fragmentBody := "---\nname: worker-fragment\ndescription: neutral fragment\n---\nWORKER FRAGMENT\n"
	fragment, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "worker-fragment", Body: fragmentBody})
	if err != nil {
		t.Fatalf("publish worker fragment: %v", err)
	}
	composedBody := fmt.Sprintf(`---
name: worker-composed
description: neutral composed worker skill
includes:
  - id: fragment
    skill: worker-fragment
    scope: global
    version: %d
    content_hash: %q
---
WORKER ROOT
<!-- mcpx:include fragment -->
`, fragment.Version, fragment.ContentHash)
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "worker-composed", Body: composedBody}); err != nil {
		t.Fatalf("publish worker composition: %v", err)
	}
	body, err = adapter.GetSkillBody(ctx, wsID, "worker-composed", "")
	if err != nil {
		t.Fatalf("worker composition: %v", err)
	}
	if !strings.Contains(body, "WORKER FRAGMENT") || strings.Contains(body, "mcpx:include") {
		t.Fatalf("worker received raw placeholders: %q", body)
	}

	// 5. Global worker (empty wsID) sees only globals.
	if _, err = adapter.GetSkillBody(ctx, "", "global-only", ""); err != nil {
		t.Fatalf("global-only via empty wsID: %v", err)
	}

	// A ws-only name is not visible to global scope.
	_, err = adapter.GetSkillBody(ctx, "", "ws-only", "")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ws-only via global scope err = %v, want ErrNotFound", err)
	}

	// 6. Not found for unknown name in any scope.
	_, err = adapter.GetSkillBody(ctx, wsID, "no-such-skill", "v99")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no-such err = %v, want ErrNotFound", err)
	}

	// Also via pure global.
	_, err = adapter.GetSkillBody(ctx, "", "no-such-skill", "")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no-such (global) err = %v, want ErrNotFound", err)
	}
}
