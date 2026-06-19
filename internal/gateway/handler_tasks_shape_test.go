// handler_tasks_shape_test.go — guards the null → [] normalisation for
// the task__* MCP surface (task 01KSGHS25GM0BG8K6T7EEFHSDN).
//
// Every collection field in task__list / task__get / task__update
// responses must marshal to `[]` (or `{}`) when empty, never `null`.
// Tests assert on the raw JSON bytes so a future regression that
// reintroduces `null` is caught at the wire level — not just at the
// Go-struct level (where nil-vs-empty-slice round-trips deceptively).
package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
)

// bindSessionWorkspace seeds the session manager's workspace chain so
// the workspaceID() probe in the task handlers returns wsID. Used by
// tests that exercise the MCP handler without a full transport setup.
func bindSessionWorkspace(h *handler, wsID string) {
	h.sessions.wsChain = []routing.WorkspaceAncestor{{ID: wsID, RootPath: "/tmp/ws-admin"}}
}

// rawResultText extracts the raw text of an MCP CallToolResult's first
// content block. Tests on the marshal shape (does `[]` appear?) need
// the unparsed bytes, not the round-tripped map[string]any.
func rawResultText(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var env struct {
		Content []struct{ Type, Text string }
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unwrap envelope: %v (raw=%s)", err, string(raw))
	}
	if env.IsError {
		t.Fatalf("expected isError=false, got error result: %s", string(raw))
	}
	if len(env.Content) == 0 {
		t.Fatalf("empty content envelope: %s", string(raw))
	}
	return env.Content[0].Text
}

// TestTaskList_EmptyResultReturnsEmptyArray asserts that an empty
// list response carries `"tasks": []`, NOT `"tasks": null`. The
// known_* envelope fields must also be `[]` for the same reason.
func TestTaskList_EmptyResultReturnsEmptyArray(t *testing.T) {
	ctx := context.Background()
	h, _, wsID := newTasksHandler(t)
	bindSessionWorkspace(h, wsID)
	// No tasks seeded — list should be empty.
	raw, rpcErr := h.handleTaskList(ctx, json.RawMessage(`{"q":"no-such-task"}`))
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	body := rawResultText(t, raw)

	mustHave := []string{
		`"tasks": []`,
		`"known_assignees": []`,
		`"known_statuses": [`, // may be empty `[]` or canonical defaults
		`"known_tags": []`,
		`"known_meta_keys": []`,
	}
	for _, want := range mustHave {
		if !strings.Contains(body, want) {
			t.Errorf("task__list body missing %q:\n%s", want, body)
		}
	}
	mustNotHave := []string{
		`"tasks": null`,
		`"known_assignees": null`,
		`"known_statuses": null`,
		`"known_tags": null`,
		`"known_meta_keys": null`,
	}
	for _, bad := range mustNotHave {
		if strings.Contains(body, bad) {
			t.Errorf("task__list body contains %q (should be []):\n%s", bad, body)
		}
	}
}

// TestTaskGet_EmptySiblingsReturnEmptyArrays asserts task__get of a
// task with no notes / no composed_by / no composes returns empty
// arrays for each, never null.
func TestTaskGet_EmptySiblingsReturnEmptyArrays(t *testing.T) {
	ctx := context.Background()
	h, db, wsID := newTasksHandler(t)
	bindSessionWorkspace(h, wsID)

	row := &store.Task{
		WorkspaceID: wsID,
		Title:       "lone task",
	}
	if err := db.CreateTask(ctx, row); err != nil {
		t.Fatalf("create task: %v", err)
	}

	raw, rpcErr := h.handleTaskGet(ctx, json.RawMessage(`{"id":"`+row.ID+`"}`))
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	body := rawResultText(t, raw)

	mustHave := []string{
		`"notes": []`,
		`"composed_by": []`,
		`"composes": []`,
		`"known_assignees": []`,
	}
	for _, want := range mustHave {
		if !strings.Contains(body, want) {
			t.Errorf("task__get body missing %q:\n%s", want, body)
		}
	}
	mustNotHave := []string{
		`"notes": null`,
		`"composed_by": null`,
		`"composes": null`,
		`"known_assignees": null`,
	}
	for _, bad := range mustNotHave {
		if strings.Contains(body, bad) {
			t.Errorf("task__get body contains %q (should be []):\n%s", bad, body)
		}
	}
}

// TestTaskUpdate_EmptyBulkReturnsEmptyArrays asserts bulk task__update
// with all-failed (or all-empty) returns `ok: []` / `failed: []`,
// never null. Hits the bulk branch with a single non-existent id so
// the ok side stays empty.
func TestTaskUpdate_EmptyBulkReturnsEmptyArrays(t *testing.T) {
	ctx := context.Background()
	h, _, wsID := newTasksHandler(t)
	bindSessionWorkspace(h, wsID)

	args := map[string]any{
		"ids":   []string{"01KSGCBWR4EM6GSSVG4V40B3H0"}, // does not exist
		"title": "noop",
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	resp, rpcErr := h.handleTaskUpdate(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("rpc error: %v", rpcErr)
	}
	body := rawResultText(t, resp)
	// ok should be `[]` (no successful updates); failed should be a
	// populated array with one error row.
	if !strings.Contains(body, `"ok": []`) {
		t.Errorf("expected `\"ok\": []` in bulk-update body:\n%s", body)
	}
	if strings.Contains(body, `"ok": null`) {
		t.Errorf("bulk-update body contains `\"ok\": null`:\n%s", body)
	}
	if strings.Contains(body, `"failed": null`) {
		t.Errorf("bulk-update body contains `\"failed\": null`:\n%s", body)
	}
}
