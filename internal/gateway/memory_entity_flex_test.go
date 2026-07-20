// memory_entity_flex_test.go — coverage for the flexible `entities`
// argument (audit finding, 2026-07-18: 6 memory__save calls lost to
// "cannot unmarshal string into Go struct field .entities").
//
// The pre-fix failure was especially bad because it happened inside
// encoding/json before the handler ran, so the caller got a bare -32602
// protocol error with no hint about the expected shape.
package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestFlexEntities_AcceptsObservedShapes(t *testing.T) {
	tests := []struct {
		name string
		json string
		want []entityArg
	}{
		{
			name: "documented array of objects",
			json: `[{"kind":"task","id":"01ABC","role":"subject"}]`,
			want: []entityArg{{Kind: "task", ID: "01ABC", Role: "subject"}},
		},
		{
			name: "bare string array of kind:id",
			json: `["task:01ABC","person:a@example.com"]`,
			want: []entityArg{
				{Kind: "task", ID: "01ABC"},
				{Kind: "person", ID: "a@example.com"},
			},
		},
		{
			name: "single object, not wrapped in an array",
			json: `{"kind":"task","id":"01ABC"}`,
			want: []entityArg{{Kind: "task", ID: "01ABC"}},
		},
		{
			name: "single bare string",
			json: `"task:01ABC"`,
			want: []entityArg{{Kind: "task", ID: "01ABC"}},
		},
		{
			name: "mixed array of objects and strings",
			json: `[{"kind":"org","id":"acme"},"task:01ABC"]`,
			want: []entityArg{
				{Kind: "org", ID: "acme"},
				{Kind: "task", ID: "01ABC"},
			},
		},
		{
			name: "id containing colons splits on the first only",
			json: `["artifact:gh:acme/repo#12"]`,
			want: []entityArg{{Kind: "artifact", ID: "gh:acme/repo#12"}},
		},
		{
			name: "kind prefix is case-insensitive",
			json: `["Person:a@example.com"]`,
			want: []entityArg{{Kind: "person", ID: "a@example.com"}},
		},
		{
			name: "whitespace is trimmed",
			json: `[" task : 01ABC "]`,
			want: []entityArg{{Kind: "task", ID: "01ABC"}},
		},
		{
			name: "null yields no refs",
			json: `null`,
			want: nil,
		},
		{
			name: "empty array yields no refs",
			json: `[]`,
			want: []entityArg{},
		},
		{
			name: "null elements are skipped",
			json: `["task:01ABC",null]`,
			want: []entityArg{{Kind: "task", ID: "01ABC"}},
		},
		{
			// An unreserved prefix is NOT split — guessing a kind would
			// silently change recall scope semantics.
			name: "unreserved prefix is not treated as a kind",
			json: `["https://example.com/x"]`,
			want: []entityArg{{ID: "https://example.com/x"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got flexEntities
			if err := json.Unmarshal([]byte(tc.json), &got); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.json, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d refs %+v, want %d %+v",
					len(got), got, len(tc.want), tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("ref[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestValidateEntities_FlagsKindlessRefs(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr bool
	}{
		{"kinded refs are fine", `["task:01ABC"]`, false},
		{"object refs are fine", `[{"kind":"org","id":"acme"}]`, false},
		{"empty is fine", `[]`, false},
		{"bare slug has no kind", `["acme"]`, true},
		{"one bad among good still flags", `["task:01ABC","acme"]`, true},
		{"object missing kind is flagged", `[{"id":"acme"}]`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var ents flexEntities
			if err := json.Unmarshal([]byte(tc.json), &ents); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			fieldErr := validateEntities("entities", ents)
			if tc.wantErr != (fieldErr != nil) {
				t.Fatalf("validateEntities(%s) err=%v, wantErr=%v",
					tc.json, fieldErr, tc.wantErr)
			}
			if fieldErr == nil {
				return
			}
			if fieldErr.Field != "entities" {
				t.Fatalf("field = %q, want entities", fieldErr.Field)
			}
			// The hint must teach BOTH accepted shapes and name the
			// reserved kinds, so the caller can fix it in one retry.
			for _, want := range []string{"kind:id", "task", "person", "workspace"} {
				if !strings.Contains(fieldErr.Hint, want) {
					t.Fatalf("hint %q missing %q", fieldErr.Hint, want)
				}
			}
		})
	}
}

// TestMemorySave_AcceptsFlexibleEntityShapes is the end-to-end regression:
// pre-fix, each of these returned a bare -32602 RPCError from json.Unmarshal
// and the memory was never written. Post-fix the save lands AND the entity
// links are actually persisted (asserted by reading them back via
// memory__recall_about, not by trusting the response text).
func TestMemorySave_AcceptsFlexibleEntityShapes(t *testing.T) {
	tests := []struct {
		name      string
		entities  string
		wantLinks int
	}{
		{"bare string array", `["task:01ABC","person:a@example.com"]`, 2},
		{"single object", `{"kind":"task","id":"01ABC"}`, 1},
		{"single string", `"task:01ABC"`, 1},
		{"documented array of objects", `[{"kind":"task","id":"01ABC"}]`, 1},
		{"mixed", `[{"kind":"org","id":"acme"},"task:01ABC"]`, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h, _, _ := newHandlerWithMemoryStore(t)

			body := []byte(`{"scope":"global","name":"rollout-plan",` +
				`"content":"the rollback gate requires a second approver on production",` +
				`"entities":` + tc.entities + `}`)
			resp, rpcErr, handled := h.dispatchMemoryTool(ctx, "memory__save", body)
			if !handled {
				t.Fatal("memory__save not handled")
			}
			if rpcErr != nil {
				t.Fatalf("entities=%s rejected at the JSON layer: %v",
					tc.entities, rpcErr)
			}
			txt := toolResultText(t, resp)
			if !strings.Contains(txt, "Saved memory") {
				t.Fatalf("save did not succeed: %s", txt)
			}

			// Read the links back: every shape must produce real entity refs.
			about, _ := json.Marshal(map[string]any{"kind": "task", "id": "01ABC"})
			aResp, aErr, _ := h.dispatchMemoryTool(ctx, "memory__recall_about", about)
			if aErr != nil {
				t.Fatalf("recall_about: %v", aErr)
			}
			var env memoryRecallEnvelope
			if err := json.Unmarshal([]byte(toolResultText(t, aResp)), &env); err != nil {
				t.Fatalf("decode recall_about envelope: %v", err)
			}
			if env.Count != 1 {
				t.Fatalf("task:01ABC should link exactly 1 memory, got %d", env.Count)
			}
		})
	}
}

// TestMemorySave_KindlessEntityGetsActionableError proves the ambiguous case
// fails LOUDLY with guidance rather than silently dropping the link (the
// pre-existing toEntityRefs behaviour) or dying as an opaque -32602.
func TestMemorySave_KindlessEntityGetsActionableError(t *testing.T) {
	ctx := context.Background()
	h, _, _ := newHandlerWithMemoryStore(t)

	body := []byte(`{"scope":"global","content":` +
		`"the rollback gate requires a second approver on production",` +
		`"entities":["acme"]}`)
	resp, rpcErr, _ := h.dispatchMemoryTool(ctx, "memory__save", body)
	if rpcErr != nil {
		t.Fatalf("should be a structured tool error, got RPC error: %v", rpcErr)
	}
	isErr, txt := toolResultErrorText(t, resp)
	if !isErr {
		t.Fatalf("kindless entity should error, got: %s", txt)
	}
	for _, want := range []string{"invalid_argument_shape", "acme", "kind:id", "task, person"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("error missing %q: %s", want, txt)
		}
	}
}

// TestMemoryRecall_AcceptsFlexibleEntityShapes covers the filter side —
// entities/entities_any on recall took the same []entityArg type and failed
// the same way.
func TestMemoryRecall_AcceptsFlexibleEntityShapes(t *testing.T) {
	tests := []struct {
		name string
		args string
	}{
		{"entities as string array", `{"entities":["task:01ABC"]}`},
		{"entities_any as string array", `{"entities_any":["task:01ABC"]}`},
		{"entities as single object", `{"entities":{"kind":"task","id":"01ABC"}}`},
		{"entities as single string", `{"entities":"task:01ABC"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h, _, _ := newHandlerWithMemoryStore(t)

			resp, rpcErr, handled := h.dispatchMemoryTool(
				ctx, "memory__recall", []byte(tc.args))
			if !handled {
				t.Fatal("memory__recall not handled")
			}
			if rpcErr != nil {
				t.Fatalf("%s rejected at the JSON layer: %v", tc.args, rpcErr)
			}
			var env memoryRecallEnvelope
			if err := json.Unmarshal([]byte(toolResultText(t, resp)), &env); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if env.Count != 0 {
				t.Fatalf("empty store should recall nothing, got %d", env.Count)
			}
		})
	}
}
