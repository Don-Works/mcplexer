package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// TestValidator_RequireString_Aggregates is the heart of the
// LLM-ergonomics promise: three missing required fields, one response.
func TestValidator_RequireString_Aggregates(t *testing.T) {
	v := newValidator()
	v.requireString("name", "")
	v.requireString("content", "   ") // whitespace counts as missing
	v.requireString("kind", "")
	raw, ok := v.envelope()
	if !ok {
		t.Fatal("expected envelope=true for 3 missing fields")
	}
	var ctr CallToolResult
	if err := json.Unmarshal(raw, &ctr); err != nil {
		t.Fatalf("unmarshal CallToolResult: %v", err)
	}
	if !ctr.IsError {
		t.Error("IsError should be true")
	}
	if len(ctr.Content) != 1 {
		t.Fatalf("expected one text block, got %d", len(ctr.Content))
	}
	var env fieldErrorsEnvelope
	if err := json.Unmarshal([]byte(ctr.Content[0].Text), &env); err != nil {
		t.Fatalf("envelope JSON: %v", err)
	}
	if len(env.Errors) != 3 {
		t.Fatalf("expected 3 errors, got %d", len(env.Errors))
	}
	got := map[string]string{}
	for _, e := range env.Errors {
		got[e.Field] = e.Code
	}
	for _, f := range []string{"name", "content", "kind"} {
		if got[f] != "required_field_missing" {
			t.Errorf("field %s: code = %q want required_field_missing", f, got[f])
		}
	}
	// Legacy `error` should mirror the first.
	if env.Error.Field != "name" {
		t.Errorf("legacy error.field = %q want name", env.Error.Field)
	}
}

// TestValidator_RequireString_Single keeps the single-missing-field
// case behaving like the legacy envelope so existing clients are happy.
func TestValidator_RequireString_Single(t *testing.T) {
	v := newValidator()
	v.requireString("name", "")
	raw, ok := v.envelope()
	if !ok {
		t.Fatal("expected ok=true")
	}
	var ctr CallToolResult
	_ = json.Unmarshal(raw, &ctr)
	var env fieldErrorsEnvelope
	if err := json.Unmarshal([]byte(ctr.Content[0].Text), &env); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if env.Error.Code != "required_field_missing" || env.Error.Field != "name" {
		t.Errorf("legacy fields wrong: %+v", env.Error)
	}
	if len(env.Errors) != 1 {
		t.Errorf("errors len = %d, want 1", len(env.Errors))
	}
}

// TestValidator_Clean_NoEnvelope covers the happy path — no required
// fields missing, no envelope returned, caller proceeds.
func TestValidator_Clean_NoEnvelope(t *testing.T) {
	v := newValidator()
	v.requireString("name", "ok")
	v.requireString("content", "ok")
	if v.hasErrors() {
		t.Error("clean validator should not report errors")
	}
	if _, ok := v.envelope(); ok {
		t.Error("clean validator should not produce envelope")
	}
}

// TestValidator_RequireOneOf_HintListsAllowed asserts the hint
// includes the allowed set so the LLM can pick a valid value next time.
func TestValidator_RequireOneOf_HintListsAllowed(t *testing.T) {
	v := newValidator()
	v.requireOneOf("direction", "sideways", "inbound", "outbound")
	raw, ok := v.envelope()
	if !ok {
		t.Fatal("expected envelope")
	}
	var ctr CallToolResult
	_ = json.Unmarshal(raw, &ctr)
	var env fieldErrorsEnvelope
	_ = json.Unmarshal([]byte(ctr.Content[0].Text), &env)
	if env.Error.Code != "invalid_enum_value" {
		t.Errorf("code = %q want invalid_enum_value", env.Error.Code)
	}
	if !strings.Contains(env.Error.Message, "inbound") || !strings.Contains(env.Error.Message, "outbound") {
		t.Errorf("message should list allowed values: %q", env.Error.Message)
	}
}

// TestValidator_RequireOneOf_EmptyValueIgnored — requireOneOf is for
// "is this enum valid"; combine with requireString to enforce presence.
func TestValidator_RequireOneOf_EmptyValueIgnored(t *testing.T) {
	v := newValidator()
	v.requireOneOf("direction", "", "inbound", "outbound")
	if v.hasErrors() {
		t.Error("empty value should not trigger requireOneOf")
	}
}

// TestValidator_Add_StoreFieldError shows that store-side errors
// (e.g. FTS5 syntax) round-trip into the same aggregated shape.
func TestValidator_Add_StoreFieldError(t *testing.T) {
	v := newValidator()
	v.requireString("missing", "")
	v.add(store.NewFieldError("fts5_reserved_syntax", "q", "AND AND",
		"FTS5 reserved syntax", "wrap multi-word terms"))
	raw, ok := v.envelope()
	if !ok {
		t.Fatal("expected envelope")
	}
	var ctr CallToolResult
	_ = json.Unmarshal(raw, &ctr)
	var env fieldErrorsEnvelope
	_ = json.Unmarshal([]byte(ctr.Content[0].Text), &env)
	if len(env.Errors) != 2 {
		t.Fatalf("errors = %d want 2", len(env.Errors))
	}
	codes := map[string]bool{env.Errors[0].Code: true, env.Errors[1].Code: true}
	if !codes["required_field_missing"] || !codes["fts5_reserved_syntax"] {
		t.Errorf("expected both codes, got %v", codes)
	}
}

// TestMarshalFieldErrorsResult_FiltersUntyped exercises the multi-arg
// helper used by handlers that mix validator output with a single
// failing store call.
func TestMarshalFieldErrorsResult_FiltersUntyped(t *testing.T) {
	mix := []error{
		errors.New("plain old error"),
		newFieldArgError("a", "f1", "", "msg", "hint"),
		fmt.Errorf("wrapped: %w",
			store.NewFieldError("b", "f2", "", "msg2", "hint2")),
		nil,
	}
	raw, ok := marshalFieldErrorsResult(mix)
	if !ok {
		t.Fatal("expected typed errors to be picked up")
	}
	var ctr CallToolResult
	_ = json.Unmarshal(raw, &ctr)
	var env fieldErrorsEnvelope
	_ = json.Unmarshal([]byte(ctr.Content[0].Text), &env)
	if len(env.Errors) != 2 {
		t.Errorf("expected 2 typed errors after filtering, got %d", len(env.Errors))
	}
}

// TestMarshalFieldErrorsResult_NoTyped — no typed errors → no envelope.
func TestMarshalFieldErrorsResult_NoTyped(t *testing.T) {
	if _, ok := marshalFieldErrorsResult(nil); ok {
		t.Error("nil slice should not produce envelope")
	}
	if _, ok := marshalFieldErrorsResult([]error{errors.New("x")}); ok {
		t.Error("non-typed errors should not produce envelope")
	}
}
