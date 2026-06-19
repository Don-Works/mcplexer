package brain

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/adrg/frontmatter"
	"gopkg.in/yaml.v3"
)

// yamlFormat decodes the `--- ... ---` frontmatter block with yaml.v3 so
// the parse path uses the SAME YAML library as the serialize path. The
// adrg/frontmatter default YAML format uses yaml.v2; overriding it keeps
// the round-trip self-consistent (tag handling, time formats, etc.).
var yamlFormat = frontmatter.NewFormat(frontmatterDelim, frontmatterDelim, yaml.Unmarshal)

// ParseTask splits a task Markdown document into its frontmatter struct
// and prose body. A missing or malformed frontmatter block is an error
// (never a panic).
func ParseTask(data []byte) (TaskFrontmatter, string, error) {
	var fm TaskFrontmatter
	body, err := parseDoc(data, &fm)
	if err != nil {
		return TaskFrontmatter{}, "", fmt.Errorf("brain: parse task: %w", err)
	}
	return fm, body, nil
}

// ParseMemory splits a memory Markdown document into its frontmatter
// struct and prose body.
func ParseMemory(data []byte) (MemoryFrontmatter, string, error) {
	var fm MemoryFrontmatter
	body, err := parseDoc(data, &fm)
	if err != nil {
		return MemoryFrontmatter{}, "", fmt.Errorf("brain: parse memory: %w", err)
	}
	return fm, body, nil
}

// ParsePerson splits a CRM person Markdown document into its frontmatter
// struct and prose body (the person's notes).
func ParsePerson(data []byte) (PersonFrontmatter, string, error) {
	var fm PersonFrontmatter
	body, err := parseDoc(data, &fm)
	if err != nil {
		return PersonFrontmatter{}, "", fmt.Errorf("brain: parse person: %w", err)
	}
	return fm, body, nil
}

// ParseWorkspace splits a workspace.md document into its frontmatter
// struct and (typically empty) body.
func ParseWorkspace(data []byte) (WorkspaceFrontmatter, string, error) {
	var fm WorkspaceFrontmatter
	body, err := parseDoc(data, &fm)
	if err != nil {
		return WorkspaceFrontmatter{}, "", fmt.Errorf("brain: parse workspace: %w", err)
	}
	return fm, body, nil
}

// parseDoc is the shared frontmatter splitter. It decodes the YAML block
// into v and returns the remaining body (leading blank lines trimmed).
// frontmatter.MustParse reports ErrNotFound when no frontmatter fence is
// present, which we surface as an error so the indexer records a
// brain_errors row rather than silently indexing a body-only file.
func parseDoc(data []byte, v any) (string, error) {
	rest, err := frontmatter.MustParse(bytes.NewReader(data), v, yamlFormat)
	if err != nil {
		return "", err
	}
	body := strings.TrimLeft(string(rest), "\n")
	return body, nil
}
