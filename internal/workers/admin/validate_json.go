// validate_json.go — JSON-shape validators for the Worker fields whose
// admin payload is a string of JSON. Each guard catches obvious
// misuse at create/update time so the operator (or admin agent) sees
// the failure immediately rather than discovering it on the next run.
package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	maxWorkerSkillRefs           = 8
	maxWorkerParametersJSONBytes = 64 * 1024
)

// validateSkillRefs rejects entries with an empty name or duplicate
// (name, version) pairs. Order matters for the runner so we don't
// canonicalise — duplicates are user error, not a sort-merge case.
func validateSkillRefs(refs []store.SkillRef) error {
	if len(refs) > maxWorkerSkillRefs {
		return fmt.Errorf("skill_refs max %d entries (got %d)", maxWorkerSkillRefs, len(refs))
	}
	seen := make(map[store.SkillRef]struct{}, len(refs))
	for i, ref := range refs {
		if strings.TrimSpace(ref.Name) == "" {
			return fmt.Errorf("skill_refs[%d].name required", i)
		}
		key := store.SkillRef{Name: ref.Name, Version: ref.Version}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("skill_refs[%d] duplicate (name=%q, version=%q)", i, ref.Name, ref.Version)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// validateOutputChannelsJSON parses the JSON-encoded array of output
// channel descriptors and rejects unknown `type` values or malformed
// shape. Empty / "null" / "[]" are accepted (the runner defaults to
// "no output" — only the schedule + mesh-lifecycle messages still
// fire). The runner's own output dispatcher does deeper per-channel
// validation; this is the early-warning gate.
func validateOutputChannelsJSON(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" || s == "null" || s == "[]" {
		return nil
	}
	var channels []map[string]any
	if err := json.Unmarshal([]byte(s), &channels); err != nil {
		return fmt.Errorf("output_channels_json must be a JSON array of objects: %w", err)
	}
	for i, c := range channels {
		t, ok := c["type"].(string)
		if !ok || t == "" {
			return fmt.Errorf("output_channels_json[%d] missing or non-string `type`", i)
		}
		if !isValidOutputChannelType(t) {
			return fmt.Errorf(
				"output_channels_json[%d] unknown type %q (want mesh|file|webhook|slack_webhook|clickup_task|github_issue)",
				i, t,
			)
		}
	}
	return nil
}

// isValidOutputChannelType mirrors internal/workers/runner/output.go's
// dispatchChannel switch. Kept local so admin/ doesn't need to import
// the runner package just for six strings.
func isValidOutputChannelType(t string) bool {
	switch t {
	case "mesh", "file", "webhook", "slack_webhook", "clickup_task", "github_issue":
		return true
	}
	return false
}

// validateParametersJSON enforces "parameters_json is a JSON object,
// not an array, not a scalar". Empty string is OK (the runner / store
// default to "{}"); explicit "null" is normalised to OK as well.
func validateParametersJSON(raw string) error {
	if len(raw) > maxWorkerParametersJSONBytes {
		return fmt.Errorf(
			"parameters_json max %d bytes (got %d)",
			maxWorkerParametersJSONBytes, len(raw),
		)
	}
	s := strings.TrimSpace(raw)
	if s == "" || s == "null" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return fmt.Errorf("parameters_json must be valid JSON: %w", err)
	}
	if _, ok := v.(map[string]any); !ok {
		return errors.New("parameters_json must be a JSON object (not array or scalar)")
	}
	return nil
}

// validateAllowlistJSON rejects ToolAllowlistJSON values that won't
// parse into a string array at runtime. SECURITY: the runner-side
// parser fails CLOSED (deny-everything) when JSON is malformed; we
// catch the same error at create/update time so the operator gets
// immediate feedback rather than a silently-deny-everything worker.
//
// Empty / "null" are accepted (sentinel for "no allowlist
// configured"); the sqlite store defaults newly-created workers
// to "[]" anyway.
func validateAllowlistJSON(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" || s == "null" {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(s), &names); err != nil {
		return fmt.Errorf(
			"tool_allowlist_json must be a JSON array of strings: %w", err,
		)
	}
	for i, n := range names {
		if n == "" {
			return fmt.Errorf(
				"tool_allowlist_json[%d] is empty (every entry must be a non-empty tool name)", i,
			)
		}
	}
	return nil
}
