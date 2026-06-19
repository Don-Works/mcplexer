package runner_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// countingSecrets wraps the secret-read path with a call counter so a
// test can assert resolveAPIKey never touches secrets for the gated CLI
// providers (claude_cli / opencode_cli / grok_cli / mimo_cli). value is returned for every
// Get when err is nil.
type countingSecrets struct {
	mu    sync.Mutex
	calls int
	value []byte
	err   error
}

func (c *countingSecrets) Get(_ context.Context, _, _ string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return c.value, nil
}

func (c *countingSecrets) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// TestDispatchToolCalls_MultiCallTurnRespectsToolCallCap is the
// regression test for the unbounded-side-effects bypass: a single model
// turn that returns more ToolCalls than MaxToolCalls must NOT dispatch
// all of them. The inner dispatch loop has to re-check the cap before
// each call, short-circuiting to cap_exceeded once the limit is reached.
//
// Before the fix, checkCaps only fired at the top of runLoop (once per
// turn), so N tool calls in one turn dispatched N times regardless of
// MaxToolCalls. The existing per-worker cap test emits one call per turn
// and never exercises this path.
func TestDispatchToolCalls_MultiCallTurnRespectsToolCallCap(t *testing.T) {
	tests := []struct {
		name           string
		maxToolCalls   int
		callsInTurn    int
		wantDispatched int
	}{
		{name: "cap 2, turn of 5", maxToolCalls: 2, callsInTurn: 5, wantDispatched: 2},
		{name: "cap 1, turn of 4", maxToolCalls: 1, callsInTurn: 4, wantDispatched: 1},
		{name: "cap 3, turn of 3 (exactly at cap)", maxToolCalls: 3, callsInTurn: 3, wantDispatched: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newTestStore(t)
			wsID, scopeID := setupFKs(t, db)
			w := sampleWorker(wsID, scopeID)
			w.MaxToolCalls = tt.maxToolCalls
			createWorker(t, db, w)

			calls := make([]models.ToolCall, tt.callsInTurn)
			for i := range calls {
				calls[i] = models.ToolCall{ID: "t", Name: "write_tool"}
			}
			adapter := &fakeAdapter{responses: []models.SendResponse{
				{ToolCalls: calls, StopReason: models.StopToolUse},
				{Text: "never reached", StopReason: models.StopEndTurn},
			}}
			disp := &fakeDispatcher{}
			r := makeRunner(t, db, adapter, disp, &fakeMesh{})

			runID, err := r.Run(context.Background(), w.ID)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			run, _ := db.GetWorkerRun(context.Background(), runID)
			if run.Status != runner.StatusCapExceeded {
				t.Fatalf("status = %q, want cap_exceeded", run.Status)
			}
			if !strings.Contains(run.Error, "tool calls") {
				t.Fatalf("error = %q, want mention of tool calls", run.Error)
			}
			if len(disp.dispatched) != tt.wantDispatched {
				t.Fatalf("dispatched %d tool calls, want %d (cap must bound a single multi-call turn)",
					len(disp.dispatched), tt.wantDispatched)
			}
		})
	}
}

// TestResolveAPIKey covers the gated-CLI-provider short-circuit and the
// missing-SecretReader error path. The CLI providers (claude_cli /
// opencode_cli / grok_cli / mimo_cli) MUST return "" without ever touching secrets even when a
// SecretScopeID is set, while a normal provider with a scope but no
// SecretReader must fail loudly.
func TestResolveAPIKey(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		secretScopeID  string
		secretsNil     bool
		secretValue    []byte
		secretErr      error
		wantKey        string
		wantErr        bool
		wantSecretGets int
	}{
		{
			name:           "claude_cli with scope short-circuits, never reads secrets",
			provider:       "claude_cli",
			secretScopeID:  "scope-123",
			secretValue:    []byte("should-not-be-read"),
			wantKey:        "",
			wantSecretGets: 0,
		},
		{
			name:           "opencode_cli with scope short-circuits, never reads secrets",
			provider:       "opencode_cli",
			secretScopeID:  "scope-456",
			secretValue:    []byte("should-not-be-read"),
			wantKey:        "",
			wantSecretGets: 0,
		},
		{
			name:           "grok_cli with scope short-circuits, never reads secrets",
			provider:       "grok_cli",
			secretScopeID:  "scope-grok",
			secretValue:    []byte("should-not-be-read"),
			wantKey:        "",
			wantSecretGets: 0,
		},
		{
			name:           "mimo_cli with scope short-circuits, never reads secrets",
			provider:       "mimo_cli",
			secretScopeID:  "scope-mimo",
			secretValue:    []byte("should-not-be-read"),
			wantKey:        "",
			wantSecretGets: 0,
		},
		{
			name:          "normal provider with scope but nil SecretReader errors",
			provider:      models.ProviderAnthropic,
			secretScopeID: "scope-789",
			secretsNil:    true,
			wantErr:       true,
		},
		{
			name:           "normal provider reads api_key value",
			provider:       models.ProviderAnthropic,
			secretScopeID:  "scope-abc",
			secretValue:    []byte("real-key"),
			wantKey:        "real-key",
			wantSecretGets: 1,
		},
		{
			name:           "empty scope short-circuits regardless of provider",
			provider:       models.ProviderAnthropic,
			secretScopeID:  "",
			secretValue:    []byte("unused"),
			wantKey:        "",
			wantSecretGets: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := &countingSecrets{value: tt.secretValue, err: tt.secretErr}
			deps := runner.Deps{
				Store:      newTestStore(t),
				Dispatcher: &fakeDispatcher{},
				Mesh:       &fakeMesh{},
				Adapter: func(_ models.Config) (models.ModelAdapter, error) {
					return &fakeAdapter{}, nil
				},
			}
			if !tt.secretsNil {
				deps.Secrets = cs
			}
			r := runner.New(deps)

			w := &store.Worker{
				ModelProvider: tt.provider,
				SecretScopeID: tt.secretScopeID,
			}
			key, err := r.ResolveAPIKeyForTest(context.Background(), w)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got key=%q nil err", key)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if key != tt.wantKey {
				t.Fatalf("key = %q, want %q", key, tt.wantKey)
			}
			if got := cs.callCount(); got != tt.wantSecretGets {
				t.Fatalf("secrets.Get called %d times, want %d", got, tt.wantSecretGets)
			}
		})
	}
}

// TestMergeWorkerCaps unit-tests the per-field inherit semantics and the
// MaxOutputTokens->lifetimeOutputCap mapping in isolation. A future
// refactor that starts lowering the per-turn ceiling (c.MaxOutputTokens)
// from a worker's MaxOutputTokens would slip past the e2e test (which
// only asserts cap_exceeded fires) but is caught here.
func TestMergeWorkerCaps(t *testing.T) {
	base := runner.Caps{
		MaxIterations:   12,
		MaxToolCalls:    50,
		MaxWallClock:    300 * time.Second,
		MaxOutputTokens: 4096,
		MaxInputTokens:  runner.DefaultMaxInputTokens,
	}

	tests := []struct {
		name             string
		worker           store.Worker
		wantCaps         runner.Caps
		wantLifetimeXOut int
	}{
		{
			name:             "all zero worker fields inherit base, no lifetime cap",
			worker:           store.Worker{},
			wantCaps:         base,
			wantLifetimeXOut: 0,
		},
		{
			name:   "MaxOutputTokens sets lifetime cap but leaves per-turn ceiling untouched",
			worker: store.Worker{MaxOutputTokens: 64},
			wantCaps: runner.Caps{
				MaxIterations:   12,
				MaxToolCalls:    50,
				MaxWallClock:    300 * time.Second,
				MaxOutputTokens: 4096, // unchanged — NOT lowered to 64
				MaxInputTokens:  runner.DefaultMaxInputTokens,
			},
			wantLifetimeXOut: 64,
		},
		{
			name:   "MaxToolCalls overrides only its own field",
			worker: store.Worker{MaxToolCalls: 7},
			wantCaps: runner.Caps{
				MaxIterations:   12,
				MaxToolCalls:    7,
				MaxWallClock:    300 * time.Second,
				MaxOutputTokens: 4096,
				MaxInputTokens:  runner.DefaultMaxInputTokens,
			},
			wantLifetimeXOut: 0,
		},
		{
			name:   "MaxWallClockSeconds overrides only its own field",
			worker: store.Worker{MaxWallClockSeconds: 30},
			wantCaps: runner.Caps{
				MaxIterations:   12,
				MaxToolCalls:    50,
				MaxWallClock:    30 * time.Second,
				MaxOutputTokens: 4096,
				MaxInputTokens:  runner.DefaultMaxInputTokens,
			},
			wantLifetimeXOut: 0,
		},
		{
			name:   "MaxInputTokens overrides only its own field",
			worker: store.Worker{MaxInputTokens: 100},
			wantCaps: runner.Caps{
				MaxIterations:   12,
				MaxToolCalls:    50,
				MaxWallClock:    300 * time.Second,
				MaxOutputTokens: 4096,
				MaxInputTokens:  100,
			},
			wantLifetimeXOut: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := tt.worker
			got, lifetimeOut := runner.MergeWorkerCapsForTest(base, &w)
			if got != tt.wantCaps {
				t.Fatalf("caps = %+v, want %+v", got, tt.wantCaps)
			}
			if lifetimeOut != tt.wantLifetimeXOut {
				t.Fatalf("lifetimeOutputCap = %d, want %d", lifetimeOut, tt.wantLifetimeXOut)
			}
		})
	}
}
