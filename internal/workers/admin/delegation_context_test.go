package admin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type mockRecaller struct {
	hits    []store.MemoryHit
	err     error
	gotK    int
	gotQ    string
	gotFilt store.MemoryFilter
}

func (m *mockRecaller) Recall(_ context.Context, f store.MemoryFilter, query string, k int) ([]store.MemoryHit, error) {
	m.gotK, m.gotQ, m.gotFilt = k, query, f
	return m.hits, m.err
}

type mockMeshStore struct {
	store.MeshStore
	msgs    []store.MeshMessage
	err     error
	gotFilt store.MeshMessageFilter
}

func (m *mockMeshStore) QueryMeshMessages(_ context.Context, f store.MeshMessageFilter) ([]store.MeshMessage, error) {
	m.gotFilt = f
	return m.msgs, m.err
}

func TestBuildAutoContextRendersBothSections(t *testing.T) {
	rec := &mockRecaller{hits: []store.MemoryHit{
		{Entry: store.MemoryEntry{Name: "auth-decision", Content: "we use age for secrets at rest"}},
	}}
	msh := &mockMeshStore{msgs: []store.MeshMessage{
		{Kind: "finding", AgentName: "sibling", Content: "runner deps refactored"},
		{Kind: "finding", AgentName: "noise", Content: "tick", Tags: "delegation_progress,worker:x"},
	}}
	svc := &Service{memory: rec, meshStore: msh, clock: realClock{}}

	in := &DelegationInput{WorkspaceID: "ws1", Objective: "harden secret storage", TaskID: "t1"}
	packet, tokens := svc.buildAutoContext(context.Background(), in)

	if !strings.Contains(packet, "Prior knowledge") || !strings.Contains(packet, "auth-decision") {
		t.Fatalf("missing memory section:\n%s", packet)
	}
	if !strings.Contains(packet, "Recent mesh activity") || !strings.Contains(packet, "runner deps refactored") {
		t.Fatalf("missing mesh section:\n%s", packet)
	}
	if strings.Contains(packet, "delegation_progress") || strings.Contains(packet, "tick") {
		t.Fatalf("progress telemetry must be filtered from mesh section:\n%s", packet)
	}
	if tokens <= 0 {
		t.Fatalf("tokens = %d, want > 0", tokens)
	}
	if rec.gotK != autoContextRecallK || rec.gotQ != "harden secret storage" {
		t.Fatalf("recall called with k=%d q=%q", rec.gotK, rec.gotQ)
	}
	if len(rec.gotFilt.Scope.WorkspaceIDs) != 1 || rec.gotFilt.Scope.WorkspaceIDs[0] != "ws1" {
		t.Fatalf("recall scope = %+v", rec.gotFilt.Scope)
	}
	if len(rec.gotFilt.EntitiesAny) != 1 || rec.gotFilt.EntitiesAny[0].ID != "t1" {
		t.Fatalf("task entity filter not applied: %+v", rec.gotFilt.EntitiesAny)
	}
	if !msh.gotFilt.OrderRecent || !msh.gotFilt.StatusLive {
		t.Fatalf("mesh filter = %+v", msh.gotFilt)
	}
}

func TestBuildAutoContextOptOut(t *testing.T) {
	rec := &mockRecaller{hits: []store.MemoryHit{{Entry: store.MemoryEntry{Name: "x", Content: "y"}}}}
	svc := &Service{memory: rec, clock: realClock{}}
	if p, tok := svc.buildAutoContext(context.Background(), &DelegationInput{NoAutoContext: true, WorkspaceID: "ws"}); p != "" || tok != 0 {
		t.Fatalf("opt-out must inject nothing, got %q / %d", p, tok)
	}
}

func TestBuildAutoContextBestEffortOnError(t *testing.T) {
	rec := &mockRecaller{err: errors.New("embedder down")}
	msh := &mockMeshStore{err: errors.New("db down")}
	svc := &Service{memory: rec, meshStore: msh, clock: realClock{}}
	if p, tok := svc.buildAutoContext(context.Background(), &DelegationInput{WorkspaceID: "ws"}); p != "" || tok != 0 {
		t.Fatalf("errors must degrade to empty, got %q / %d", p, tok)
	}
}

func TestBuildAutoContextNoDepsNoop(t *testing.T) {
	svc := &Service{clock: realClock{}}
	if p, _ := svc.buildAutoContext(context.Background(), &DelegationInput{WorkspaceID: "ws"}); p != "" {
		t.Fatalf("no deps must inject nothing, got %q", p)
	}
}

func TestBuildAutoContextBudgetBounds(t *testing.T) {
	big := strings.Repeat("x", 10000)
	rec := &mockRecaller{hits: []store.MemoryHit{
		{Entry: store.MemoryEntry{Name: "huge", Content: big}},
	}}
	svc := &Service{memory: rec, clock: realClock{}}
	packet, _ := svc.buildAutoContext(context.Background(), &DelegationInput{WorkspaceID: "ws", Objective: "q"})
	if len(packet) > autoContextBudgetChars+200 {
		t.Fatalf("packet %d chars exceeds budget %d", len(packet), autoContextBudgetChars)
	}
	// Each memory body is individually capped.
	if strings.Contains(packet, strings.Repeat("x", autoContextMemoryCharCap+1)) {
		t.Fatal("memory body exceeded per-entry cap")
	}
}

func TestTruncateForContext(t *testing.T) {
	if got := truncateForContext("  a\nb  ", 10); got != "a b" {
		t.Fatalf("truncateForContext newline/trim = %q", got)
	}
	if got := truncateForContext("abcdef", 3); got != "abc…" {
		t.Fatalf("truncateForContext cap = %q", got)
	}
	_ = time.Second
}
