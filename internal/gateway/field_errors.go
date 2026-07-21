// field_errors.go — aggregated field-validation envelope shared across
// every builtin tool family (task__*, memory__*, mesh__*, secret__*,
// skill__*, mcplexer__*).
//
// Background
// ----------
// 0cc9fdd ("feat(tasks): structured FieldError + FTS5 error rewrite")
// introduced `store.FieldError` and the gateway-side `fieldArgError`
// (in handler_tasks_errors.go) so the *task* tools could return a typed
// `{error:{code,field,value,hint,...}}` body instead of a free-text
// "List failed: %v" string. The other tool families still return on
// first failure, which forces the LLM into N round-trips when N
// required fields are missing.
//
// This file generalises the pattern:
//
//  1. `validator` accumulates `*fieldArgError`s (and arbitrary errors
//     turned into them) before any work happens. The handler walks
//     every required-field check; the validator yields a single
//     aggregated envelope only after all checks ran.
//  2. `marshalFieldErrorsResult([]error)` produces the wire envelope.
//     Backward compatible with the single-error envelope from
//     handler_tasks_errors.go — the legacy `error.{code,field,...}`
//     fields mirror the FIRST error so older clients that only look at
//     `error.code` keep working. The full list ships under `errors:[]`.
//
// Wire shape
// ----------
//
//	{
//	  "error":  { code, field, value, message, hint, example }, // first
//	  "errors": [ {code, field, value, message, hint, example}, ... ]
//	}
//
// The MCP CallToolResult wraps this JSON in a text content block with
// IsError=true, exactly like the single-error envelope.
package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// validator aggregates field-validation errors so a handler can report
// every problem in one response instead of fail-on-first.
//
// Zero-value is ready to use. The typical handler shape is:
//
//	v := newValidator()
//	v.requireString("name", args.Name)
//	v.requireString("content", args.Content)
//	if env, ok := v.envelope(); ok {
//		return env, nil
//	}
//
// requireString / requireOneOf / addErr append. `envelope` returns
// (json.RawMessage, true) when one or more errors accumulated.
type validator struct {
	errs []*fieldArgError
}

// newValidator returns an empty validator. Provided for symmetry; the
// zero-value works too.
func newValidator() *validator { return &validator{} }

// add appends an error if non-nil. Accepts *fieldArgError and
// *store.FieldError transparently; anything else is wrapped into a
// generic fieldArgError.
func (v *validator) add(err error) {
	if err == nil {
		return
	}
	var ae *fieldArgError
	if errors.As(err, &ae) && ae != nil {
		v.errs = append(v.errs, ae)
		return
	}
	var fe *store.FieldError
	if errors.As(err, &fe) && fe != nil {
		v.errs = append(v.errs, &fieldArgError{
			Code:    fe.Code,
			Message: fe.Message,
			Field:   fe.Field,
			Value:   fe.Value,
			Hint:    fe.Hint,
			Example: fe.Example,
		})
		return
	}
	// Last resort: surface as an opaque validation_failed entry — better
	// than swallowing it. Callers should prefer typed errors.
	v.errs = append(v.errs, &fieldArgError{
		Code:    "validation_failed",
		Message: err.Error(),
	})
}

// requireString flags `field` as missing when value is empty after
// TrimSpace. Hint defaults to "pass a non-empty string for <field>".
func (v *validator) requireString(field, value string) {
	if strings.TrimSpace(value) != "" {
		return
	}
	v.errs = append(v.errs, newFieldArgError(
		"required_field_missing",
		field, "",
		fmt.Sprintf("%s is required", field),
		fmt.Sprintf("pass a non-empty string for %s", field),
	))
}

// requireStringWithHint is requireString with a custom hint —
// useful when the field has non-obvious format expectations.
func (v *validator) requireStringWithHint(field, value, hint string) {
	if strings.TrimSpace(value) != "" {
		return
	}
	v.errs = append(v.errs, newFieldArgError(
		"required_field_missing",
		field, "",
		fmt.Sprintf("%s is required", field),
		hint,
	))
}

// requireOneOf flags `field` when value is non-empty but not in the
// allowed set. Empty value is silently accepted (combine with
// requireString if the field is mandatory). The allowed set is rendered
// in the hint so the LLM can self-correct.
func (v *validator) requireOneOf(field, value string, allowed ...string) {
	if value == "" {
		return
	}
	for _, a := range allowed {
		if value == a {
			return
		}
	}
	v.errs = append(v.errs, newFieldArgError(
		"invalid_enum_value",
		field, value,
		fmt.Sprintf("%s must be one of: %s", field, strings.Join(allowed, ", ")),
		"pick one of the allowed values (case-sensitive)",
	))
}

// addFieldErr is a sugar wrapper that builds + appends a fieldArgError
// in one call.
func (v *validator) addFieldErr(code, field, value, message, hint string) {
	v.errs = append(v.errs, newFieldArgError(code, field, value, message, hint))
}

// hasErrors reports whether any validation error accumulated.
func (v *validator) hasErrors() bool { return len(v.errs) > 0 }

// envelope returns (CallToolResult, true) when one or more errors
// accumulated, ready to be returned from a handler. Returns (nil, false)
// when the validator is clean.
func (v *validator) envelope() (json.RawMessage, bool) {
	if len(v.errs) == 0 {
		return nil, false
	}
	return encodeFieldErrors(v.errs), true
}

// fieldErrorsEnvelope is the on-the-wire shape extended with the
// `errors` array. Backward compatible: clients that only consume the
// top-level `error` see the first item; clients that want the full
// list iterate `errors`.
type fieldErrorsEnvelope struct {
	Error  errorBody   `json:"error"`
	Errors []errorBody `json:"errors,omitempty"`
}

// encodeFieldErrors renders the envelope as pretty JSON wrapped in an
// MCP CallToolResult with IsError=true. First entry mirrors into the
// legacy `error` body so single-error clients still work.
func encodeFieldErrors(errs []*fieldArgError) json.RawMessage {
	if len(errs) == 0 {
		return nil
	}
	bodies := make([]errorBody, 0, len(errs))
	for _, e := range errs {
		bodies = append(bodies, errorBody{
			Code:    e.Code,
			Message: e.Message,
			Field:   e.Field,
			Value:   e.Value,
			Hint:    e.Hint,
			Example: e.Example,
		})
	}
	env := fieldErrorsEnvelope{
		Error:  bodies[0],
		Errors: bodies,
	}
	pretty, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		// Fall back to a one-line summary on the marshal failure path —
		// keeps the result format predictable for the caller.
		summary := fmt.Sprintf("%s: %s", bodies[0].Code, bodies[0].Message)
		return marshalErrorResult(summary)
	}
	return marshalErrorResult(string(pretty))
}

// marshalFieldErrorsResult is the multi-error sibling of
// marshalFieldErrorResult (singular). Caller passes a slice of errors;
// anything that's a *fieldArgError or *store.FieldError (or wraps one)
// is included. Returns (nil, false) when the slice is empty / only
// non-field errors. Used by handlers that need to mix
// validator-collected errors with a single failing-store-call error.
func marshalFieldErrorsResult(errs []error) (json.RawMessage, bool) {
	if len(errs) == 0 {
		return nil, false
	}
	var collected []*fieldArgError
	for _, err := range errs {
		if err == nil {
			continue
		}
		var ae *fieldArgError
		if errors.As(err, &ae) && ae != nil {
			collected = append(collected, ae)
			continue
		}
		var fe *store.FieldError
		if errors.As(err, &fe) && fe != nil {
			collected = append(collected, &fieldArgError{
				Code:    fe.Code,
				Message: fe.Message,
				Field:   fe.Field,
				Value:   fe.Value,
				Hint:    fe.Hint,
				Example: fe.Example,
			})
		}
	}
	if len(collected) == 0 {
		return nil, false
	}
	return encodeFieldErrors(collected), true
}
