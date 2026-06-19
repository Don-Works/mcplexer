// field_errors.go — aggregated field-validation envelope for the
// mcplexer__* admin tools served by the control backend.
//
// Mirrors the gateway-side validator in internal/gateway/field_errors.go
// — keeping the wire shape identical so an LLM that learned the
// envelope from a memory__save error can parse a create_workspace
// error the same way:
//
//	{
//	  "error":  { code, field, value, message, hint, example },  // first
//	  "errors": [ {code, field, value, message, hint, example}, ... ]
//	}
//
// The MCP CallToolResult wraps this JSON in a text content block with
// IsError=true.
//
// The control package has its own copy (instead of importing the
// gateway one) because:
//  1. internal/gateway already depends on us via memory imports — going
//     the other direction would risk a cycle.
//  2. The validator surface here is intentionally tiny (requireString +
//     envelope) — duplicating ~30 lines is cheaper than wiring a shared
//     dependency.
package control

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/gateway"
)

// fieldError is the on-the-wire body for a single field validation
// failure. Same field set as gateway.errorBody / store.FieldError.
type fieldError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
	Value   string `json:"value,omitempty"`
	Hint    string `json:"hint,omitempty"`
	Example string `json:"example,omitempty"`
}

// fieldErrorsEnvelope is the JSON envelope returned to the agent.
type fieldErrorsEnvelope struct {
	Error  fieldError   `json:"error"`
	Errors []fieldError `json:"errors,omitempty"`
}

// validator accumulates field-validation errors so a handler reports
// every problem in one response. Zero-value is ready to use.
type validator struct {
	errs []fieldError
}

// requireString flags `field` as missing when value is empty after
// TrimSpace. hint must be specific (e.g. "lowercase alphanumeric, kebab
// or snake case"). Empty hint falls back to a generic instruction.
func (v *validator) requireString(field, value, hint string) {
	if strings.TrimSpace(value) != "" {
		return
	}
	if hint == "" {
		hint = fmt.Sprintf("pass a non-empty string for %s", field)
	}
	v.errs = append(v.errs, fieldError{
		Code:    "required_field_missing",
		Field:   field,
		Message: fmt.Sprintf("%s is required", field),
		Hint:    hint,
	})
}

// requireOneOf checks value against an allowed set. Empty value is
// silently accepted (compose with requireString to enforce presence).
func (v *validator) requireOneOf(field, value string, allowed ...string) {
	if value == "" {
		return
	}
	for _, a := range allowed {
		if value == a {
			return
		}
	}
	v.errs = append(v.errs, fieldError{
		Code:    "invalid_enum_value",
		Field:   field,
		Value:   value,
		Message: fmt.Sprintf("%s must be one of: %s", field, strings.Join(allowed, ", ")),
		Hint:    "pick one of the allowed values (case-sensitive)",
	})
}

// envelope returns (CallToolResult, true) when one or more errors
// accumulated. Caller short-circuits with `return env, nil`.
func (v *validator) envelope() (json.RawMessage, bool) {
	if len(v.errs) == 0 {
		return nil, false
	}
	env := fieldErrorsEnvelope{
		Error:  v.errs[0],
		Errors: v.errs,
	}
	pretty, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		// Fall back to legacy one-line errorResult so the dispatcher
		// still sees IsError=true and the agent gets something useful.
		return errorResult(fmt.Sprintf("%s: %s",
			v.errs[0].Code, v.errs[0].Message)), true
	}
	result := gateway.CallToolResult{
		Content: []gateway.ToolContent{{Type: "text", Text: string(pretty)}},
		IsError: true,
	}
	data, mErr := json.Marshal(result)
	if mErr != nil {
		return errorResult(fmt.Sprintf("%s: %s",
			v.errs[0].Code, v.errs[0].Message)), true
	}
	return data, true
}
