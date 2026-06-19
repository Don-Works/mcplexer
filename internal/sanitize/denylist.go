// Package sanitize defends the agent's context against prompt-injection
// in tool results. Layer 1: regex denylist of known injection markers.
// Layer 2: untrusted-content envelope wrap. Designed for sub-millisecond
// scan budget on Pi Zero 2 W.
package sanitize

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Match is one denylist hit inside a scanned text. Byte offsets are into
// the original input.
type Match struct {
	Pattern string // canonical name of the rule, e.g. "ignore_previous"
	Start   int    // byte offset, inclusive
	End     int    // byte offset, exclusive
	Snippet string // up to ~80 chars around the hit, for audit/UI display
}

// snippetWindow is the number of bytes of context to capture on each
// side of a hit when building Match.Snippet.
const snippetWindow = 40

// compiledRule pairs a canonical rule name with its precompiled regex.
type compiledRule struct {
	name string
	re   *regexp.Regexp
}

// Denylist is the compiled set of injection patterns. Zero value not
// usable — construct via NewDenylist or DefaultDenylist.
type Denylist struct {
	rules []compiledRule
}

// defaultPatterns is the canonical set of injection markers shipped with
// mcplexer. Iteration order is preserved by sorting Names() at runtime;
// Scan order is by offset, not rule order.
var defaultPatterns = map[string]string{
	"ignore_previous":         `(?i)\b(ignore|disregard|forget)\s+(all\s+|the\s+|your\s+)?(previous|prior|above|earlier)\s+(instructions?|prompts?|messages?|rules?)\b`,
	"system_override":         `(?i)\b(system|administrator|admin)\s*:\s*you\s+(must|will|should|are)\b`,
	"chat_template_im":        `<\|im_start\|>`,
	"chat_template_end":       `<\|im_end\|>`,
	"jinja_block":             `\{\{[^}]{1,200}\}\}`,
	"inline_role_switch":      `(?i)\b(assistant|user|system|tool)\s*:\s*<\s*new\s*instructions?\s*>`,
	"data_url_html":           `data:text/html`,
	"prompt_injection_marker": `(?i)\b(prompt\s+injection|jailbreak|dan\s+mode|developer\s+mode|opposite\s+day)\b`,
	"tool_use_smuggling":      `(?i)\b(call|invoke|run)\s+(the\s+)?(tool|function)\s+["'<]?[a-z_]+["'>]?\s+with\s+(args|arguments)`,
	"exfil_secret_blob":       `(?i)\b(send|post|upload|transmit)\s+(my|the|all)\s+(api[\s_-]?key|password|token|secret|credential)s?\b`,
}

// defaultDenylist is built once at init and shared. The compiled regexes
// are safe to use concurrently from multiple goroutines.
var defaultDenylist = mustBuildDefault()

func mustBuildDefault() *Denylist {
	d, err := NewDenylist(defaultPatterns)
	if err != nil {
		panic(fmt.Sprintf("sanitize: default denylist failed to compile: %v", err))
	}
	return d
}

// DefaultDenylist returns a Denylist preloaded with the well-known
// injection markers shipped with mcplexer. Safe to share across goroutines.
func DefaultDenylist() *Denylist {
	return defaultDenylist
}

// NewDenylist returns a Denylist compiled from the given (name, regex)
// pairs. Returns an error if any regex fails to compile.
func NewDenylist(rules map[string]string) (*Denylist, error) {
	if len(rules) == 0 {
		return &Denylist{}, nil
	}
	names := make([]string, 0, len(rules))
	for name := range rules {
		names = append(names, name)
	}
	sort.Strings(names)
	compiled := make([]compiledRule, 0, len(rules))
	for _, name := range names {
		re, err := regexp.Compile(rules[name])
		if err != nil {
			return nil, fmt.Errorf("sanitize: rule %q: %w", name, err)
		}
		compiled = append(compiled, compiledRule{name: name, re: re})
	}
	return &Denylist{rules: compiled}, nil
}

// Scan returns every match in text, in start-offset order. nil input
// returns nil. Cheap to call repeatedly; the regexes are precompiled
// at construction.
func (d *Denylist) Scan(text string) []Match {
	if d == nil || len(d.rules) == 0 || text == "" {
		return nil
	}
	var matches []Match
	for _, rule := range d.rules {
		locs := rule.re.FindAllStringIndex(text, -1)
		for _, loc := range locs {
			matches = append(matches, Match{
				Pattern: rule.name,
				Start:   loc[0],
				End:     loc[1],
				Snippet: buildSnippet(text, loc[0], loc[1]),
			})
		}
	}
	if len(matches) == 0 {
		return nil
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Start != matches[j].Start {
			return matches[i].Start < matches[j].Start
		}
		return matches[i].Pattern < matches[j].Pattern
	})
	return matches
}

// Names returns the canonical rule names currently loaded, sorted.
func (d *Denylist) Names() []string {
	if d == nil || len(d.rules) == 0 {
		return nil
	}
	out := make([]string, len(d.rules))
	for i, r := range d.rules {
		out[i] = r.name
	}
	sort.Strings(out)
	return out
}

// buildSnippet returns a window of context around [start,end) within text,
// with newlines replaced by the literal "\n" so the snippet is single-line
// for line-based audit viewers.
func buildSnippet(text string, start, end int) string {
	lo := max(start-snippetWindow, 0)
	hi := min(end+snippetWindow, len(text))
	return strings.ReplaceAll(text[lo:hi], "\n", `\n`)
}
