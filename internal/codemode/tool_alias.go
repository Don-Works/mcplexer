package codemode

import (
	"fmt"
	"strings"
)

// assignJSMemberNames maps arbitrary MCP member names to deterministic,
// collision-free JavaScript identifiers. Original valid identifiers are
// reserved first; punctuation-heavy names then receive underscore aliases.
func assignJSMemberNames(names []string) map[string]string {
	out := make(map[string]string, len(names))
	used := make(map[string]bool, len(names))
	for _, name := range names {
		if isJSIdentifier(name) {
			out[name], used[name] = name, true
		}
	}
	for _, name := range names {
		if out[name] != "" {
			continue
		}
		base, candidate := jsMemberAlias(name), jsMemberAlias(name)
		for suffix := 2; used[candidate]; suffix++ {
			candidate = fmt.Sprintf("%s_%d", base, suffix)
		}
		out[name], used[candidate] = candidate, true
	}
	return out
}

func jsMemberAlias(name string) string {
	if isJSIdentifier(name) {
		return name
	}
	var b strings.Builder
	for i, r := range name {
		valid := r == '_' || r == '$' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if i > 0 {
			valid = valid || r >= '0' && r <= '9'
		}
		if valid {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	alias := strings.Trim(b.String(), "_")
	if alias == "" {
		return "tool"
	}
	if alias[0] >= '0' && alias[0] <= '9' {
		return "_" + alias
	}
	return alias
}

func isJSIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || r == '$' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}
