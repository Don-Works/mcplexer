package skillregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/don-works/mcplexer/internal/skills"
	"gopkg.in/yaml.v3"
)

// MaxBodyBytes caps the full SKILL.md body size at publish time.
// Skills are loaded into the agent's context window, so unbounded bodies
// would let one bad actor exhaust attention budget. 64 KB is well above
// the agentskills.io best-practice "<5000 token / <500 line" guideline
// without being absurd.
const MaxBodyBytes = 64 * 1024

// MaxDescriptionLen is the agentskills.io spec cap.
const MaxDescriptionLen = 1024

// nameRE matches the agentskills.io name field rules: lowercase
// alphanumeric + hyphen. Leading/trailing hyphen and double-hyphen
// rejection happens in code (Go's RE2 has no negative lookahead).
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$`)

// reservedNames cannot be used as a skill name. Mirrors the spec.
var reservedNames = map[string]bool{
	"anthropic": true,
	"claude":    true,
}

// Parsed is the structured form of a SKILL.md document.
type Parsed struct {
	Name         string
	Description  string
	Category     string
	Body         string
	MetadataJSON json.RawMessage
	TagsJSON     json.RawMessage
	ContentHash  string

	// Extra holds the W4 structured frontmatter fields (requires,
	// produces, consumes, phases, refinement). Zero value when the
	// SKILL.md declares none of them. Validated by ValidateExtra
	// during Parse — an invalid extras block fails the publish.
	Extra skills.ManifestExtra
}

// Parse extracts and validates the YAML frontmatter from body. Returns a
// detailed error when validation fails — callers surface this directly
// to agents so they can fix and retry.
//
// expectedName, when non-empty, is matched against the frontmatter name;
// a mismatch returns an error. This keeps the publish API and the file
// content honest.
func Parse(body, expectedName string) (*Parsed, error) {
	if body == "" {
		return nil, errors.New("body is empty")
	}
	if len(body) > MaxBodyBytes {
		return nil, fmt.Errorf("body exceeds %d bytes (got %d)", MaxBodyBytes, len(body))
	}

	frontmatter, _, err := splitFrontmatter(body)
	if err != nil {
		return nil, err
	}

	var meta map[string]any
	if err := yaml.Unmarshal([]byte(frontmatter), &meta); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	if meta == nil {
		return nil, errors.New("frontmatter is empty")
	}

	name, err := requiredFrontmatterString(meta, "name")
	if err != nil {
		return nil, err
	}
	desc, err := requiredFrontmatterString(meta, "description")
	if err != nil {
		return nil, err
	}
	if !nameRE.MatchString(name) || strings.Contains(name, "--") {
		return nil, fmt.Errorf("invalid name %q: must match [a-z0-9-], no leading/trailing/double hyphens, ≤64 chars", name)
	}
	if len(name) > 64 {
		return nil, fmt.Errorf("name %q exceeds 64 chars", name)
	}
	if reservedNames[name] {
		return nil, fmt.Errorf("name %q is reserved", name)
	}
	if len(desc) > MaxDescriptionLen {
		return nil, fmt.Errorf("description exceeds %d chars (got %d)", MaxDescriptionLen, len(desc))
	}
	if expectedName != "" && expectedName != name {
		return nil, fmt.Errorf("frontmatter name %q does not match argument %q", name, expectedName)
	}

	tags := extractTags(meta)
	category := extractCategory(meta)
	extra, err := extractManifestExtra(meta)
	if err != nil {
		return nil, err
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return nil, fmt.Errorf("marshal tags: %w", err)
	}

	sum := sha256.Sum256([]byte(body))

	return &Parsed{
		Name:         name,
		Description:  desc,
		Category:     category,
		Body:         body,
		MetadataJSON: metaJSON,
		TagsJSON:     tagsJSON,
		ContentHash:  hex.EncodeToString(sum[:]),
		Extra:        extra,
	}, nil
}

func requiredFrontmatterString(meta map[string]any, key string) (string, error) {
	v, ok := meta[key]
	if !ok || v == nil {
		return "", fmt.Errorf("frontmatter missing required field: %s", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s: invalid type %s, expected string", key, yamlTypeName(v))
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("frontmatter missing required field: %s", key)
	}
	return s, nil
}

func yamlTypeName(v any) string {
	switch v.(type) {
	case []any:
		return "sequence"
	case map[string]any:
		return "mapping"
	case bool:
		return "boolean"
	case int, int64, float64:
		return "number"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// categoryRE matches the same shape as a skill name: lowercase alnum +
// hyphen, no leading/trailing/double hyphens. Categories share that
// constraint so they're safe in URL slugs and CLI args.
var categoryRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$`)

// extractCategory pulls a single `category:` string from frontmatter,
// or from `metadata.category`. Anything that isn't a non-empty string
// matching categoryRE is dropped silently (so a malformed value never
// blocks publish). Empty string means "uncategorized" — the UI groups
// those under a default bucket.
func extractCategory(meta map[string]any) string {
	cand := meta["category"]
	if cand == nil {
		if m, ok := meta["metadata"].(map[string]any); ok {
			cand = m["category"]
		}
	}
	s, ok := cand.(string)
	if !ok {
		return ""
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || strings.Contains(s, "--") || !categoryRE.MatchString(s) || len(s) > 32 {
		return ""
	}
	return s
}

// splitFrontmatter returns the YAML between the leading `---` fences and
// the markdown body that follows. Both UNIX and Windows line endings work.
func splitFrontmatter(body string) (string, string, error) {
	s := strings.TrimLeft(body, " \t\r\n")
	if !strings.HasPrefix(s, "---") {
		return "", "", errors.New("missing leading --- frontmatter fence")
	}
	rest := strings.TrimPrefix(s, "---")
	// Skip exactly one newline after the opening fence.
	rest = strings.TrimPrefix(rest, "\r")
	rest = strings.TrimPrefix(rest, "\n")

	closing := strings.Index(rest, "\n---")
	if closing < 0 {
		return "", "", errors.New("missing closing --- frontmatter fence")
	}
	frontmatter := rest[:closing]
	tail := rest[closing+len("\n---"):]
	tail = strings.TrimPrefix(tail, "\r")
	tail = strings.TrimPrefix(tail, "\n")
	return frontmatter, tail, nil
}

// extractTags pulls a tags list out of either a top-level `tags:` field
// or `metadata.tags`. Anything that isn't a list of strings is dropped.
func extractTags(meta map[string]any) []string {
	cand := meta["tags"]
	if cand == nil {
		if m, ok := meta["metadata"].(map[string]any); ok {
			cand = m["tags"]
		}
	}
	raw, ok := cand.([]any)
	if !ok {
		return []string{}
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}
