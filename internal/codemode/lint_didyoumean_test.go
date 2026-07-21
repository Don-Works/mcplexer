package codemode

import (
	"strings"
	"testing"
)

// TestLintWithTools_NamespaceTypoWarns asserts that a typo on the namespace
// portion of a `ns.member(` call (e.g. `gihub` instead of `github`) is
// surfaced as a WARNING with a did-you-mean over the registered namespaces.
func TestLintWithTools_NamespaceTypoWarns(t *testing.T) {
	tools := []string{"github__list_issues", "github__get_repo", "linear__create_issue"}

	result := LintWithTools(`gihub.list_issues({owner:"acme"});`, tools)
	if len(result.Warnings) == 0 {
		t.Fatal("expected a warning for namespace typo, got none")
	}

	var matched bool
	for _, w := range result.Warnings {
		if w.Severity != "warning" {
			continue
		}
		if strings.Contains(w.Message, "github") && strings.Contains(w.Message, "Did you mean") {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("expected did-you-mean: github in warnings, got %+v", result.Warnings)
	}
}

// TestLintWithTools_MemberTypoErrors asserts that calling a real namespace
// with an unknown member emits an ERROR with a did-you-mean over that
// namespace's members.
func TestLintWithTools_MemberTypoErrors(t *testing.T) {
	tools := []string{"github__list_issues", "github__get_repo"}

	result := LintWithTools(`github.list_isues({owner:"acme"});`, tools)
	if len(result.Warnings) == 0 {
		t.Fatal("expected an error for member typo, got none")
	}

	var matched bool
	for _, w := range result.Warnings {
		if w.Severity != "error" {
			continue
		}
		if strings.Contains(w.Message, "github.list_issues") && strings.Contains(w.Message, "Did you mean") {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("expected error suggesting github.list_issues, got %+v", result.Warnings)
	}
}

// TestLintWithTools_KnownToolNoFlag asserts that a registered call passes
// without any warning so we don't pollute clean code.
func TestLintWithTools_KnownToolNoFlag(t *testing.T) {
	tools := []string{"github__list_issues"}

	result := LintWithTools(`const r = github.list_issues({owner:"acme"}); print(r);`, tools)
	for _, w := range result.Warnings {
		if strings.Contains(w.Message, "Did you mean") || strings.Contains(w.Message, "not a known namespace") {
			t.Fatalf("did-you-mean false positive on registered call: %+v", w)
		}
	}
}

// TestLintWithTools_PropertyChainNotFlagged asserts that a chained property
// access (`items.map(x=>x.id)`) is never reported as a tool typo — the
// dotted form `array.method` looks like a namespace.member but the
// preceding/contextual semantics make it a property chain. The lint
// suppresses any match whose preceding character is `.` (property
// access) AND any match that lands on a known sandbox global.
func TestLintWithTools_PropertyChainNotFlagged(t *testing.T) {
	tools := []string{"github__list_issues"}

	cases := []struct {
		name string
		code string
	}{
		{
			name: "array method chain",
			code: `const ids = items.map(x => x.id);`,
		},
		{
			name: "JSON.parse sandbox builtin",
			code: `const x = JSON.parse(text);`,
		},
		{
			name: "Math.max sandbox builtin",
			code: `const m = Math.max(1, 2, 3);`,
		},
		{
			name: "console.log sandbox builtin",
			code: `console.log("x");`,
		},
		{
			name: "property chain three deep",
			code: `print(result.task.id);`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result := LintWithTools(tc.code, tools)
			for _, w := range result.Warnings {
				if strings.Contains(w.Message, "Did you mean") ||
					strings.Contains(w.Message, "not a registered tool") ||
					strings.Contains(w.Message, "not a known namespace") {
					t.Fatalf("false positive on %q: %+v", tc.name, w)
				}
			}
		})
	}
}

func TestLintWithTools_LocalBindingShadowsToolNamespace(t *testing.T) {
	tools := []string{"skill__run_start", "skill__phase", "mcpx__skill_get"}
	code := `
const skill = mcpx.skill_get({name:"using-mcplexer"});
print(skill.slice(0, 120));
`

	result := LintWithTools(code, tools)
	for _, w := range result.Warnings {
		if strings.Contains(w.Message, "skill.slice") ||
			strings.Contains(w.Message, "not a registered tool") ||
			strings.Contains(w.Message, "Did you mean") {
			t.Fatalf("local binding should not be treated as tool namespace: %+v", w)
		}
	}
}

func TestLintWithTools_LocalParametersAndLoopBindingsNotFlagged(t *testing.T) {
	tools := []string{"ip__lookup", "brw__open"}
	code := `
function normalize(s) { return s.replace(/\s+/g, " ").trim(); }
const ids = rows.map(x => x.id);
for (let i = 0; i < ids.length; i++) { print(i.toString()); }
for (const row of rows) { print(row.id); }
try { print(ids); } catch (err) { print(err.toString()); }
`
	result := LintWithTools(code, tools)
	for _, w := range result.Warnings {
		if strings.Contains(w.Message, "not a known namespace") ||
			strings.Contains(w.Message, "not a registered tool") {
			t.Fatalf("local binding treated as tool namespace: %+v", w)
		}
	}
}

func TestLintWithTools_DestructuredBindingsNotFlagged(t *testing.T) {
	tools := []string{"ip__lookup"}
	code := `
const {rows, meta: details} = response;
const [first, ...rest] = rows;
print(rows.map(x => x.id), details.toString(), first.toString(), rest.slice(1));
`
	result := LintWithTools(code, tools)
	for _, w := range result.Warnings {
		if strings.Contains(w.Message, "not a known namespace") {
			t.Fatalf("destructured binding treated as namespace: %+v", w)
		}
	}
}

func TestLintWithTools_CodeModeAliasForPunctuatedMember(t *testing.T) {
	tools := []string{"gcal__get-current-time"}
	result := LintWithTools(`gcal.get_current_time({});`, tools)
	for _, w := range result.Warnings {
		if strings.Contains(w.Message, "gcal.get_current_time") {
			t.Fatalf("generated Code Mode alias should be registered: %+v", w)
		}
	}
}

func TestLintWithTools_CrossNamespaceRegistrySuggestion(t *testing.T) {
	tools := []string{
		"skill__run_start", "skill__phase", "mcpx__skill_get", "mcpx__skill_search", "task__append_note",
	}
	for _, tc := range []struct {
		code string
		want string
	}{
		{`skill.get({name:"x"});`, "mcpx.skill_get"},
		{`skill.skill_search({query:"x"});`, "mcpx.skill_search"},
		{`mcplexer.skill_get({name:"x"});`, "mcpx.skill_get"},
		{`task.note({id:"x", note:"y"});`, "task.append_note"},
	} {
		result := LintWithTools(tc.code, tools)
		var joined strings.Builder
		for _, w := range result.Warnings {
			joined.WriteString(w.Message)
		}
		if !strings.Contains(joined.String(), tc.want) {
			t.Errorf("%s: expected suggestion %q, got %q", tc.code, tc.want, joined.String())
		}
	}
}

// TestLintWithTools_InsideStringNotFlagged confirms the masker hides
// quoted content from the typo regex. A model that prints `print("call
// gihub.list_issues like this")` must not trip a lint warning.
func TestLintWithTools_InsideStringNotFlagged(t *testing.T) {
	tools := []string{"github__list_issues"}

	cases := []string{
		`print("call gihub.list_issues like this");`,
		`print('see gihub.list_isues docs');`,
		"const s = `call gihub.list_isues here`;",
		`// gihub.list_isues — comment only`,
		`/* gihub.list_isues — block comment */`,
	}
	for _, code := range cases {
		t.Run(code, func(t *testing.T) {
			result := LintWithTools(code, tools)
			for _, w := range result.Warnings {
				if strings.Contains(w.Message, "gihub") || strings.Contains(w.Message, "list_isues") {
					t.Fatalf("string/comment content leaked to lint: %+v", w)
				}
			}
		})
	}
}

// TestLintWithTools_EmptyToolListNoWarnings ensures that running with no
// registered tools skips the typo pass entirely (so callers that omit
// the tool list don't break the standard Lint flow).
func TestLintWithTools_EmptyToolListNoWarnings(t *testing.T) {
	result := LintWithTools(`gihub.list_issues({});`, nil)
	for _, w := range result.Warnings {
		if strings.Contains(w.Message, "Did you mean") {
			t.Fatalf("unexpected did-you-mean without tool list: %+v", w)
		}
	}
}

// TestLintWithTools_ReportsLineNumbers verifies the warning carries the
// 1-indexed line number of the offending call.
func TestLintWithTools_ReportsLineNumbers(t *testing.T) {
	tools := []string{"github__list_issues"}
	code := `// line 1
// line 2
github.list_isues({owner:"acme"});
`
	result := LintWithTools(code, tools)
	if len(result.Warnings) == 0 {
		t.Fatal("expected a warning")
	}
	var found bool
	for _, w := range result.Warnings {
		if w.Line == 3 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected warning on line 3, got %+v", result.Warnings)
	}
}

// TestMaskLiteralsForLint asserts the masker preserves byte length and
// line breaks so byte-offset based regex lookups stay aligned. Sentinel
// constants from strip.go are intentionally NOT used here — strip.go's
// masker shifts offsets, which would break the lint pass.
func TestMaskLiteralsForLint(t *testing.T) {
	cases := []struct {
		name string
		in   string
		// want is the expected masked form. Whitespace replaces quoted
		// payload bytes; quotes and code outside literals are untouched.
		want string
	}{
		{
			name: "double-quoted body becomes spaces",
			in:   `print("hello");`,
			want: `print("     ");`,
		},
		{
			name: "single-quoted body becomes spaces",
			in:   `print('a.b()');`,
			want: `print('     ');`,
		},
		{
			name: "template body becomes spaces",
			in:   "print(`a.b()`);",
			want: "print(`     `);",
		},
		{
			name: "line comment body becomes spaces",
			in:   `// gihub.list_isues`,
			want: `                   `,
		},
		{
			name: "block comment body becomes spaces",
			in:   `/* gihub.list_isues */`,
			want: `                      `,
		},
		{
			name: "no string/comment passes through",
			in:   `const x = 1 + 2;`,
			want: `const x = 1 + 2;`,
		},
		{
			name: "newline inside string preserved",
			in:   "const s = \"a\nb\";",
			want: "const s = \" \n \";",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := maskLiteralsForLint(tc.in)
			if len(got) != len(tc.in) {
				t.Fatalf("mask changed length: in %d, out %d", len(tc.in), len(got))
			}
			if got != tc.want {
				t.Fatalf("mask mismatch:\n want: %q\n got:  %q", tc.want, got)
			}
		})
	}
}
