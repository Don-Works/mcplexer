// helpers.go — shared utilities for the harness import package.
package harnessimport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"gopkg.in/yaml.v3"
)

// deterministicID hashes (path + extra + content) to produce a stable
// ID. Re-importing the same file produces the same store ID, so the
// WriteMemory PK uniqueness is the idempotency gate.
func deterministicID(path, extra string, content []byte) string {
	h := sha256.New()
	h.Write([]byte(path))
	h.Write([]byte{0})
	if extra != "" {
		h.Write([]byte(extra))
		h.Write([]byte{0})
	}
	h.Write(content)
	sum := h.Sum(nil)
	return "himp-" + hex.EncodeToString(sum[:16])
}

func mustJSONArray(tags []string) json.RawMessage {
	if len(tags) == 0 {
		return json.RawMessage("[]")
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return json.RawMessage("[]")
	}
	return b
}

func mustJSONObject(m map[string]any) json.RawMessage {
	if len(m) == 0 {
		return json.RawMessage("{}")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

// claudeFrontmatter is the subset of YAML frontmatter we read from
// Claude Code memory files.
type claudeFrontmatter struct {
	Name     string                `yaml:"name"`
	Metadata claudeFrontmatterMeta `yaml:"metadata"`
}

type claudeFrontmatterMeta struct {
	Type            string `yaml:"type"`
	OriginSessionID string `yaml:"originSessionId"`
}

// parseClaudeFrontmatter splits a markdown file with YAML frontmatter
// into (parsed, body). Tolerant: malformed YAML degrades to (empty,
// whole-file).
func parseClaudeFrontmatter(raw []byte) (claudeFrontmatter, string) {
	s := string(raw)
	if !strings.HasPrefix(s, "---") {
		return claudeFrontmatter{}, s
	}
	rest := strings.TrimPrefix(s, "---")
	rest = strings.TrimLeft(rest, "\r\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return claudeFrontmatter{}, s
	}
	yamlBlock := rest[:end]
	body := rest[end+len("\n---"):]
	body = strings.TrimLeft(body, "\r\n")
	var fm claudeFrontmatter
	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
		return claudeFrontmatter{}, s
	}
	return fm, body
}
