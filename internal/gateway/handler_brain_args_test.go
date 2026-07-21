// handler_brain_args_test.go — loud-argument-failure coverage for the
// brain__* tools. brain__search once returned a silently-empty result when
// the caller passed `query` instead of `q` (burning a live debugging
// session); a bare brain__list errored instead of listing. Both must now
// behave helpfully.
package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/brain"
)

// brainResult dispatches a brain tool and returns the parsed CallToolResult
// (including IsError) so a test can assert tool-level errors.
func brainResult(t *testing.T, h *handler, name, args string) CallToolResult {
	t.Helper()
	resp, rpcErr, handled := h.dispatchBrainTool(context.Background(), name, json.RawMessage(args))
	if !handled {
		t.Fatalf("%s not handled", name)
	}
	if rpcErr != nil {
		t.Fatalf("%s rpcErr: %v", name, rpcErr)
	}
	var parsed CallToolResult
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("%s unmarshal result: %v", name, err)
	}
	if len(parsed.Content) == 0 {
		t.Fatalf("%s empty content", name)
	}
	return parsed
}

func TestBrainSearch_MissingQFailsLoudly(t *testing.T) {
	h, _, _ := newHandlerWithBrain(t)

	cases := []struct {
		name string
		args string
	}{
		{name: "query instead of q", args: `{"query":"deploy"}`},
		{name: "no args", args: `{}`},
		{name: "blank q", args: `{"q":"   "}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := brainResult(t, h, "brain__search", tc.args)
			if !res.IsError {
				t.Fatalf("expected isError=true for %s, got: %+v", tc.args, res)
			}
			if !strings.Contains(res.Content[0].Text, "q is required") {
				t.Errorf("error must name the missing arg, got: %s", res.Content[0].Text)
			}
		})
	}
}

func TestBrainList_BareCallListsBothKinds(t *testing.T) {
	h, _, ed := newHandlerWithBrain(t)
	ctx := context.Background()
	if _, err := ed.SaveTask(ctx, brain.TaskRecord{
		Workspace: "ws", Title: "Fix the scheduler", Status: "open",
	}, nil); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if _, err := ed.SaveMemory(ctx, brain.MemoryRecord{
		Kind: brain.MemoryKindNote, Name: "deploy-runbook", Content: "ship it", Workspace: "ws",
	}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	res := brainResult(t, h, "brain__list", `{}`)
	if res.IsError {
		t.Fatalf("bare brain__list must not error: %s", res.Content[0].Text)
	}
	body := res.Content[0].Text
	for _, want := range []string{`"tasks"`, `"memories"`, "Fix the scheduler", "deploy-runbook"} {
		if !strings.Contains(body, want) {
			t.Errorf("bare brain__list missing %q in:\n%s", want, body)
		}
	}
}

func TestBrainList_UnknownKindFailsLoudly(t *testing.T) {
	h, _, _ := newHandlerWithBrain(t)
	res := brainResult(t, h, "brain__list", `{"kind":"person"}`)
	if !res.IsError {
		t.Fatalf("unknown kind must error, got: %+v", res)
	}
	if !strings.Contains(res.Content[0].Text, "kind must be") {
		t.Errorf("error must explain valid kinds, got: %s", res.Content[0].Text)
	}
}
