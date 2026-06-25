package codemode

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// LintWarning describes a single code quality issue found during linting.
type LintWarning struct {
	Line     int    `json:"line"`
	Column   int    `json:"column,omitempty"`
	Message  string `json:"message"`
	Severity string `json:"severity"` // "hint", "warning", "error"
}

// LintResult contains all lint warnings for a code snippet.
type LintResult struct {
	Warnings []LintWarning `json:"warnings"`
	Code     string        `json:"-"` // cleaned code (post-lint modifications)
}

// Compiled patterns for lint checks. These detect common cheap-model mistakes
// before the code reaches the sandbox executor.
var (
	// Matches JSON.parse(...) calls. The sandbox auto-unwraps MCP result
	// envelopes, so JSON.parse on a tool result is redundant and often
	// produces a double-parsed error.
	reJSONParse = regexp.MustCompile(`JSON\.parse\s*\(`)

	// Matches `await` keyword before a tool-namespace call or a variable
	// assignment from a tool call. The sandbox is synchronous — no await
	// needed for any MCP call.
	reAwaitUsage = regexp.MustCompile(`\bawait\s+\w+`)

	// Matches `async function` or arrow functions declared async.
	reAsyncFunction = regexp.MustCompile(`\basync\s+function\b`)

	// Matches async arrow functions: async (...) => ...
	reAsyncArrow = regexp.MustCompile(`\basync\s*\([^)]*\)\s*=>`)

	// Matches `console.log` as the primary output mechanism without `print`.
	// Warn if console.log appears but print does not.
	reConsoleLog = regexp.MustCompile(`console\.log\s*\(`)

	// Matches a possible namespace.member( call pattern. Captures
	// namespace (group 1) and member (group 2). Used by LintWithTools
	// to detect typos in tool calls before runtime — the namespace is
	// verified against the registered tool set, and unknown members
	// trigger a did-you-mean suggestion over that namespace's members.
	reToolCall = regexp.MustCompile(`([a-zA-Z_$][\w$]*)\.([a-zA-Z_$][\w$]*)\s*\(`)

	// Matches simple local declarations so the tool-call typo pass does
	// not treat calls on locally bound values as namespace.member tool calls.
	reLocalBinding = regexp.MustCompile(`\b(?:const|let|var)\s+([a-zA-Z_$][\w$]*)\b`)
)

// sandboxGlobals enumerates the names of objects and functions exposed by
// the sandbox runtime itself. Calls on these (e.g. `JSON.parse(...)`,
// `Math.max(...)`) must never be flagged as unknown tool calls.
var sandboxGlobals = map[string]struct{}{
	"print":      {},
	"console":    {},
	"parallel":   {},
	"compact":    {},
	"sleep":      {},
	"help":       {},
	"JSON":       {},
	"Math":       {},
	"Object":     {},
	"Array":      {},
	"String":     {},
	"Number":     {},
	"Boolean":    {},
	"Date":       {},
	"RegExp":     {},
	"Error":      {},
	"Promise":    {},
	"Map":        {},
	"Set":        {},
	"Symbol":     {},
	"globalThis": {},
	"undefined":  {},
	"NaN":        {},
	"Infinity":   {},
}

// Lint runs pre-execution checks on user-provided JavaScript code and
// returns warnings for common mistakes. It also returns a cleaned version
// of the code (currently identical to the input, but may be modified by
// future lint fixes).
func Lint(code string) LintResult {
	return LintWithTools(code, nil)
}

// LintWithTools runs the standard lint checks AND inspects each
// `namespace.member(` call in the code against the registered tool set.
// When the namespace exists but the member is unknown, it emits an ERROR
// with a did-you-mean over that namespace's members. When the namespace
// itself is a near-miss of a real namespace, it emits a WARNING with a
// did-you-mean over namespace names. Sandbox builtins/globals (print,
// JSON, Math, etc.) and property chains (e.g. `result.task.id` where the
// preceding character is `.`) are skipped to avoid false positives.
func LintWithTools(code string, toolNames []string) LintResult {
	var warnings []LintWarning

	lines := strings.Split(code, "\n")

	// Check for redundant JSON.parse on tool results.
	for _, loc := range findAllLocations(reJSONParse, code) {
		if !insideStringLiteral(code, loc) && !looksLikeUntrustedContentParser(code) {
			line := lineNumber(lines, loc)
			warnings = append(warnings, LintWarning{
				Line:     line,
				Message:  "JSON.parse() is unnecessary on tool results — the sandbox auto-unwraps MCP envelopes. Read result.id directly instead.",
				Severity: "warning",
			})
		}
	}

	// Check for await usage (synchronous sandbox).
	for _, loc := range findAllLocations(reAwaitUsage, code) {
		if !insideStringLiteral(code, loc) {
			line := lineNumber(lines, loc)
			warnings = append(warnings, LintWarning{
				Line:     line,
				Message:  "await is harmless but unnecessary — sandbox tool calls are synchronous. Write const x = tool(...), no await.",
				Severity: "warning",
			})
		}
	}

	// Check for async functions (not supported by the sandbox).
	for _, loc := range findAllLocations(reAsyncFunction, code) {
		if !insideStringLiteral(code, loc) {
			line := lineNumber(lines, loc)
			warnings = append(warnings, LintWarning{
				Line:     line,
				Message:  "async function declarations are not supported in the sandbox. Tool calls are synchronous — remove the async keyword.",
				Severity: "error",
			})
		}
	}
	for _, loc := range findAllLocations(reAsyncArrow, code) {
		if !insideStringLiteral(code, loc) {
			line := lineNumber(lines, loc)
			warnings = append(warnings, LintWarning{
				Line:     line,
				Message:  "async arrow functions are not supported in the sandbox. Tool calls are synchronous — remove the async keyword.",
				Severity: "error",
			})
		}
	}

	// Check for console.log without print.
	hasConsoleLog := reConsoleLog.MatchString(code)
	hasPrint := strings.Contains(code, "print(")
	if hasConsoleLog && !hasPrint {
		for _, loc := range findAllLocations(reConsoleLog, code) {
			if !insideStringLiteral(code, loc) {
				line := lineNumber(lines, loc)
				warnings = append(warnings, LintWarning{
					Line:     line,
					Message:  "Use print() instead of console.log() to return output to the caller. console.log() output is captured, but print() is the idiomatic sandbox function.",
					Severity: "hint",
				})
				break
			}
		}
	}

	// Tool-call typo detection. Inspect every `ns.member(` site outside
	// strings/comments and (a) flag an ERROR when ns is a registered
	// namespace but the member is unknown, (b) flag a WARNING when ns
	// is a near-miss of a real namespace. Property chains (`a.b.c()`),
	// sandbox builtins (`JSON.parse`, `Math.max`, `console.log`), and
	// matches inside literals are skipped.
	if len(toolNames) > 0 {
		warnings = append(warnings, lintToolCallTypos(code, lines, toolNames)...)
	}

	return LintResult{
		Warnings: warnings,
		Code:     code,
	}
}

func looksLikeUntrustedContentParser(code string) bool {
	return strings.Contains(code, "untrusted-content")
}

// lintToolCallTypos walks every `ns.member(` site in masked code and
// classifies it against the registered tool set. Returns the warnings
// the caller should append.
func lintToolCallTypos(code string, lines []string, toolNames []string) []LintWarning {
	namespaces, members := indexToolNames(toolNames)
	if len(namespaces) == 0 {
		return nil
	}

	masked := maskLiteralsForLint(code)
	matches := reToolCall.FindAllStringSubmatchIndex(masked, -1)
	if len(matches) == 0 {
		return nil
	}

	nsList := sortedKeys(namespaces)
	localBindings := collectLocalBindings(masked)
	var out []LintWarning
	seen := make(map[string]struct{})

	for _, m := range matches {
		nsStart, nsEnd := m[2], m[3]
		memberStart, memberEnd := m[4], m[5]
		ns := masked[nsStart:nsEnd]
		member := masked[memberStart:memberEnd]

		if _, ok := sandboxGlobals[ns]; ok {
			continue
		}
		if _, ok := localBindings[ns]; ok {
			continue
		}
		// Skip property chains: `result.task.id()` — the preceding
		// char before ns is `.` so ns is not a top-level binding.
		if nsStart > 0 && masked[nsStart-1] == '.' {
			continue
		}

		key := fmt.Sprintf("%d:%s.%s", nsStart, ns, member)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		line := lineNumber(lines, nsStart)

		if knownMembers, ok := namespaces[ns]; ok {
			if _, memberOK := members[ns+"__"+member]; memberOK {
				continue
			}
			msg := fmt.Sprintf("%s.%s is not a registered tool.", ns, member)
			if sug := DidYouMean(member, knownMembers, 3); len(sug) > 0 {
				msg += " Did you mean: " + joinDotted(ns, sug) + "?"
			}
			out = append(out, LintWarning{
				Line:     line,
				Message:  msg,
				Severity: "error",
			})
			continue
		}

		if sug := DidYouMean(ns, nsList, 1); len(sug) > 0 {
			out = append(out, LintWarning{
				Line:     line,
				Message:  fmt.Sprintf("%s is not a known namespace. Did you mean: %s? Call help() to list namespaces.", ns, sug[0]),
				Severity: "warning",
			})
		}
	}
	return out
}

func collectLocalBindings(masked string) map[string]struct{} {
	matches := reLocalBinding.FindAllStringSubmatch(masked, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		if len(m) > 1 {
			out[m[1]] = struct{}{}
		}
	}
	return out
}

// indexToolNames splits "ns__member" entries into a namespace→members map
// and a set of full tool names for quick lookup.
func indexToolNames(toolNames []string) (map[string][]string, map[string]struct{}) {
	namespaces := make(map[string][]string)
	members := make(map[string]struct{}, len(toolNames))
	for _, name := range toolNames {
		members[name] = struct{}{}
		ns, member, ok := strings.Cut(name, "__")
		if !ok {
			continue
		}
		namespaces[ns] = append(namespaces[ns], member)
	}
	return namespaces, members
}

// sortedKeys returns the map keys in stable order so did-you-mean ranking
// is deterministic across runs.
func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// joinDotted formats a list of member names as `ns.member1, ns.member2`.
func joinDotted(ns string, members []string) string {
	parts := make([]string, len(members))
	for i, m := range members {
		parts[i] = ns + "." + m
	}
	return strings.Join(parts, ", ")
}

// maskLiteralsForLint replaces the contents of string literals (single,
// double, template) and comments with spaces, preserving line breaks and
// total byte length so byte-offset based regex lookups stay aligned with
// the original source. Distinct from strip.go's maskLiterals (which uses
// sentinels and shifts offsets); the lint pass needs offset preservation
// so lineNumber lookups stay accurate after masking. Calls like
// `print("a.b()")` inside a print() argument never trip the tool-call
// detector once their contents are spaced out.
func maskLiteralsForLint(code string) string {
	out := []byte(code)
	inSingle := false
	inDouble := false
	inTemplate := false
	inLineComment := false
	inBlockComment := false

	for i := 0; i < len(out); i++ {
		c := out[i]

		if inLineComment {
			if c == '\n' {
				inLineComment = false
				continue
			}
			out[i] = ' '
			continue
		}
		if inBlockComment {
			if c == '*' && i+1 < len(out) && out[i+1] == '/' {
				out[i] = ' '
				out[i+1] = ' '
				i++
				inBlockComment = false
				continue
			}
			if c != '\n' {
				out[i] = ' '
			}
			continue
		}

		if inSingle || inDouble || inTemplate {
			if c == '\\' && i+1 < len(out) {
				if out[i+1] != '\n' {
					out[i] = ' '
					out[i+1] = ' '
				} else {
					out[i] = ' '
				}
				i++
				continue
			}
			if (inSingle && c == '\'') ||
				(inDouble && c == '"') ||
				(inTemplate && c == '`') {
				inSingle = false
				inDouble = false
				inTemplate = false
				continue
			}
			if c != '\n' {
				out[i] = ' '
			}
			continue
		}

		switch c {
		case '/':
			if i+1 < len(out) {
				if out[i+1] == '/' {
					inLineComment = true
					out[i] = ' '
					out[i+1] = ' '
					i++
					continue
				}
				if out[i+1] == '*' {
					inBlockComment = true
					out[i] = ' '
					out[i+1] = ' '
					i++
					continue
				}
			}
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '`':
			inTemplate = true
		}
	}
	return string(out)
}

// DidYouMean returns suggested corrections for a mistyped name, comparing
// it against a list of known names using Levenshtein distance. Returns up
// to maxSuggestions closest matches within distance threshold.
func DidYouMean(name string, knownNames []string, maxSuggestions int) []string {
	if len(knownNames) == 0 || name == "" {
		return nil
	}

	type candidate struct {
		name string
		dist int
	}

	candidates := make([]candidate, 0, len(knownNames))
	maxDist := 2
	if len(name) > 8 {
		maxDist = 3
	}

	for _, known := range knownNames {
		d := levenshtein(strings.ToLower(name), strings.ToLower(known))
		if d <= maxDist && d > 0 {
			candidates = append(candidates, candidate{name: known, dist: d})
		}
		// Also check normalized form (stripping underscores/hyphens).
		normName := normalizeForMatch(name)
		normKnown := normalizeForMatch(known)
		if normName != strings.ToLower(name) || normKnown != strings.ToLower(known) {
			d2 := levenshtein(normName, normKnown)
			if d2 <= maxDist && d2 > 0 && (len(candidates) == 0 || d2 < candidates[len(candidates)-1].dist) {
				candidates = append(candidates, candidate{name: known, dist: d2})
			}
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].dist != candidates[j].dist {
			return candidates[i].dist < candidates[j].dist
		}
		return candidates[i].name < candidates[j].name
	})

	// Deduplicate by name.
	seen := make(map[string]bool)
	var suggestions []string
	for _, c := range candidates {
		if seen[c.name] {
			continue
		}
		seen[c.name] = true
		suggestions = append(suggestions, c.name)
		if len(suggestions) >= maxSuggestions {
			break
		}
	}
	return suggestions
}

// normalizeForMatch lowercases and strips underscores/dashes for fuzzy matching.
func normalizeForMatch(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, ch := range strings.ToLower(s) {
		if ch != '_' && ch != '-' {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

// levenshtein computes the edit distance between two strings.
func levenshtein(a, b string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		curr := make([]int, len(b)+1)
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(curr[j-1]+1, min(prev[j]+1, prev[j-1]+cost))
		}
		prev = curr
	}

	return prev[len(b)]
}

// findAllLocations returns all match start positions for a regex in the
// input string.
func findAllLocations(re *regexp.Regexp, s string) []int {
	matches := re.FindAllStringIndex(s, -1)
	locs := make([]int, len(matches))
	for i, m := range matches {
		locs[i] = m[0]
	}
	return locs
}

// insideStringLiteral checks whether a position in code is inside a quoted
// string or template literal. Simple scan — sufficient for lint false-positive
// prevention.
func insideStringLiteral(code string, pos int) bool {
	inSingle := false
	inDouble := false
	inTemplate := false
	inLineComment := false
	inBlockComment := false
	for i := 0; i < len(code) && i < pos; i++ {
		c := code[i]

		if inLineComment {
			if c == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			if i+1 < len(code) && c == '*' && code[i+1] == '/' {
				inBlockComment = false
				i++
			}
			continue
		}

		switch c {
		case '/':
			if i+1 < len(code) {
				if code[i+1] == '/' {
					inLineComment = true
					i++
					continue
				}
				if code[i+1] == '*' {
					inBlockComment = true
					i++
					continue
				}
			}
		case '\'':
			if !inDouble && !inTemplate {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle && !inTemplate {
				inDouble = !inDouble
			}
		case '`':
			if !inSingle && !inDouble {
				inTemplate = !inTemplate
			}
		case '\\':
			if inSingle || inDouble || inTemplate {
				i++ // skip escaped character
			}
		}
	}
	return inSingle || inDouble || inTemplate
}

// lineNumber returns the 1-indexed line number for a byte position in code.
func lineNumber(lines []string, pos int) int {
	offset := 0
	for i, line := range lines {
		offset += len(line) + 1 // +1 for newline
		if offset > pos {
			return i + 1
		}
	}
	return len(lines)
}

// FormatLintWarnings returns a human-readable string of all warnings,
// suitable for inclusion in the execution error output.
func FormatLintWarnings(warnings []LintWarning) string {
	if len(warnings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n--- Lint warnings ---\n")
	for _, w := range warnings {
		prefix := "[" + w.Severity + "]"
		b.WriteString(fmt.Sprintf("%s line %d: %s\n", prefix, w.Line, w.Message))
	}
	b.WriteString("---\n")
	return b.String()
}
