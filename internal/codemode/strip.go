package codemode

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Compiled patterns for stripping TypeScript type annotations.
// These are intentionally conservative — only matching patterns that are
// unambiguously TypeScript and never valid JavaScript.
var (
	// Matches generated declaration blocks like:
	//   declare namespace github { ... }
	// The outer closing brace is expected at the start of a line, which matches
	// our generated API format and avoids swallowing arbitrary JS blocks.
	reDeclareNamespace = regexp.MustCompile(`(?ms)^\s*declare\s+namespace\s+\w+\s*\{.*?^\s*\}\s*\n?`)

	// Matches `interface Name { ... }` blocks (including multiline).
	// Uses a greedy match to handle nested braces from inline object types
	// like `config: { name: string; count: number }`.
	// Safe: `interface {}` is never valid JS.
	reInterface = regexp.MustCompile(`(?ms)^\s*interface\s+\w+\s*\{.*?^\s*\}\s*\n?`)

	// Matches type annotations ONLY after const/let/var declarations:
	//   const x: string = ...  →  const x = ...
	//   let y: number;         →  let y;
	// This avoids matching object property values like { key: { ... } }.
	reVarTypeAnnotation = regexp.MustCompile(
		`((?:const|let|var)\s+\w+)` + // captured: declaration
			`:\s*` + // colon + whitespace
			`(?:` +
			`[A-Za-z_][\w.]*(?:<[^>]+>)?(?:\[\])?` + // named types, generics, arrays
			`(?:\s*\|\s*[A-Za-z_][\w.]*(?:<[^>]+>)?(?:\[\])?)*` + // union types
			`)`,
	)

	// Matches `as Type` casts — only when followed by a delimiter.
	// e.g. `result as string;` → `result;`
	reAsCast = regexp.MustCompile(`\s+as\s+[A-Za-z_][\w.]*(?:<[^>]+>)?`)

	// Matches `declare` keyword at start of line (from generated declarations).
	// Safe: `declare` is never valid JS.
	reDeclare = regexp.MustCompile(`(?m)^\s*declare\s+`)

	// Matches TypeScript declaration signatures like:
	//   function print(value: any): void;
	// These can appear after stripping `declare`.
	reFunctionDeclaration = regexp.MustCompile(
		`(?m)^\s*function\s+\w+\s*\([^)]*\)\s*(?::\s*[^;{]+)?;\s*\n?`,
	)

	// Matches generated header comments.
	reGenComment = regexp.MustCompile(`(?m)^//\s*Auto-generated.*\n|^//\s*Tool functions.*\n`)
)

// StripTypeScript removes TypeScript-specific syntax from code, producing
// valid JavaScript that can be executed in Goja. This handles the constrained
// subset of TypeScript that LLMs generate for our code API:
//   - Generated `declare namespace ... {}` blocks from search_tools
//   - Interface declarations
//   - Type annotations on variable declarations (const x: Type = ...)
//   - Type casts (as Type)
//   - Declaration-only `function foo(...): Type;` signatures
//   - declare keywords
//
// Intentionally does NOT strip `: value` in object literals like { key: value }.
//
// String, template, and comment regions are masked out before the
// type-stripping regexes run, so prose like `print("run as fast as you can")`
// or template content like “ `marked as done` “ is never corrupted. The
// masked spans are restored verbatim afterwards.
func StripTypeScript(code string) string {
	code, literals := maskLiterals(code)

	// Remove generated namespace declaration blocks before anything else.
	code = reDeclareNamespace.ReplaceAllString(code, "")

	// Remove interface blocks first.
	code = reInterface.ReplaceAllString(code, "")

	// Remove type annotations on variable declarations only.
	code = reVarTypeAnnotation.ReplaceAllString(code, "$1")

	// Remove `as Type` casts.
	code = reAsCast.ReplaceAllString(code, "")

	// Remove `declare` keyword (keep the rest of the line).
	code = reDeclare.ReplaceAllString(code, "")

	// Remove declaration-only function signatures.
	code = reFunctionDeclaration.ReplaceAllString(code, "")

	// Remove generated header comments.
	code = reGenComment.ReplaceAllString(code, "")

	code = restoreLiterals(code, literals)

	// Clean up blank lines.
	code = cleanBlankLines(code)

	return strings.TrimSpace(code)
}

// literalSentinel is a placeholder substituted in for each masked
// string/template/comment region. It is chosen so that none of the
// type-stripping regexes can match across it: it contains no whitespace,
// no `as`, no `:`, and no `{`/`}` braces. The %d index is restored 1:1.
const literalSentinel = "\x00MCPXLIT%dMCPXLIT\x00"

var reLiteralSentinel = regexp.MustCompile("\x00MCPXLIT(\\d+)MCPXLIT\x00")

// maskLiterals replaces every string literal ("..."/'...') and template
// literal (`...`) with an opaque sentinel, returning the masked code plus the
// ordered list of removed spans. It is a single-pass lexer that respects
// backslash escapes so the type-stripping regexes only ever see real code
// spans. Comments are deliberately NOT masked: the generated-header comment
// regex (reGenComment) must still see and remove them, and stripping a stray
// `as`/`:` from a comment is harmless since comments never execute. The
// lexer still skips over comment bodies so that a quote character inside a
// comment (e.g. `// it's fine`) does not open a phantom string literal.
func maskLiterals(code string) (string, []string) {
	var (
		out      strings.Builder
		literals []string
		i        int
		n        = len(code)
	)
	emit := func(span string) {
		fmt.Fprintf(&out, literalSentinel, len(literals))
		literals = append(literals, span)
	}
	for i < n {
		c := code[i]
		switch {
		case c == '"' || c == '\'' || c == '`':
			j := scanQuoted(code, i, c)
			emit(code[i:j])
			i = j
		case c == '/' && i+1 < n && code[i+1] == '/':
			// Copy the line comment through verbatim (do not mask).
			j := i + 2
			for j < n && code[j] != '\n' {
				j++
			}
			out.WriteString(code[i:j])
			i = j
		case c == '/' && i+1 < n && code[i+1] == '*':
			// Copy the block comment through verbatim (do not mask).
			j := i + 2
			for j+1 < n && (code[j] != '*' || code[j+1] != '/') {
				j++
			}
			if j+1 < n {
				j += 2 // include closing */
			} else {
				j = n
			}
			out.WriteString(code[i:j])
			i = j
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String(), literals
}

// scanQuoted returns the index just past the closing quote of the
// string/template literal that starts at start (whose delimiter is quote),
// honouring backslash escapes. Unterminated literals consume to EOF.
func scanQuoted(code string, start int, quote byte) int {
	n := len(code)
	j := start + 1
	for j < n {
		switch code[j] {
		case '\\':
			j += 2 // skip the escaped character
			continue
		case quote:
			return j + 1
		}
		j++
	}
	return n
}

// restoreLiterals replaces every sentinel with its original span. Sentinels
// that were removed wholesale (e.g. a string literal inside a stripped
// interface block) simply never appear in the input and are dropped.
func restoreLiterals(code string, literals []string) string {
	return reLiteralSentinel.ReplaceAllStringFunc(code, func(m string) string {
		sub := reLiteralSentinel.FindStringSubmatch(m)
		if len(sub) != 2 {
			return m
		}
		idx, err := strconv.Atoi(sub[1])
		if err != nil || idx < 0 || idx >= len(literals) {
			return m
		}
		return literals[idx]
	})
}

// cleanBlankLines collapses runs of 3+ blank lines into 2.
func cleanBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	blanks := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blanks++
			if blanks <= 2 {
				out = append(out, line)
			}
		} else {
			blanks = 0
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
