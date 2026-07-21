package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newHandlerWithKVDB(t *testing.T) *handler {
	t.Helper()
	db, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.store = db
	h.sessions.wsChain = []routing.WorkspaceAncestor{{ID: "ws-kv", RootPath: "/tmp/ws-kv"}}
	return h
}

func kvText(t *testing.T, h *handler, name, args string) string {
	t.Helper()
	raw, rpcErr, handled := h.dispatchKVTool(context.Background(), name, json.RawMessage(args))
	if !handled {
		t.Fatalf("%s not handled", name)
	}
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	return rawResultText(t, raw)
}

func kvErrText(t *testing.T, h *handler, name, args string) string {
	t.Helper()
	raw, rpcErr, handled := h.dispatchKVTool(context.Background(), name, json.RawMessage(args))
	if !handled {
		t.Fatalf("%s not handled", name)
	}
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	var env struct {
		Content []struct{ Type, Text string }
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unwrap envelope: %v (raw=%s)", err, string(raw))
	}
	if !env.IsError {
		t.Fatalf("expected isError=true, got %s", string(raw))
	}
	if len(env.Content) == 0 {
		t.Fatalf("empty content envelope: %s", string(raw))
	}
	return env.Content[0].Text
}

func TestKVRoundTrip(t *testing.T) {
	h := newHandlerWithKVDB(t)
	set := kvText(t, h, "kv__set",
		`{"key":"customers","value":{"count":2,"names":["acme","globex"],"revenue":1234.5}}`)
	if !strings.Contains(set, `"ok":true`) || !strings.Contains(set, `"bytes":`) {
		t.Fatalf("set body: %s", set)
	}
	// kv__get returns the value verbatim (auto-unwrapped in the sandbox).
	got := kvText(t, h, "kv__get", `{"key":"customers"}`)
	if !strings.Contains(got, "globex") || !strings.Contains(got, "1234.5") {
		t.Fatalf("get did not rehydrate the stored value: %s", got)
	}
}

func TestKVGetMissingReturnsNull(t *testing.T) {
	h := newHandlerWithKVDB(t)
	got := kvText(t, h, "kv__get", `{"key":"nope"}`)
	if strings.TrimSpace(got) != "null" {
		t.Fatalf("missing key should return null, got %q", got)
	}
}

func TestKVListPrefixAndIdempotentDelete(t *testing.T) {
	h := newHandlerWithKVDB(t)
	_ = kvText(t, h, "kv__set", `{"key":"a","value":1}`)
	_ = kvText(t, h, "kv__set", `{"key":"ab","value":2}`)
	_ = kvText(t, h, "kv__set", `{"key":"b","value":3}`)

	list := kvText(t, h, "kv__list", `{}`)
	if !strings.Contains(list, `"count":3`) {
		t.Fatalf("list count: %s", list)
	}
	pref := kvText(t, h, "kv__list", `{"prefix":"a"}`)
	if !strings.Contains(pref, `"count":2`) {
		t.Fatalf("prefix list: %s", pref)
	}

	del := kvText(t, h, "kv__delete", `{"key":"a"}`)
	if !strings.Contains(del, `"deleted":true`) {
		t.Fatalf("delete existing: %s", del)
	}
	del2 := kvText(t, h, "kv__delete", `{"key":"a"}`)
	if !strings.Contains(del2, `"deleted":false`) {
		t.Fatalf("idempotent delete of absent key: %s", del2)
	}
}

func TestKVValueTooLargeRejected(t *testing.T) {
	h := newHandlerWithKVDB(t)
	big := strings.Repeat("x", kvMaxValueBytes+10)
	args, _ := json.Marshal(map[string]any{"key": "big", "value": big})
	msg := kvErrText(t, h, "kv__set", string(args))
	if !strings.Contains(msg, "too large") {
		t.Fatalf("expected too-large error, got %s", msg)
	}
}

func TestKVKeyCountCapRejected(t *testing.T) {
	h := newHandlerWithKVDB(t)
	for i := range kvMaxKeysPerWorkspace {
		args, _ := json.Marshal(map[string]any{"key": fmt.Sprintf("k%d", i), "value": 1})
		_ = kvText(t, h, "kv__set", string(args))
	}
	msg := kvErrText(t, h, "kv__set", `{"key":"overflow","value":1}`)
	if !strings.Contains(msg, "too many keys") {
		t.Fatalf("expected key-cap error, got %s", msg)
	}
}
