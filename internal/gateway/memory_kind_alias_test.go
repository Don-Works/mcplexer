// memory_kind_alias_test.go — coverage for the memory__save `kind` alias
// mapping (audit finding, 2026-07-18: 17/200 saves lost to "invalid kind").
//
// Every alias in the observed list is asserted BOTH at the pure-function
// level and end-to-end through dispatchMemoryTool, because the pure mapping
// being right is worthless if the handler still hands the raw string to
// WriteMemory. The end-to-end cases read the persisted row back so a save
// that "succeeded" with the wrong kind cannot pass.
package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestResolveMemoryKind_ObservedAliases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// The five kinds the audit log actually captured.
		{"decision (15 of 17 failures)", "decision", store.MemoryKindFact},
		{"preference", "preference", store.MemoryKindFact},
		{"anti-pattern", "anti-pattern", store.MemoryKindNote},
		{"project", "project", store.MemoryKindFact},
		{"project_fact", "project_fact", store.MemoryKindFact},

		// Canonical values must still pass straight through.
		{"canonical fact", "fact", store.MemoryKindFact},
		{"canonical note", "note", store.MemoryKindNote},

		// Separator + case insensitivity: one alias entry covers every
		// spelling an agent might reach for.
		{"underscored anti_pattern", "anti_pattern", store.MemoryKindNote},
		{"squashed antipattern", "antipattern", store.MemoryKindNote},
		{"title case", "Decision", store.MemoryKindFact},
		{"shouted", "PREFERENCE", store.MemoryKindFact},
		{"padded", "  decision  ", store.MemoryKindFact},
		{"inner space", "project fact", store.MemoryKindFact},

		// Plurals and near neighbours in the same semantic family.
		{"plural decisions", "decisions", store.MemoryKindFact},
		{"adr", "adr", store.MemoryKindFact},
		{"setting", "setting", store.MemoryKindFact},
		{"lesson", "lesson", store.MemoryKindNote},
		{"observation", "observation", store.MemoryKindNote},

		// Empty stays empty so the store applies its own note default
		// rather than the gateway silently pinning one.
		{"empty passes through", "", ""},
		{"whitespace only", "   ", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, fieldErr := resolveMemoryKind(tc.input)
			if fieldErr != nil {
				t.Fatalf("resolveMemoryKind(%q) unexpected error: %s",
					tc.input, fieldErr.Message)
			}
			if got != tc.want {
				t.Fatalf("resolveMemoryKind(%q) = %q, want %q",
					tc.input, got, tc.want)
			}
		})
	}
}

func TestResolveMemoryKind_UnknownReturnsDidYouMean(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantSuggest string // "" = no suggestion expected, just the rule
	}{
		{"typo near decision", "decsion", "decision"},
		{"typo near preference", "preferance", "preference"},
		{"typo near note", "nte", "note"},
		{"typo near fact", "fct", "fact"},
		{"nonsense gets the rule only", "zzzzzzzzzzzzzzzzq", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, fieldErr := resolveMemoryKind(tc.input)
			if fieldErr == nil {
				t.Fatalf("resolveMemoryKind(%q) = %q, want a field error",
					tc.input, got)
			}
			if got != "" {
				t.Fatalf("rejected kind should resolve to empty, got %q", got)
			}
			if fieldErr.Field != "kind" {
				t.Fatalf("field = %q, want %q", fieldErr.Field, "kind")
			}
			if fieldErr.Code != "invalid_enum_value" {
				t.Fatalf("code = %q, want invalid_enum_value", fieldErr.Code)
			}
			// The message must name the valid set — that is the whole point
			// of the did-you-mean path over a bare validation failure.
			for _, want := range []string{store.MemoryKindFact, store.MemoryKindNote} {
				if !strings.Contains(fieldErr.Message, want) {
					t.Fatalf("message %q does not name valid kind %q",
						fieldErr.Message, want)
				}
			}
			if tc.wantSuggest != "" &&
				!strings.Contains(fieldErr.Hint, tc.wantSuggest) {
				t.Fatalf("hint %q does not suggest %q",
					fieldErr.Hint, tc.wantSuggest)
			}
		})
	}
}

// toolResultErrorText reads a tool result WITHOUT failing on isError —
// the shared toolResultText helper treats an error envelope as a test
// failure, and these tests assert on the error envelope's contents.
func toolResultErrorText(t *testing.T, raw json.RawMessage) (bool, string) {
	t.Helper()
	var env struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal tool result: %v (raw=%s)", err, string(raw))
	}
	if len(env.Content) == 0 {
		t.Fatalf("empty tool result: %s", string(raw))
	}
	return env.IsError, env.Content[0].Text
}

// saveWithKind dispatches memory__save with an explicit kind and a distinct
// name (so fact supersession between cases cannot mask a wrong mapping).
func saveWithKind(
	t *testing.T, h *handler, ctx context.Context, name, kind string,
) json.RawMessage {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"name":    name,
		"kind":    kind,
		"scope":   "global",
		"content": "the deployment rollback gate requires a second approver on production",
	})
	resp, rpcErr, handled := h.dispatchMemoryTool(ctx, "memory__save", body)
	if !handled || rpcErr != nil {
		t.Fatalf("memory__save(kind=%q): handled=%v rpcErr=%v", kind, handled, rpcErr)
	}
	return resp
}

// TestMemorySave_AliasKindsPersistWithCanonicalKind is the regression that
// fails on the pre-fix code: every one of these returned
// `Save failed: WriteMemory: invalid kind "<alias>"` and stored nothing.
func TestMemorySave_AliasKindsPersistWithCanonicalKind(t *testing.T) {
	tests := []struct {
		alias string
		want  string
	}{
		{"decision", store.MemoryKindFact},
		{"preference", store.MemoryKindFact},
		{"project", store.MemoryKindFact},
		{"project_fact", store.MemoryKindFact},
		{"anti-pattern", store.MemoryKindNote},
	}
	for _, tc := range tests {
		t.Run(tc.alias, func(t *testing.T) {
			ctx := context.Background()
			h, svc, _ := newHandlerWithMemoryStore(t)

			resp := saveWithKind(t, h, ctx, "gate-policy-"+tc.alias, tc.alias)
			txt := toolResultText(t, resp)
			if strings.Contains(txt, "Save failed") || strings.Contains(txt, "invalid kind") {
				t.Fatalf("save with kind=%q was rejected: %s", tc.alias, txt)
			}

			// End-to-end: read the row back and assert the CANONICAL kind
			// landed. A save that reported success but stored the raw alias
			// (or the wrong canonical kind) fails here.
			id := structuredID(t, resp)
			entry, err := svc.Get(ctx, id)
			if err != nil {
				t.Fatalf("read back %s: %v", id, err)
			}
			if entry.Kind != tc.want {
				t.Fatalf("stored kind = %q, want %q (alias %q)",
					entry.Kind, tc.want, tc.alias)
			}
		})
	}
}

// TestMemorySave_UnmappableKindReturnsDidYouMean proves the reject path is a
// structured field error naming the valid set, not the store's bare
// "WriteMemory: invalid kind" string.
func TestMemorySave_UnmappableKindReturnsDidYouMean(t *testing.T) {
	ctx := context.Background()
	h, _, _ := newHandlerWithMemoryStore(t)

	resp := saveWithKind(t, h, ctx, "gate-policy-bad", "decsion")
	isErr, txt := toolResultErrorText(t, resp)
	if !isErr {
		t.Fatalf("unmappable kind should return an error envelope, got: %s", txt)
	}

	if strings.Contains(txt, "WriteMemory") {
		t.Fatalf("error leaked the store's bare validation message: %s", txt)
	}
	for _, want := range []string{"invalid_enum_value", "did you mean", "decision", "fact", "note"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("did-you-mean response missing %q: %s", want, txt)
		}
	}
}
