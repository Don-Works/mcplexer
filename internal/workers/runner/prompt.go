package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// placeholderRe matches {identifier} tokens with simple identifier rules
// (letters, digits, underscore, dash). The match is intentionally narrow
// so literal braces in instructions ("emit {valid_json}") don't get
// substituted as side effects — a placeholder is meaningful only when
// it's a single word in braces.
var placeholderRe = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_\-]*)\}`)

// renderPrompt substitutes {placeholder} tokens in the template using
// parametersJSON (a JSON object: { "key": "value", ... }). Values are
// coerced to strings via fmt.Sprintf("%v", ...). Unknown placeholders
// are left as-is so the prompt author can use literal braces in
// instructions. An empty parametersJSON ("" or "{}") is allowed —
// every placeholder in the template is then left untouched.
func renderPrompt(template, parametersJSON string) (string, error) {
	params, err := parseParameters(parametersJSON)
	if err != nil {
		return "", fmt.Errorf("parse parameters: %w", err)
	}
	rendered := placeholderRe.ReplaceAllStringFunc(template, func(match string) string {
		key := match[1 : len(match)-1]
		v, ok := params[key]
		if !ok {
			return match
		}
		return fmt.Sprintf("%v", v)
	})
	return rendered, nil
}

// parseParameters decodes parametersJSON as a JSON object. Empty input
// is treated as an empty map (not an error).
func parseParameters(parametersJSON string) (map[string]any, error) {
	if parametersJSON == "" {
		return map[string]any{}, nil
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(parametersJSON), &params); err != nil {
		return nil, err
	}
	if params == nil {
		params = map[string]any{}
	}
	return params, nil
}

// ErrSkillReaderRequired is returned when a Worker has skill refs set
// but the Runner wasn't constructed with a SkillReader.
var ErrSkillReaderRequired = errors.New("worker has skill refs but runner has no skill reader")

// skillBodySeparator is the markdown horizontal rule the runner uses to
// join multiple skill bodies. Picked so the model sees each skill as a
// distinct section rather than one runs-into-the-next document.
const skillBodySeparator = "\n\n---\n\n"

const (
	maxSkillRefsPerRun   = 8
	maxSkillBodiesBytes  = 96 * 1024
	maxSystemPromptBytes = 128 * 1024
	maxUserPromptBytes   = 128 * 1024
)

// composeSystemPrompt joins the gateway-owned preamble (when present)
// with the worker's skill bodies. Either piece may be empty — when both
// are empty the result is the empty string, matching the pre-preamble
// behaviour.
func composeSystemPrompt(preamble, skillBodies string) string {
	switch {
	case preamble == "" && skillBodies == "":
		return ""
	case preamble == "":
		return skillBodies
	case skillBodies == "":
		return preamble
	default:
		return preamble + skillBodySeparator + skillBodies
	}
}

// loadSkillBodies fetches every skill body in order and joins them with
// the markdown separator. Returns the empty string (no error) for an
// empty refs slice. When skills is nil but refs is non-empty,
// ErrSkillReaderRequired surfaces — a clean error beats silently
// dropping the operator's intent.
// workspaceID is forwarded to the reader so workspace-scoped skills are
// resolved before falling back to global skills.
func loadSkillBodies(ctx context.Context, skills SkillReader, workspaceID string, refs []store.SkillRef) (string, error) {
	if len(refs) == 0 {
		return "", nil
	}
	if len(refs) > maxSkillRefsPerRun {
		return "", fmt.Errorf("skill_refs max %d entries (got %d)", maxSkillRefsPerRun, len(refs))
	}
	if skills == nil {
		return "", ErrSkillReaderRequired
	}
	bodies := make([]string, 0, len(refs))
	total := 0
	for _, ref := range refs {
		if ref.Name == "" {
			continue
		}
		body, err := skills.GetSkillBody(ctx, workspaceID, ref.Name, ref.Version)
		if err != nil {
			return "", fmt.Errorf("load skill %q@%q: %w", ref.Name, ref.Version, err)
		}
		nextTotal := total + len(body)
		if len(bodies) > 0 {
			nextTotal += len(skillBodySeparator)
		}
		if nextTotal > maxSkillBodiesBytes {
			return "", fmt.Errorf(
				"skill bodies exceed %d bytes after %q@%q",
				maxSkillBodiesBytes, ref.Name, ref.Version,
			)
		}
		total = nextTotal
		bodies = append(bodies, body)
	}
	return strings.Join(bodies, skillBodySeparator), nil
}

func validatePromptBudgets(systemPrompt, userPrompt string) error {
	if len(systemPrompt) > maxSystemPromptBytes {
		return fmt.Errorf(
			"system prompt exceeds %d bytes (got %d)",
			maxSystemPromptBytes, len(systemPrompt),
		)
	}
	if len(userPrompt) > maxUserPromptBytes {
		return fmt.Errorf(
			"user prompt exceeds %d bytes (got %d)",
			maxUserPromptBytes, len(userPrompt),
		)
	}
	return nil
}
