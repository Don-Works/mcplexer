// handler_monitoring_test.go — workspace-ownership coverage for the
// ID-addressed monitoring.* tools: monitoring__search/source_id,
// monitoring__raw/template_id, monitoring__ack/template_id, and
// monitoring__notify/remote_host_id must reject objects outside the
// caller/resolved workspace. Same-workspace behaviour must continue
// to work.
package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// captureNotifier records every Notification handed to it so the
// monitoring__notify tests can assert that cross-workspace calls were
// dropped at the ownership gate rather than reaching the dispatcher.
type captureNotifier struct {
	notes []distill.Notification
}

func (c *captureNotifier) Notify(_ context.Context, n distill.Notification) error {
	c.notes = append(c.notes, n)
	return nil
}

// newMonitoringOwnershipHandler wires a real SQLite store with two
// workspaces and the matching monitoring services onto a fresh
// gateway handler. The session's workspace chain intentionally spans
// BOTH workspaces so the requireWorkspaceRead/Write gate at the
// workspace-resolution layer doesn't reject either — this isolates
// the new per-object ownership check we're verifying here.
func newMonitoringOwnershipHandler(t *testing.T) (*handler, *sqlite.DB, *captureNotifier) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "monitoring.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	wsA := &store.Workspace{ID: "ws-A", Name: "Workspace A", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, wsA); err != nil {
		t.Fatalf("create ws-A: %v", err)
	}
	wsB := &store.Workspace{ID: "ws-B", Name: "Workspace B", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, wsB); err != nil {
		t.Fatalf("create ws-B: %v", err)
	}
	scopeA := &store.AuthScope{Name: "scope-A", Type: "env"}
	if err := db.CreateAuthScope(ctx, scopeA); err != nil {
		t.Fatalf("create scope-A: %v", err)
	}
	scopeB := &store.AuthScope{Name: "scope-B", Type: "env"}
	if err := db.CreateAuthScope(ctx, scopeB); err != nil {
		t.Fatalf("create scope-B: %v", err)
	}

	hostA := &store.RemoteHost{
		WorkspaceID: "ws-A", Name: "ip-prod-A", SSHUser: "logwatch",
		SSHHost: "100.64.0.3", AuthScopeID: scopeA.ID, Enabled: true,
	}
	if err := db.CreateRemoteHost(ctx, hostA); err != nil {
		t.Fatalf("create host-A: %v", err)
	}
	hostB := &store.RemoteHost{
		WorkspaceID: "ws-B", Name: "ip-prod-B", SSHUser: "logwatch",
		SSHHost: "100.64.0.4", AuthScopeID: scopeB.ID, Enabled: true,
	}
	if err := db.CreateRemoteHost(ctx, hostB); err != nil {
		t.Fatalf("create host-B: %v", err)
	}

	srcA := &store.LogSource{
		WorkspaceID: "ws-A", RemoteHostID: hostA.ID, Name: "api-A",
		Kind: store.LogSourceKindDocker, Selector: "api-a", Enabled: true,
		RetentionDays: 7, RetentionMB: 50,
	}
	if err := db.CreateLogSource(ctx, srcA); err != nil {
		t.Fatalf("create source-A: %v", err)
	}
	srcB := &store.LogSource{
		WorkspaceID: "ws-B", RemoteHostID: hostB.ID, Name: "api-B",
		Kind: store.LogSourceKindDocker, Selector: "api-b", Enabled: true,
		RetentionDays: 7, RetentionMB: 50,
	}
	if err := db.CreateLogSource(ctx, srcB); err != nil {
		t.Fatalf("create source-B: %v", err)
	}

	now := time.Now().UTC()
	tplA := &store.LogTemplate{
		ID: "tpl-A", SourceID: srcA.ID, Masked: "GET / <n>",
		Severity: store.SeverityInfo, FirstSeen: now, LastSeen: now,
	}
	if _, err := db.UpsertLogTemplate(ctx, tplA, 1); err != nil {
		t.Fatalf("upsert tpl-A: %v", err)
	}
	tplB := &store.LogTemplate{
		ID: "tpl-B", SourceID: srcB.ID, Masked: "GET / <n>",
		Severity: store.SeverityInfo, FirstSeen: now, LastSeen: now,
	}
	if _, err := db.UpsertLogTemplate(ctx, tplB, 1); err != nil {
		t.Fatalf("upsert tpl-B: %v", err)
	}

	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.store = db
	h.sessions.wsChain = []routing.WorkspaceAncestor{
		{ID: "ws-A", Name: "Workspace A"},
		{ID: "ws-B", Name: "Workspace B"},
	}
	notifier := &captureNotifier{}
	h.monitoringQry = distill.NewQuery(db)
	h.monitoringNtf = notifier
	return h, db, notifier
}

// monitoringToolText invokes one of the built-in monitoring tools via
// dispatchMonitoringTool and returns the text payload from the
// CallToolResult envelope. RPC errors fail the test — the ownership
// guard is intentionally implemented at the tool-result layer (so
// the LLM sees a typed error instead of an RPC fault).
func monitoringToolText(t *testing.T, h *handler, name, argsJSON string) (string, bool) {
	t.Helper()
	raw, rpcErr, handled := h.dispatchMonitoringTool(
		context.Background(), name, json.RawMessage(argsJSON),
	)
	if !handled {
		t.Fatalf("%s not handled", name)
	}
	if rpcErr != nil {
		t.Fatalf("%s rpcErr: %v", name, rpcErr)
	}
	var parsed CallToolResult
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("%s unmarshal: %v", name, err)
	}
	if len(parsed.Content) == 0 {
		t.Fatalf("%s empty content", name)
	}
	return parsed.Content[0].Text, parsed.IsError
}

func TestMonitoringWorkspaceOwnership(t *testing.T) {
	h, _, notifier := newMonitoringOwnershipHandler(t)

	// Pull the seeded IDs back so the table tests can address them.
	ctx := context.Background()
	srcA, err := h.store.ListLogSources(ctx, "ws-A")
	if err != nil || len(srcA) == 0 {
		t.Fatalf("ListLogSources ws-A: %v len=%d", err, len(srcA))
	}
	srcB, err := h.store.ListLogSources(ctx, "ws-B")
	if err != nil || len(srcB) == 0 {
		t.Fatalf("ListLogSources ws-B: %v len=%d", err, len(srcB))
	}
	hostA, err := h.store.ListRemoteHosts(ctx, "ws-A")
	if err != nil || len(hostA) == 0 {
		t.Fatalf("ListRemoteHosts ws-A: %v", err)
	}
	hostB, err := h.store.ListRemoteHosts(ctx, "ws-B")
	if err != nil || len(hostB) == 0 {
		t.Fatalf("ListRemoteHosts ws-B: %v", err)
	}
	const tplA, tplB = "tpl-A", "tpl-B"

	cases := []struct {
		name        string
		tool        string
		args        string
		wantIsError bool
		wantContain string // substring the response must contain when wantIsError
	}{
		{
			name: "search same-workspace succeeds",
			tool: "monitoring__search",
			args: `{"source_id":"` + srcA[0].ID + `","q":"GET","limit":10,"workspace_id":"ws-A"}`,
		},
		{
			name:        "search cross-workspace rejected",
			tool:        "monitoring__search",
			args:        `{"source_id":"` + srcB[0].ID + `","q":"GET","limit":10,"workspace_id":"ws-A"}`,
			wantIsError: true,
			wantContain: store.ErrLogSourceNotFound.Error(),
		},
		{
			name:        "search unknown id rejected",
			tool:        "monitoring__search",
			args:        `{"source_id":"does-not-exist","q":"GET","limit":10,"workspace_id":"ws-A"}`,
			wantIsError: true,
			wantContain: store.ErrLogSourceNotFound.Error(),
		},
		{
			name: "raw same-workspace succeeds",
			tool: "monitoring__raw",
			args: `{"template_id":"` + tplA + `","limit":10,"workspace_id":"ws-A"}`,
		},
		{
			name:        "raw cross-workspace rejected",
			tool:        "monitoring__raw",
			args:        `{"template_id":"` + tplB + `","limit":10,"workspace_id":"ws-A"}`,
			wantIsError: true,
			wantContain: store.ErrLogTemplateNotFound.Error(),
		},
		{
			name:        "raw unknown id rejected",
			tool:        "monitoring__raw",
			args:        `{"template_id":"missing","limit":10,"workspace_id":"ws-A"}`,
			wantIsError: true,
			wantContain: store.ErrLogTemplateNotFound.Error(),
		},
		{
			name: "ack same-workspace succeeds",
			tool: "monitoring__ack",
			args: `{"template_id":"` + tplA + `","note":"ack by ws-A","workspace_id":"ws-A"}`,
		},
		{
			name:        "ack cross-workspace rejected",
			tool:        "monitoring__ack",
			args:        `{"template_id":"` + tplB + `","note":"sneak","workspace_id":"ws-A"}`,
			wantIsError: true,
			wantContain: store.ErrLogTemplateNotFound.Error(),
		},
		{
			name:        "ack unknown id rejected",
			tool:        "monitoring__ack",
			args:        `{"template_id":"missing","note":"","workspace_id":"ws-A"}`,
			wantIsError: true,
			wantContain: store.ErrLogTemplateNotFound.Error(),
		},
		{
			name: "notify same-workspace with host-A succeeds",
			tool: "monitoring__notify",
			args: `{"severity":"info","title":"ws-A ping","body":"","remote_host_id":"` + hostA[0].ID + `","workspace_id":"ws-A"}`,
		},
		{
			name:        "notify cross-workspace with host-B rejected",
			tool:        "monitoring__notify",
			args:        `{"severity":"info","title":"sneak","body":"","remote_host_id":"` + hostB[0].ID + `","workspace_id":"ws-A"}`,
			wantIsError: true,
			wantContain: store.ErrRemoteHostNotFound.Error(),
		},
		{
			name:        "notify unknown remote_host_id rejected",
			tool:        "monitoring__notify",
			args:        `{"severity":"info","title":"sneak","body":"","remote_host_id":"missing","workspace_id":"ws-A"}`,
			wantIsError: true,
			wantContain: store.ErrRemoteHostNotFound.Error(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(notifier.notes)
			text, isErr := monitoringToolText(t, h, tc.tool, tc.args)
			if isErr != tc.wantIsError {
				t.Fatalf("isErr=%v want=%v body=%s", isErr, tc.wantIsError, text)
			}
			if tc.wantContain != "" && !strings.Contains(text, tc.wantContain) {
				t.Fatalf("response missing %q: %s", tc.wantContain, text)
			}
			// Cross-workspace + unknown rejection paths must NOT reach
			// the underlying store action — for monitoring__ack the
			// template in ws-B must remain un-acked, and for
			// monitoring__notify the dispatcher must not have been
			// called.
			if tc.tool == "monitoring__ack" && tc.wantIsError {
				tplB, err := h.store.GetLogTemplate(ctx, tplB)
				if err != nil {
					t.Fatalf("re-fetch tpl-B: %v", err)
				}
				if tplB.Acked {
					t.Fatalf("cross-workspace ack leaked into ws-B template: %+v", tplB)
				}
			}
			if tc.tool == "monitoring__notify" && tc.wantIsError {
				if len(notifier.notes) != before {
					t.Fatalf("cross-workspace notify reached the dispatcher: %+v",
						notifier.notes[before:])
				}
			}
		})
	}
}
