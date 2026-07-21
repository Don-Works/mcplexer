// handler_tasks_errors.go — typed-error helpers for the task__* MCP
// surface (LLM-ergonomics: actionable error envelopes).
//
// Today most error paths in handler_tasks.go return:
//
//	marshalErrorResult(fmt.Sprintf("List failed: %v", err))
//
// which produces a single nested wrapper string the LLM has to
// reverse-engineer. The helpers here let the same call sites surface a
// structured JSON envelope when the error is a store.FieldError or a
// validator-level fieldArgError:
//
//	{"error": {
//	   "code":    "fts5_reserved_syntax",
//	   "message": "search query contains FTS5 reserved syntax",
//	   "field":   "q",
//	   "value":   "SSE live-test probe",
//	   "hint":    "wrap multi-word terms in double-quotes..."
//	}}
//
// The envelope is still wrapped in an MCP CallToolResult with IsError=
// true, so existing clients keep working — they just now have machine-
// parseable details to act on instead of a free-text wrapper.
package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// fieldArgError is the gateway-side equivalent of store.FieldError —
// used by validation helpers that haven't crossed into the store
// layer yet (id-too-short, bad-RFC3339, unknown-enum). Matches the
// same JSON shape so callers only need to learn one envelope.
type fieldArgError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field"`
	Value   string `json:"value"`
	Hint    string `json:"hint,omitempty"`
	Example string `json:"example,omitempty"`
}

func (e *fieldArgError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s (field=%s value=%q)", e.Code, e.Message, e.Field, e.Value)
}

// newFieldArgError constructs a validator-side typed error.
func newFieldArgError(code, field, value, message, hint string) *fieldArgError {
	const maxValueLen = 512
	if len(value) > maxValueLen {
		value = value[:maxValueLen] + "...(truncated)"
	}
	return &fieldArgError{
		Code:    code,
		Field:   field,
		Value:   value,
		Message: message,
		Hint:    hint,
	}
}

// errorEnvelope is the on-the-wire shape: `{"error": {...}}` so a tool
// caller can parse `result.error.code` regardless of whether the
// underlying source was the store or the handler validator.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
	Value   string `json:"value,omitempty"`
	Hint    string `json:"hint,omitempty"`
	Example string `json:"example,omitempty"`
}

// marshalFieldErrorResult inspects err. If it (or anything in its
// Unwrap chain) is a store.FieldError or a gateway-side
// *fieldArgError, it returns a JSON envelope wrapped in an MCP
// error tool-result and ok=true. Otherwise (nil, false) — caller
// falls through to its existing free-text error path.
func marshalFieldErrorResult(err error) (json.RawMessage, bool) {
	if err == nil {
		return nil, false
	}
	var fe *store.FieldError
	if errors.As(err, &fe) && fe != nil {
		return encodeErrorEnvelope(errorBody{
			Code:    fe.Code,
			Message: fe.Message,
			Field:   fe.Field,
			Value:   fe.Value,
			Hint:    fe.Hint,
			Example: fe.Example,
		}), true
	}
	var ae *fieldArgError
	if errors.As(err, &ae) && ae != nil {
		return encodeErrorEnvelope(errorBody{
			Code:    ae.Code,
			Message: ae.Message,
			Field:   ae.Field,
			Value:   ae.Value,
			Hint:    ae.Hint,
			Example: ae.Example,
		}), true
	}
	return nil, false
}

// encodeErrorEnvelope renders the envelope as a JSON string wrapped
// in an MCP CallToolResult with IsError=true. Done as a string rather
// than embedded JSON so existing text-only MCP clients still see a
// readable body in the content[0].text field.
func encodeErrorEnvelope(body errorBody) json.RawMessage {
	env := errorEnvelope{Error: body}
	pretty, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("%s: %s", body.Code, body.Message))
	}
	return marshalErrorResult(string(pretty))
}

// validateTaskID enforces the "id prefix must be ≥6 chars or a full
// 26-char ULID" rule and returns a fieldArgError when the id is
// rejected. The store layer accepts any non-empty string today so
// this check lives in the handler — it catches the common "I typed
// the first four chars" mistake before the lookup runs.
//
// Returns (nil) when the id is acceptable. The minimum-6 rule matches
// the resolveTaskIDPrefix helper landing in the short-ID task; here
// we duplicate the constant rather than depend on it so this PR
// stays standalone.
func validateTaskID(field, id string) error {
	const minPrefix = 6
	const fullULIDLen = 26
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return newFieldArgError(
			"required_field_missing",
			field, "",
			fmt.Sprintf("%s is required", field),
			"pass a full 26-char ULID (e.g. 01KSGCWX3H8D2CD6N6KM6JETW9) or a ≥6-char prefix",
		)
	}
	if len(trimmed) == fullULIDLen {
		return nil
	}
	if len(trimmed) < minPrefix {
		return newFieldArgError(
			"task_id_prefix_too_short",
			field, trimmed,
			fmt.Sprintf("%s prefix must be at least %d characters (or a full %d-char ULID)", field, minPrefix, fullULIDLen),
			"use more characters of the ULID — short prefixes collide silently in busy workspaces",
		)
	}
	return nil
}
