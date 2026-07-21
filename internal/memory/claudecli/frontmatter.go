// frontmatter.go — YAML frontmatter parser for Claude Code memory
// files. Tolerant: a file with no frontmatter, or a malformed YAML
// block, degrades to (empty parsed, whole-file body) rather than
// erroring out — the importer prioritises ingesting content.
package claudecli

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// parsedFrontmatter is the subset of the YAML frontmatter we read.
type parsedFrontmatter struct {
	Name        string             `yaml:"name"`
	Description string             `yaml:"description"`
	Metadata    parsedFrontmatterM `yaml:"metadata"`
}

type parsedFrontmatterM struct {
	NodeType        string `yaml:"node_type"`
	Type            string `yaml:"type"`
	OriginSessionID string `yaml:"originSessionId"`
}

// parseFrontmatter splits a markdown file with YAML frontmatter into
// (parsed, body). A file without frontmatter is treated as
// (empty parsed, whole-file body). A file that opens with `---` but
// is malformed degrades the same way — we never block the import on a
// YAML quirk.
func parseFrontmatter(raw []byte) (parsedFrontmatter, string) {
	s := string(raw)
	if !strings.HasPrefix(s, "---") {
		return parsedFrontmatter{}, s
	}
	rest := strings.TrimPrefix(s, "---")
	rest = strings.TrimLeft(rest, "\r\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return parsedFrontmatter{}, s
	}
	yamlBlock := rest[:end]
	body := rest[end+len("\n---"):]
	body = strings.TrimLeft(body, "\r\n")
	var fm parsedFrontmatter
	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
		return parsedFrontmatter{}, s
	}
	return fm, body
}
