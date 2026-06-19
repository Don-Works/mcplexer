// handler_tasks_errors_test.go — pins the LLM-ergonomic error
// envelope shape returned to MCP tool callers (code/field/value/hint).
package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestMarshalFieldErrorResult_StoreFieldError(t *testing.T) {
	fe := store.NewFieldError(
		"fts5_reserved_syntax", "q", "SSE live-test probe",
		"search query contains FTS5 reserved syntax",
		"wrap multi-word terms in double-quotes",
	)
	raw, ok := marshalFieldErrorResult(fe)
	if !ok {
		t.Fatal("expected ok=true for *store.FieldError")
	}
	// Parse out the wrapped MCP text content to assert on the JSON body.
	var ctr CallToolResult
	if err := json.Unmarshal(raw, &ctr); err != nil {
		t.Fatalf("unmarshal CallToolResult: %v", err)
	}
	if !ctr.IsError {
		t.Error("expected IsError=true on envelope")
	}
	if len(ctr.Content) != 1 || ctr.Content[0].Type != "text" {
		t.Fatalf("unexpected content shape: %+v", ctr.Content)
	}
	var env errorEnvelope
	if err := json.Unmarshal([]byte(ctr.Content[0].Text), &env); err != nil {
		t.Fatalf("inner envelope is not JSON: %v\n%s", err, ctr.Content[0].Text)
	}
	if env.Error.Code != "fts5_reserved_syntax" {
		t.Errorf("code = %q want fts5_reserved_syntax", env.Error.Code)
	}
	if env.Error.Field != "q" {
		t.Errorf("field = %q want q", env.Error.Field)
	}
	if env.Error.Value != "SSE live-test probe" {
		t.Errorf("value = %q want SSE live-test probe", env.Error.Value)
	}
	if env.Error.Hint == "" {
		t.Error("expected non-empty hint")
	}
}

func TestMarshalFieldErrorResult_WrappedError(t *testing.T) {
	// FieldError wrapped behind fmt.Errorf("xxx: %w", ...) must still
	// be recoverable via errors.As — that's the whole point of the
	// errors.As lookup in marshalFieldErrorResult.
	fe := store.NewFieldError("fts5_reserved_syntax", "q", "OR OR OR",
		"search query contains FTS5 reserved syntax", "drop the operator")
	wrapped := fmt.Errorf("List failed: %w", fe)
	if _, ok := marshalFieldErrorResult(wrapped); !ok {
		t.Fatal("expected wrapped FieldError to surface via errors.As")
	}
}

func TestMarshalFieldErrorResult_FallthroughForUnknownError(t *testing.T) {
	if env, ok := marshalFieldErrorResult(errors.New("plain old error")); ok {
		t.Fatalf("expected ok=false for non-FieldError, got envelope: %s", env)
	}
	if env, ok := marshalFieldErrorResult(nil); ok {
		t.Fatalf("expected ok=false for nil, got envelope: %s", env)
	}
}

func TestMarshalFieldErrorResult_FieldArgError(t *testing.T) {
	ae := newFieldArgError(
		"task_id_prefix_too_short", "id", "01KS",
		"id prefix must be at least 6 characters (or a full 26-char ULID)",
		"use more characters of the ULID",
	)
	raw, ok := marshalFieldErrorResult(ae)
	if !ok {
		t.Fatal("expected ok=true for *fieldArgError")
	}
	var ctr CallToolResult
	if err := json.Unmarshal(raw, &ctr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var env errorEnvelope
	if err := json.Unmarshal([]byte(ctr.Content[0].Text), &env); err != nil {
		t.Fatalf("envelope JSON: %v", err)
	}
	if env.Error.Code != "task_id_prefix_too_short" {
		t.Errorf("code = %q", env.Error.Code)
	}
	if env.Error.Field != "id" || env.Error.Value != "01KS" {
		t.Errorf("field/value = %q/%q", env.Error.Field, env.Error.Value)
	}
}

func TestValidateTaskID(t *testing.T) {
	cases := []struct {
		name     string
		id       string
		wantOK   bool
		wantCode string
	}{
		{"empty", "", false, "required_field_missing"},
		{"all-whitespace", "   ", false, "required_field_missing"},
		{"too-short", "01KS", false, "task_id_prefix_too_short"},
		{"5-chars-still-short", "01KSG", false, "task_id_prefix_too_short"},
		{"6-chars-allowed", "01KSGC", true, ""},
		{"full-ulid", "01KSGCWX3H8D2CD6N6KM6JETW9", true, ""},
		{"trims-spaces-then-validates", "  01KSGCWX3H8D2CD6N6KM6JETW9  ", true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateTaskID("id", c.id)
			if c.wantOK {
				if err != nil {
					t.Fatalf("expected ok, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ae *fieldArgError
			if !errors.As(err, &ae) {
				t.Fatalf("expected *fieldArgError, got %T", err)
			}
			if ae.Code != c.wantCode {
				t.Errorf("code = %q want %q", ae.Code, c.wantCode)
			}
			if ae.Field != "id" {
				t.Errorf("field = %q want id", ae.Field)
			}
			// Hint should always be present for ergonomics.
			if ae.Hint == "" {
				t.Error("expected non-empty hint")
			}
		})
	}
}

func TestFieldArgErrorTruncatesHugeValue(t *testing.T) {
	huge := strings.Repeat("x", 2000)
	ae := newFieldArgError("any", "f", huge, "msg", "hint")
	if len(ae.Value) > 600 {
		t.Errorf("expected truncated, got %d chars", len(ae.Value))
	}
}
