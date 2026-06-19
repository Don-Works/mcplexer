// events_test.go — table-driven coverage of the Phase-2 task_event
// emitter. Mocks the mesh.Manager.Send entrypoint via a recording
// fakeSender so we can assert the full SendRequest shape without
// spinning a sqlite mesh manager.
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
)

// fakeSender records every Send call so tests can inspect the
// MeshSender interface contract. Satisfies tasks.MeshSender.
type fakeSender struct {
	calls []sentCall
}

type sentCall struct {
	meta mesh.SessionMeta
	req  mesh.SendRequest
}

func (f *fakeSender) Send(_ context.Context, meta mesh.SessionMeta, req mesh.SendRequest) (*store.MeshMessage, error) {
	f.calls = append(f.calls, sentCall{meta: meta, req: req})
	// Return a synthetic MeshMessage so the contract matches the real
	// signature; callers ignore it but a nil return would mask a bug
	// where the emitter dereferences it.
	return &store.MeshMessage{
		ID:          "test-msg-id",
		Kind:        req.Kind,
		Content:     req.Content,
		Tags:        req.Tags,
		WorkspaceID: firstWorkspace(meta.WorkspaceIDs),
		ActorKind:   req.ActorKind,
	}, nil
}

func firstWorkspace(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

// taskFixture builds a minimal Task row suitable for emit tests.
func taskFixture(t *testing.T, opts ...func(*store.Task)) *store.Task {
	t.Helper()
	tt := &store.Task{
		ID:          "task-abc",
		WorkspaceID: "ws-1",
		Title:       "Wire phase-2 events",
		Status:      "open",
		Priority:    "normal",
		TagsJSON:    []byte(`["security","review"]`),
	}
	for _, o := range opts {
		o(tt)
	}
	return tt
}

// TestEmitter_StatusChangedTags asserts the status_changed emission
// carries the structured status_from:/status_to: tags a WorkerMeshTrigger
// AND's on. Without these the transition lives only in Content and can't
// be matched precisely.
func TestEmitter_StatusChangedTags(t *testing.T) {
	fs := &fakeSender{}
	e := NewEmitter(fs)
	tt := taskFixture(t)
	e.EmitStatusChanged(context.Background(), tt, "doing", "review", EmitContext{})
	if len(fs.calls) != 1 {
		t.Fatalf("expected 1 send, got %d", len(fs.calls))
	}
	tags := fs.calls[0].req.Tags
	for _, want := range []string{"task_event:status_changed", "status_from:doing", "status_to:review"} {
		if !strings.Contains(tags, want) {
			t.Fatalf("tags %q missing %q", tags, want)
		}
	}
}

func TestEmitter_NilSafe(t *testing.T) {
	// All Emit methods must tolerate (a) nil emitter, (b) nil sender,
	// (c) nil task. The Service nil-checks the emitter once at the
	// call-site but the methods themselves defend belt-and-braces.
	var e *Emitter
	ctx := context.Background()
	tt := taskFixture(t)
	ec := EmitContext{}
	e.EmitCreated(ctx, tt, ec)
	e.EmitUpdated(ctx, tt, ec)
	e.EmitAssigned(ctx, tt, "", ec)
	e.EmitClaimed(ctx, tt, "", ec)
	e.EmitClosed(ctx, tt, "", ec)
	e.EmitDeleted(ctx, tt, ec)
	e.EmitStatusChanged(ctx, tt, "open", "doing", ec)
	e.EmitNote(ctx, tt, &store.TaskNote{}, ec)

	e2 := NewEmitter(nil)
	e2.EmitCreated(ctx, tt, ec)
	e2.EmitAssigned(ctx, nil, "", ec) // nil task
}

// TestEmitter_EventShape asserts every emit method produces a
// kind=task_event mesh message with the correct task_event:<evt>,
// task_id:, workspace: tags and the right priority. Per PLAN.md
// "Mesh event shape".
func TestEmitter_EventShape(t *testing.T) {
	cases := []struct {
		name       string
		invoke     func(*Emitter, context.Context, *store.Task)
		wantEvt    string
		wantPrio   string
		wantNotify bool
	}{
		{
			name:     "created",
			invoke:   func(e *Emitter, ctx context.Context, tt *store.Task) { e.EmitCreated(ctx, tt, EmitContext{}) },
			wantEvt:  "created",
			wantPrio: "low",
		},
		{
			name:     "updated",
			invoke:   func(e *Emitter, ctx context.Context, tt *store.Task) { e.EmitUpdated(ctx, tt, EmitContext{}) },
			wantEvt:  "updated",
			wantPrio: "low",
		},
		{
			name: "assigned-different-session-notify",
			invoke: func(e *Emitter, ctx context.Context, tt *store.Task) {
				tt.AssigneeSessionID = "session-other"
				e.EmitAssigned(ctx, tt, "session-self", EmitContext{})
			},
			wantEvt:    "assigned",
			wantPrio:   "normal",
			wantNotify: true,
		},
		{
			name: "assigned-self-quiet",
			invoke: func(e *Emitter, ctx context.Context, tt *store.Task) {
				tt.AssigneeSessionID = "session-self"
				e.EmitAssigned(ctx, tt, "session-self", EmitContext{})
			},
			wantEvt:  "assigned",
			wantPrio: "normal",
		},
		{
			name: "assigned-worker-suppressed",
			invoke: func(e *Emitter, ctx context.Context, tt *store.Task) {
				tt.AssigneeSessionID = "session-other"
				e.EmitAssigned(ctx, tt, "session-self", EmitContext{ActorKind: "worker"})
			},
			wantEvt:    "assigned",
			wantPrio:   "normal",
			wantNotify: false, // worker actor suppresses notify
		},
		{
			name: "claimed-from-prior",
			invoke: func(e *Emitter, ctx context.Context, tt *store.Task) {
				tt.AssigneeSessionID = "session-new"
				e.EmitClaimed(ctx, tt, "session-prior", EmitContext{})
			},
			wantEvt:    "claimed",
			wantPrio:   "normal",
			wantNotify: true,
		},
		{
			name: "claimed-same-session-quiet",
			invoke: func(e *Emitter, ctx context.Context, tt *store.Task) {
				tt.AssigneeSessionID = "session-x"
				e.EmitClaimed(ctx, tt, "session-x", EmitContext{})
			},
			wantEvt:    "claimed",
			wantPrio:   "normal",
			wantNotify: false,
		},
		{
			name: "closed-by-other",
			invoke: func(e *Emitter, ctx context.Context, tt *store.Task) {
				tt.AssigneeSessionID = "owner"
				e.EmitClosed(ctx, tt, "intruder", EmitContext{})
			},
			wantEvt:    "closed",
			wantPrio:   "normal",
			wantNotify: true,
		},
		{
			name: "closed-self",
			invoke: func(e *Emitter, ctx context.Context, tt *store.Task) {
				tt.AssigneeSessionID = "owner"
				e.EmitClosed(ctx, tt, "owner", EmitContext{})
			},
			wantEvt:    "closed",
			wantPrio:   "normal",
			wantNotify: false,
		},
		{
			name: "status_changed-never-notify",
			invoke: func(e *Emitter, ctx context.Context, tt *store.Task) {
				e.EmitStatusChanged(ctx, tt, "open", "doing", EmitContext{})
			},
			wantEvt:    "status_changed",
			wantPrio:   "low",
			wantNotify: false, // locked decision #5
		},
		{
			name:     "deleted",
			invoke:   func(e *Emitter, ctx context.Context, tt *store.Task) { e.EmitDeleted(ctx, tt, EmitContext{}) },
			wantEvt:  "deleted",
			wantPrio: "low",
		},
		{
			name: "note_appended",
			invoke: func(e *Emitter, ctx context.Context, tt *store.Task) {
				e.EmitNote(ctx, tt, &store.TaskNote{ID: "note-1", Body: "hi"}, EmitContext{})
			},
			wantEvt:  "note_appended",
			wantPrio: "low",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := &fakeSender{}
			e := NewEmitter(fs)
			tt := taskFixture(t)
			c.invoke(e, context.Background(), tt)
			if len(fs.calls) != 1 {
				t.Fatalf("expected 1 send, got %d", len(fs.calls))
			}
			req := fs.calls[0].req
			if req.Kind != "task_event" {
				t.Errorf("Kind = %q, want task_event", req.Kind)
			}
			if req.Priority != c.wantPrio {
				t.Errorf("Priority = %q, want %q", req.Priority, c.wantPrio)
			}
			if req.NotifyUser != c.wantNotify {
				t.Errorf("NotifyUser = %v, want %v", req.NotifyUser, c.wantNotify)
			}
			if req.Audience != "*" {
				t.Errorf("Audience = %q, want *", req.Audience)
			}
			if !hasTag(req.Tags, "task_event:"+c.wantEvt) {
				t.Errorf("Tags missing task_event:%s — got %q", c.wantEvt, req.Tags)
			}
			if !hasTag(req.Tags, "task_id:"+tt.ID) {
				t.Errorf("Tags missing task_id:%s — got %q", tt.ID, req.Tags)
			}
			if !hasTag(req.Tags, "workspace:"+tt.WorkspaceID) {
				t.Errorf("Tags missing workspace:%s — got %q", tt.WorkspaceID, req.Tags)
			}
		})
	}
}

// TestEmitter_TaskTagsPropagated confirms the task's own tags ride
// along on the mesh emission so workers can match on review/security/...
func TestEmitter_TaskTagsPropagated(t *testing.T) {
	fs := &fakeSender{}
	e := NewEmitter(fs)
	tt := taskFixture(t) // tags = ["security","review"]
	e.EmitCreated(context.Background(), tt, EmitContext{})
	if len(fs.calls) != 1 {
		t.Fatalf("want 1 send, got %d", len(fs.calls))
	}
	tags := fs.calls[0].req.Tags
	for _, want := range []string{"security", "review"} {
		if !hasTag(tags, want) {
			t.Errorf("task tag %q missing from emission %q", want, tags)
		}
	}
}

// TestEmitter_ChainDepthPropagation drives the loop-guard contract.
// Without a triggering message, the emission stamps chain-depth:1
// (fresh chain). With a triggering message at depth N, the emission
// stamps depth N+1.
func TestEmitter_ChainDepthPropagation(t *testing.T) {
	cases := []struct {
		name        string
		triggering  *store.MeshMessage
		wantDepth   string // exact tag fragment expected
		wantPresent bool
	}{
		{
			name:        "no-triggering",
			triggering:  nil,
			wantDepth:   "chain-depth:1",
			wantPresent: true,
		},
		{
			name:        "depth-1-source-bumps-to-2",
			triggering:  &store.MeshMessage{Tags: "alpha,chain-depth:1,beta"},
			wantDepth:   "chain-depth:2",
			wantPresent: true,
		},
		{
			name:        "depth-5-source-bumps-to-6",
			triggering:  &store.MeshMessage{Tags: "chain-depth:5"},
			wantDepth:   "chain-depth:6",
			wantPresent: true,
		},
		{
			name:        "malformed-source-falls-to-1",
			triggering:  &store.MeshMessage{Tags: "chain-depth:nope"},
			wantDepth:   "chain-depth:1",
			wantPresent: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := &fakeSender{}
			e := NewEmitter(fs)
			e.EmitCreated(context.Background(), taskFixture(t), EmitContext{Triggering: c.triggering})
			if len(fs.calls) != 1 {
				t.Fatalf("want 1 send, got %d", len(fs.calls))
			}
			tags := fs.calls[0].req.Tags
			if got := hasTag(tags, c.wantDepth); got != c.wantPresent {
				t.Errorf("hasTag(%q, %q) = %v, want %v", tags, c.wantDepth, got, c.wantPresent)
			}
		})
	}
}

// TestEmitter_ActorKindPlumbing confirms ec.ActorKind lands on the
// SendRequest unchanged so the receiving mesh.Manager can stamp it
// onto store.MeshMessage.actor_kind.
func TestEmitter_ActorKindPlumbing(t *testing.T) {
	for _, actor := range []string{"agent", "worker", "user", "peer-import", "system", ""} {
		t.Run(actor, func(t *testing.T) {
			fs := &fakeSender{}
			e := NewEmitter(fs)
			e.EmitCreated(context.Background(), taskFixture(t), EmitContext{ActorKind: actor})
			if got := fs.calls[0].req.ActorKind; got != actor {
				t.Errorf("ActorKind = %q, want %q", got, actor)
			}
		})
	}
}

// TestEmitter_SessionAndWorkspacePath confirms the emit context flows
// onto the SessionMeta + SendRequest so mesh.Manager fills repo/branch
// from the right cwd.
func TestEmitter_SessionAndWorkspacePath(t *testing.T) {
	fs := &fakeSender{}
	e := NewEmitter(fs)
	e.EmitCreated(context.Background(), taskFixture(t), EmitContext{
		SessionID:     "session-42",
		WorkspacePath: "/repos/mcplexer",
	})
	c := fs.calls[0]
	if c.meta.SessionID != "session-42" {
		t.Errorf("SessionID = %q, want session-42", c.meta.SessionID)
	}
	if c.meta.WorkspacePath != "/repos/mcplexer" {
		t.Errorf("meta.WorkspacePath = %q", c.meta.WorkspacePath)
	}
	if c.req.WorkspacePath != "/repos/mcplexer" {
		t.Errorf("req.WorkspacePath = %q", c.req.WorkspacePath)
	}
	// WorkspaceIDs slice should carry the task's workspace.
	if len(c.meta.WorkspaceIDs) != 1 || c.meta.WorkspaceIDs[0] != "ws-1" {
		t.Errorf("WorkspaceIDs = %v, want [ws-1]", c.meta.WorkspaceIDs)
	}
}

// TestEmitter_ContentString confirms the human-readable content
// includes the event + task title so the dashboard's mesh page renders
// without consulting the structured payload.
func TestEmitter_ContentString(t *testing.T) {
	fs := &fakeSender{}
	e := NewEmitter(fs)
	tt := taskFixture(t)
	e.EmitClosed(context.Background(), tt, "session-self", EmitContext{})
	if !strings.Contains(fs.calls[0].req.Content, "closed") {
		t.Errorf("Content missing 'closed': %q", fs.calls[0].req.Content)
	}
	if !strings.Contains(fs.calls[0].req.Content, tt.Title) {
		t.Errorf("Content missing title: %q", fs.calls[0].req.Content)
	}
}

// TestEmitter_StatusChangedRendersTransition asserts the from→to form
// is in the content so a peer reading the mesh log can reconstruct
// the transition without unpacking the payload.
func TestEmitter_StatusChangedRendersTransition(t *testing.T) {
	fs := &fakeSender{}
	e := NewEmitter(fs)
	e.EmitStatusChanged(context.Background(), taskFixture(t), "open", "doing", EmitContext{})
	body := fs.calls[0].req.Content
	if !strings.Contains(body, "open") || !strings.Contains(body, "doing") {
		t.Errorf("expected open + doing in content, got %q", body)
	}
}

// TestEmitter_MalformedTagsJSON ensures the lightweight tag parser
// doesn't panic on garbage TagsJSON — important because the column is
// stored verbatim from caller input and we don't want a corrupt row to
// poison the event stream.
func TestEmitter_MalformedTagsJSON(t *testing.T) {
	tt := &store.Task{ID: "x", WorkspaceID: "w", Status: "open"}
	for _, bad := range []string{"", "null", "{", "[", "]", `{"not":"a list"}`} {
		tt.TagsJSON = json.RawMessage(bad)
		// Should not panic.
		tags := taskTagList(tt)
		if tags != nil && containsEmpty(tags) {
			t.Errorf("malformed TagsJSON %q yielded empty tag in %v", bad, tags)
		}
	}
}

// TestNextChainDepth_TableDriven exercises the unexported helper —
// since the loop guard hinges on this and a regression here is
// invisible until tested.
func TestNextChainDepth_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		msg  *store.MeshMessage
		want int
	}{
		{"nil-message", nil, 1},
		{"empty-tags", &store.MeshMessage{Tags: ""}, 1},
		{"no-chain-depth-tag", &store.MeshMessage{Tags: "alpha,beta"}, 1},
		{"depth-3", &store.MeshMessage{Tags: "alpha,chain-depth:3,beta"}, 4},
		{"depth-0-treated-fresh", &store.MeshMessage{Tags: "chain-depth:0"}, 1},
		{"depth-negative-fresh", &store.MeshMessage{Tags: "chain-depth:-7"}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nextChainDepth(c.msg); got != c.want {
				t.Errorf("nextChainDepth(%+v) = %d, want %d", c.msg, got, c.want)
			}
		})
	}
}

// TestEmitter_RealMeshManagerInterface confirms the production
// mesh.Manager satisfies the MeshSender interface — guards against a
// future signature drift where the daemon wiring silently breaks.
func TestEmitter_RealMeshManagerInterface(t *testing.T) {
	var _ MeshSender = (*mesh.Manager)(nil)
	// Sanity: an Emitter wrapping the real manager is constructable.
	_ = NewEmitter(&mesh.Manager{})
}

// hasTag returns true when tags (comma-separated) contains exactly
// the wanted tag (whole-token match). Avoids substring false-positives
// where "chain-depth:1" could match "chain-depth:10".
func hasTag(tags, wanted string) bool {
	for _, t := range strings.Split(tags, ",") {
		if strings.TrimSpace(t) == wanted {
			return true
		}
	}
	return false
}

func containsEmpty(s []string) bool {
	for _, v := range s {
		if v == "" {
			return true
		}
	}
	return false
}

// Sanity-check that the package's exported errors are stable so the
// MCP handler's friendly-error mapping doesn't regress.
func TestErrTaskAlreadyClaimed_IsExported(t *testing.T) {
	if ErrTaskAlreadyClaimed == nil {
		t.Fatal("ErrTaskAlreadyClaimed must remain exported + non-nil")
	}
	if !errors.Is(ErrTaskAlreadyClaimed, ErrTaskAlreadyClaimed) {
		t.Fatal("errors.Is identity should hold")
	}
}
