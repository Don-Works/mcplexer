package runner

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// progressMesh is a minimal in-package MeshSender fake (the runner_test
// package's fakeMesh is not visible to internal tests).
type progressMesh struct {
	mu   sync.Mutex
	sent []MeshOutbound
}

func (m *progressMesh) Send(_ context.Context, msg MeshOutbound) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return "mid-1", nil
}

func TestReadProgressInterval(t *testing.T) {
	cases := []struct {
		name   string
		params string
		want   int
	}{
		{"empty", "", 0},
		{"invalid json", "{not json", 0},
		{"top-level int", `{"progress_interval": 7}`, 7},
		{"nested delegation key", `{"_mcplexer_delegation":{"progress_interval":3}}`, 3},
		{"nested wins over absent top-level", `{"_mcplexer_delegation":{"progress_interval":5},"other":1}`, 5},
		{"nested zero falls through to top-level", `{"_mcplexer_delegation":{"progress_interval":0},"progress_interval":9}`, 9},
		{"absent", `{"foo":"bar"}`, 0},
		{"string value ignored", `{"progress_interval":"12"}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := readProgressInterval(tc.params); got != tc.want {
				t.Fatalf("readProgressInterval(%s) = %d, want %d", tc.params, got, tc.want)
			}
		})
	}
}

func TestApplyDefaultsLeavesProgressIntervalOptIn(t *testing.T) {
	// No default: a worker that didn't ask for progress emits none.
	if got := (Caps{}).applyDefaults().ProgressInterval; got != 0 {
		t.Fatalf("ProgressInterval default = %d, want 0 (opt-in)", got)
	}
	if got := (Caps{ProgressInterval: 4}).applyDefaults().ProgressInterval; got != 4 {
		t.Fatalf("ProgressInterval = %d, want preserved 4", got)
	}
}

func newProgressLoopState(interval, toolCalls int) *loopState {
	s := newLoopState(&store.Worker{ID: "w1"}, "", "", nil, nil,
		Caps{ProgressInterval: interval}.applyDefaults(), time.Now())
	s.runID = "run1"
	s.toolCallCount = toolCalls
	return s
}

func TestMaybeEmitProgress(t *testing.T) {
	t.Run("emits at interval and records mesh id", func(t *testing.T) {
		mesh := &progressMesh{}
		r := &Runner{mesh: mesh, clock: RealClock{}}
		s := newProgressLoopState(10, 10)
		r.maybeEmitProgress(context.Background(), s)
		if len(mesh.sent) != 1 {
			t.Fatalf("sent = %d, want 1", len(mesh.sent))
		}
		if !strings.Contains(mesh.sent[0].Tags, "delegation_progress") ||
			!strings.Contains(mesh.sent[0].Tags, "run:run1") {
			t.Fatalf("tags = %q", mesh.sent[0].Tags)
		}
		if s.lastProgressEmit != 10 {
			t.Fatalf("lastProgressEmit = %d, want 10", s.lastProgressEmit)
		}
		if len(s.meshMsgIDs) != 1 {
			t.Fatalf("progress mesh id not recorded: %v", s.meshMsgIDs)
		}
	})

	t.Run("silent below interval", func(t *testing.T) {
		mesh := &progressMesh{}
		r := &Runner{mesh: mesh, clock: RealClock{}}
		s := newProgressLoopState(10, 9)
		r.maybeEmitProgress(context.Background(), s)
		if len(mesh.sent) != 0 {
			t.Fatalf("sent = %d, want 0 below interval", len(mesh.sent))
		}
	})

	t.Run("disabled interval never emits", func(t *testing.T) {
		mesh := &progressMesh{}
		r := &Runner{mesh: mesh, clock: RealClock{}}
		s := newProgressLoopState(0, 50)
		r.maybeEmitProgress(context.Background(), s)
		if len(mesh.sent) != 0 {
			t.Fatalf("sent = %d, want 0 when disabled", len(mesh.sent))
		}
	})

	t.Run("nil mesh is safe", func(t *testing.T) {
		r := &Runner{clock: RealClock{}}
		r.maybeEmitProgress(context.Background(), newProgressLoopState(10, 10))
	})
}

func TestDelegationProgressIsWorkerLifecycle(t *testing.T) {
	if !isWorkerLifecycle(store.MeshMessage{Tags: "delegation_progress,worker:w1"}) {
		t.Fatal("delegation_progress must be filtered from mesh history")
	}
}
