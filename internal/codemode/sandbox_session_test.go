package codemode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func runWithSession(t *testing.T, code string, initial map[string]json.RawMessage, maxBytes int) *ExecutionResult {
	t.Helper()
	s := NewSandbox(newMockCaller(), 5*time.Second)
	s.SetSessionState(initial, maxBytes)
	res, err := s.Execute(context.Background(), code, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return res
}

// The headline use case: build a value once, get it back next call.
func TestSessionStatePersistAndRehydrate(t *testing.T) {
	res := runWithSession(t, `session.DATA = {customers: 3, names: ["a","b"]}`, nil, 1<<20)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if _, ok := res.SessionState["DATA"]; !ok {
		t.Fatalf("DATA not snapshotted: %+v", res.SessionState)
	}

	// Second call rehydrates from the prior snapshot and mutates it.
	res2 := runWithSession(t, `print(session.DATA.customers); session.DATA.customers += 1`, res.SessionState, 1<<20)
	if res2.Error != "" {
		t.Fatalf("unexpected error 2: %s", res2.Error)
	}
	if !strings.Contains(res2.Output, "3") {
		t.Fatalf("expected rehydrated value 3 in output, got %q", res2.Output)
	}
	var v map[string]any
	if err := json.Unmarshal(res2.SessionState["DATA"], &v); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if v["customers"].(float64) != 4 {
		t.Fatalf("expected mutated customers=4, got %v", v["customers"])
	}
}

func TestSessionStateDeleteReflected(t *testing.T) {
	initial := map[string]json.RawMessage{"A": json.RawMessage(`1`), "B": json.RawMessage(`2`)}
	res := runWithSession(t, `delete session.A`, initial, 1<<20)
	if _, ok := res.SessionState["A"]; ok {
		t.Fatal("deleted key A should not be in snapshot")
	}
	if _, ok := res.SessionState["B"]; !ok {
		t.Fatal("untouched key B should remain in snapshot")
	}
}

func TestSessionStateSkipsNonSerializable(t *testing.T) {
	res := runWithSession(t, `session.fn = function(){ return 1 }; session.ok = 5`, nil, 1<<20)
	if _, ok := res.SessionState["fn"]; ok {
		t.Fatal("function value should be skipped")
	}
	if _, ok := res.SessionState["ok"]; !ok {
		t.Fatal("plain value should persist")
	}
	if res.SessionStateWarning == "" {
		t.Fatal("expected a warning naming the skipped key")
	}
}

func TestSessionStateNullRoundTrips(t *testing.T) {
	res := runWithSession(t, `session.x = null`, nil, 1<<20)
	raw, ok := res.SessionState["x"]
	if !ok {
		t.Fatalf("null value should persist, got %+v", res.SessionState)
	}
	if strings.TrimSpace(string(raw)) != "null" {
		t.Fatalf("expected null, got %s", raw)
	}
}

func TestSessionStateOverCapNotPersisted(t *testing.T) {
	code := `var s = ""; for (var i = 0; i < 200; i++) { s += "abcde"; } session.big = s;`
	res := runWithSession(t, code, nil, 100)
	if res.SessionState != nil {
		t.Fatalf("over-cap snapshot should be dropped, got %+v", res.SessionState)
	}
	if res.SessionStateWarning == "" {
		t.Fatal("expected over-cap warning")
	}
}

func TestSessionStateNotPersistedOnError(t *testing.T) {
	res := runWithSession(t, `session.x = 1; throw new Error("boom")`, nil, 1<<20)
	if res.Error == "" {
		t.Fatal("expected a runtime error")
	}
	if res.SessionState != nil {
		t.Fatalf("session state must not be snapshotted on error, got %+v", res.SessionState)
	}
}

// When session-state is OFF, referencing `session` throws a bare
// "ReferenceError: session is not defined" — which reads like the agent's bug.
// The annotation must signpost it as an unavailable feature and point at kv.
func TestSessionStateDisabledSignpostsReference(t *testing.T) {
	s := NewSandbox(newMockCaller(), 5*time.Second)
	res, err := s.Execute(context.Background(), `session.x = 1`, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected a ReferenceError for an undefined session")
	}
	if !strings.Contains(res.Error, "not available on this connection") ||
		!strings.Contains(res.Error, "kv.set") {
		t.Fatalf("expected the session-not-enabled signpost pointing at kv, got: %q", res.Error)
	}
}

func TestSessionStateDisabledLeavesNoObject(t *testing.T) {
	s := NewSandbox(newMockCaller(), 5*time.Second)
	res, err := s.Execute(context.Background(), `print(typeof session)`, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.SessionState != nil {
		t.Fatalf("disabled feature must not snapshot, got %+v", res.SessionState)
	}
	if !strings.Contains(res.Output, "undefined") {
		t.Fatalf("session should be undefined when feature disabled, got %q", res.Output)
	}
}
