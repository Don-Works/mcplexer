package codemode

import (
	"strings"
	"testing"
)

func TestLint_JSONParseWarning(t *testing.T) {
	code := `const issues = JSON.parse(github.list_issues({owner: "org"}));`
	result := Lint(code)
	if len(result.Warnings) == 0 {
		t.Fatal("expected warning for JSON.parse")
	}
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w.Message, "JSON.parse") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected JSON.parse warning, got: %+v", result.Warnings)
	}
}

func TestLint_JSONParseUntrustedContentParserNoWarning(t *testing.T) {
	code := `
function unwrap(r) {
  const m = String(r.text || r).match(/<untrusted-content[^>]*>([\s\S]*?)<\/untrusted-content>/);
  return JSON.parse(m ? m[1] : r);
}`
	result := Lint(code)
	for _, w := range result.Warnings {
		if strings.Contains(w.Message, "JSON.parse") {
			t.Fatalf("untrusted-content parser should not get JSON.parse warning, got: %+v", result.Warnings)
		}
	}
}

func TestLint_JSONParseInsideStringNoWarning(t *testing.T) {
	code := `print("use JSON.parse() on server response");`
	result := Lint(code)
	for _, w := range result.Warnings {
		if strings.Contains(w.Message, "JSON.parse") {
			t.Errorf("JSON.parse inside string should not warn, got: %s", w.Message)
		}
	}
}

func TestLint_AwaitWarning(t *testing.T) {
	code := `const issues = await github.list_issues({owner: "org"});`
	result := Lint(code)
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w.Message, "await") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected await warning, got: %+v", result.Warnings)
	}
}

func TestLint_AsyncFunctionWarning(t *testing.T) {
	code := `async function fetchData() { return github.list_issues(); }`
	result := Lint(code)
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w.Message, "async function") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected async function warning, got: %+v", result.Warnings)
	}
}

func TestLint_AsyncArrowWarning(t *testing.T) {
	code := `const fn = async (x) => { return github.list_issues(); };`
	result := Lint(code)
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w.Message, "async") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected async arrow warning, got: %+v", result.Warnings)
	}
}

func TestLint_ConsoleLogWithoutPrintGivesHint(t *testing.T) {
	code := `console.log(github.list_issues());`
	result := Lint(code)
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w.Message, "print()") && strings.Contains(w.Severity, "hint") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected console.log hint, got: %+v", result.Warnings)
	}
}

func TestLint_ConsoleLogWithPrintNoHint(t *testing.T) {
	code := `const r = github.list_issues(); print(r); console.log("debug");`
	result := Lint(code)
	for _, w := range result.Warnings {
		if strings.Contains(w.Message, "print()") {
			t.Errorf("no console.log hint expected when print() is present, got: %s", w.Message)
		}
	}
}

func TestLint_CleanCodeNoWarnings(t *testing.T) {
	code := `const issues = github.list_issues({owner: "org"});
const count = issues.length;
print("count: " + count);`
	result := Lint(code)
	if len(result.Warnings) > 0 {
		t.Errorf("expected no warnings for clean code, got: %+v", result.Warnings)
	}
}

func TestLint_AwaitInsideStringLiteralNoWarning(t *testing.T) {
	code := `const msg = "please await the result"; print(msg);`
	result := Lint(code)
	for _, w := range result.Warnings {
		if strings.Contains(w.Message, "await") {
			t.Errorf("await inside string should not warn, got: %s", w.Message)
		}
	}
}

func TestDidYouMean_ExactMatchNotSuggested(t *testing.T) {
	names := []string{"github__list_issues", "github__get_repo", "github__create_issue"}
	suggestions := DidYouMean("github__list_issues", names, 3)
	if len(suggestions) > 0 {
		t.Errorf("exact match should not be suggested, got: %v", suggestions)
	}
}

func TestDidYouMean_CloseMatch(t *testing.T) {
	names := []string{"github__list_issues", "github__get_repo", "linear__list_issues"}
	suggestions := DidYouMean("github__list_issu", names, 3)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestion for close match")
	}
	if suggestions[0] != "github__list_issues" {
		t.Errorf("expected github__list_issues as top suggestion, got: %v", suggestions)
	}
}

func TestDidYouMean_UnderscoreNormalizedMatch(t *testing.T) {
	names := []string{"github__list_issues", "github__create_issue"}
	// Simulate LLM generating single underscore instead of double underscore.
	suggestions := DidYouMean("github_list_issues", names, 3)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestion for normalized match")
	}
	if suggestions[0] != "github__list_issues" {
		t.Errorf("expected github__list_issues, got: %v", suggestions)
	}
}

func TestDidYouMean_NoMatchReturnsEmpty(t *testing.T) {
	names := []string{"github__list_issues", "linear__create_issue"}
	suggestions := DidYouMean("postgres__query", names, 3)
	if len(suggestions) > 0 {
		t.Errorf("expected no suggestions for distant match, got: %v", suggestions)
	}
}

func TestDidYouMean_EmptyInput(t *testing.T) {
	suggestions := DidYouMean("", []string{"a", "b"}, 3)
	if len(suggestions) > 0 {
		t.Errorf("expected empty for empty input, got: %v", suggestions)
	}
}

func TestDidYouMean_EmptyNames(t *testing.T) {
	suggestions := DidYouMean("foo", nil, 3)
	if len(suggestions) > 0 {
		t.Errorf("expected empty for nil names, got: %v", suggestions)
	}
}

func TestDidYouMean_MaxSuggestionsRespected(t *testing.T) {
	names := []string{"github__list_issues", "github__list_issue", "github__list_isues"}
	suggestions := DidYouMean("github__list_issu", names, 2)
	if len(suggestions) > 2 {
		t.Errorf("expected at most 2 suggestions, got %d: %v", len(suggestions), suggestions)
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"a", "", 1},
		{"", "a", 1},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"abc", "ab", 1},
		{"abc", "abcd", 1},
		{"kitten", "sitting", 3},
		{"abc", "xyz", 3},
	}

	for _, tc := range cases {
		got := levenshtein(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestInsideStringLiteral(t *testing.T) {
	cases := []struct {
		name string
		code string
		pos  int
		want bool
	}{
		{
			name: "outside string",
			code: `print("hello")`,
			pos:  0,
			want: false,
		},
		{
			name: "inside double-quoted string",
			code: `print("hello")`,
			pos:  8,
			want: true,
		},
		{
			name: "inside template literal",
			code: "const x = `result: ${y}`;",
			pos:  12,
			want: true,
		},
		{
			name: "inside single-quoted string",
			code: `const s = 'hello';`,
			pos:  12,
			want: true,
		},
		{
			name: "after string",
			code: `const s = 'hello'; print(s);`,
			pos:  20,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := insideStringLiteral(tc.code, tc.pos)
			if got != tc.want {
				t.Errorf("insideStringLiteral(%q, %d) = %v, want %v", tc.code, tc.pos, got, tc.want)
			}
		})
	}
}

func TestFormatLintWarnings_Empty(t *testing.T) {
	if s := FormatLintWarnings(nil); s != "" {
		t.Errorf("expected empty for nil, got: %q", s)
	}
}

func TestFormatLintWarnings_Formats(t *testing.T) {
	warnings := []LintWarning{
		{Line: 5, Message: "test warning", Severity: "warning"},
	}
	s := FormatLintWarnings(warnings)
	if !strings.Contains(s, "test warning") {
		t.Errorf("expected warning text in output, got: %s", s)
	}
	if !strings.Contains(s, "[warning]") {
		t.Errorf("expected severity prefix, got: %s", s)
	}
}

func TestNormalizeForMatch(t *testing.T) {
	if got := normalizeForMatch("Foo__Bar_Baz"); got != "foobarbaz" {
		t.Errorf("normalizeForMatch = %q, want %q", got, "foobarbaz")
	}
	if got := normalizeForMatch("github__list-issues"); got != "githublistissues" {
		t.Errorf("normalizeForMatch = %q, want %q", got, "githublistissues")
	}
}
