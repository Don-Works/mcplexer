package gateway

import (
	"context"
	"encoding/json"
	"testing"
)

func TestBuiltinDispatchRoutesTaskHeartbeatAndSetWorkContext(t *testing.T) {
	ctx := context.Background()
	h, _, _ := newTasksHandler(t)

	createArgs, err := json.Marshal(map[string]any{
		"title": "dispatch regression",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, rpcErr := h.handleBuiltinCall(ctx, CallToolRequest{
		Name:      "task__create",
		Arguments: createArgs,
	})
	if rpcErr != nil {
		t.Fatalf("create rpc error: %v", rpcErr)
	}
	taskID := unwrapResult(t, resp)["id"].(string)

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{
			name: "task__heartbeat",
			args: map[string]any{"id": taskID},
		},
		{
			name: "task__set_work_context",
			args: map[string]any{"id": taskID, "work_context": "ready for review"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			resp, rpcErr := h.handleBuiltinCall(ctx, CallToolRequest{
				Name:      tc.name,
				Arguments: raw,
			})
			if rpcErr != nil {
				t.Fatalf("rpc error: %v", rpcErr)
			}
			if got := unwrapResult(t, resp); got["id"] != taskID {
				t.Fatalf("result id = %v, want %s", got["id"], taskID)
			}
		})
	}
}
