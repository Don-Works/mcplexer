package codemode

import (
	"strings"
	"testing"
)

func TestStripTypeScript_InterfaceBlocks(t *testing.T) {
	code := `interface Params {
  owner: string;
  repo: string;
}
const result = github.list_issues({ owner: "org", repo: "app" });`

	got := StripTypeScript(code)

	if strings.Contains(got, "interface") {
		t.Error("interface block should be removed")
	}
	if !strings.Contains(got, "const result = github.list_issues") {
		t.Error("function call should be preserved")
	}
}

func TestStripTypeScript_TypeAnnotations(t *testing.T) {
	code := `const x: string = "hello";
const y: number = 42;
const z: boolean = true;`

	got := StripTypeScript(code)

	if strings.Contains(got, ": string") {
		t.Errorf("type annotation not stripped: %s", got)
	}
	if !strings.Contains(got, `const x = "hello"`) {
		t.Errorf("expected clean assignment, got: %s", got)
	}
}

func TestStripTypeScript_AsCast(t *testing.T) {
	code := `const data = result as MyType;`

	got := StripTypeScript(code)

	if strings.Contains(got, "as MyType") {
		t.Errorf("as cast not stripped: %s", got)
	}
	if !strings.Contains(got, "const data = result;") {
		t.Errorf("expected clean assignment, got: %s", got)
	}
}

func TestStripTypeScript_DeclareKeyword(t *testing.T) {
	code := `declare namespace github {
  function list_issues(): any;
}
const x = github.list_issues();`

	got := StripTypeScript(code)

	if strings.Contains(got, "declare") {
		t.Errorf("declare keyword not stripped: %s", got)
	}
	if !strings.Contains(got, "const x = github.list_issues()") {
		t.Errorf("function call should be preserved, got: %s", got)
	}
}

func TestStripTypeScript_PreservesLogic(t *testing.T) {
	code := `const issues = github.list_issues({ owner: "org", repo: "app" });
const bugs = issues.filter(i => i.labels.includes("bug"));
for (const bug of bugs) {
  linear.create_issue({ title: bug.title, teamId: "ENG" });
}
print(bugs.length + " bugs synced");`

	got := StripTypeScript(code)

	if !strings.Contains(got, "github.list_issues") {
		t.Error("expected github.list_issues call")
	}
	if !strings.Contains(got, "issues.filter") {
		t.Error("expected filter call")
	}
	if !strings.Contains(got, "linear.create_issue") {
		t.Error("expected linear.create_issue call")
	}
	if !strings.Contains(got, `print(bugs.length + " bugs synced")`) {
		t.Error("expected print call")
	}
}

func TestStripTypeScript_EmptyInput(t *testing.T) {
	got := StripTypeScript("")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestStripTypeScript_PureJS(t *testing.T) {
	code := `const x = 42;
const y = x * 2;
print(y);`

	got := StripTypeScript(code)

	if !strings.Contains(got, "const x = 42") {
		t.Error("pure JS should be preserved")
	}
}

func TestStripTypeScript_PreservesObjectLiterals(t *testing.T) {
	// This was the critical bug: the regex was stripping object literal values
	// like { filters: { ... } } because it matched `: { ... }` as a type annotation.
	code := `const result = clickup.clickup_search({
  filters: {
    asset_types: ["task"],
    task_statuses: ["unstarted", "active"]
  },
  sort: [{ field: "updated_at", direction: "desc" }],
  count: 50
});`

	got := StripTypeScript(code)

	if !strings.Contains(got, `filters: {`) {
		t.Errorf("object literal value was stripped: %s", got)
	}
	if !strings.Contains(got, `asset_types: ["task"]`) {
		t.Errorf("nested object was stripped: %s", got)
	}
	if !strings.Contains(got, `sort: [{ field:`) {
		t.Errorf("array of objects was stripped: %s", got)
	}
	if !strings.Contains(got, `count: 50`) {
		t.Errorf("simple value was stripped: %s", got)
	}
}

func TestStripTypeScript_InterfaceWithNestedBraces(t *testing.T) {
	// Regression: reInterface regex must handle inline object types within interfaces.
	code := `interface CreateParams {
  config: { name: string; count: number };
  title: string;
}
const x = api.create({ config: { name: "test", count: 1 }, title: "hi" });`

	got := StripTypeScript(code)

	if strings.Contains(got, "interface") {
		t.Errorf("interface block not fully removed: %s", got)
	}
	// The orphaned `;\n}` from partial regex match would cause this to fail.
	if !strings.Contains(got, `const x = api.create`) {
		t.Errorf("function call should be preserved, got: %s", got)
	}
	// Must not contain orphaned closing brace from interface.
	lines := strings.Split(strings.TrimSpace(got), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "}" {
			t.Errorf("orphaned closing brace found: %s", got)
		}
	}
}

func TestStripTypeScript_PreservesNestedObjects(t *testing.T) {
	code := `const params = { name: "test", config: { enabled: true, tags: ["a", "b"] } };`

	got := StripTypeScript(code)

	if !strings.Contains(got, `config: { enabled: true`) {
		t.Errorf("nested object literal stripped: %s", got)
	}
}

func TestStripTypeScript_RemovesGeneratedAPIDeclarations(t *testing.T) {
	code := `// Auto-generated MCPlexer Code API
// Tool functions are synchronous — no await needed.

declare namespace github {
  interface ListIssuesParams {
    owner: string;
  }
  function list_issues(params: ListIssuesParams): any;
}

/** Print output that will be returned to the caller. */
declare function print(value: any): void;

const issues = github.list_issues({ owner: "org" });
print(issues);`

	got := StripTypeScript(code)

	if strings.Contains(got, "declare namespace") || strings.Contains(got, "function list_issues") {
		t.Fatalf("generated declarations should be stripped, got: %s", got)
	}
	if strings.Contains(got, "declare function print") {
		t.Fatalf("global declare function should be stripped, got: %s", got)
	}
	if !strings.Contains(got, `const issues = github.list_issues({ owner: "org" });`) {
		t.Fatalf("tool call should remain after stripping, got: %s", got)
	}
	if !strings.Contains(got, "print(issues);") {
		t.Fatalf("runtime print call should remain, got: %s", got)
	}
}

// TestStripTypeScript_AsInsideLiterals is the regression for the literal-aware
// masking fix: the `as`-cast regex (and var-annotation regex) must never fire
// inside string, single-quoted, or template literals. Before the fix,
// `print("run as fast")` was corrupted to `print("run fast")`.
func TestStripTypeScript_AsInsideLiterals(t *testing.T) {
	cases := []struct {
		name string
		code string
		want string
	}{
		{
			name: "double-quoted string with as",
			code: `print("run as fast as you can");`,
			want: `print("run as fast as you can");`,
		},
		{
			name: "single-quoted string with as",
			code: `const s = 'marked as done';`,
			want: `const s = 'marked as done';`,
		},
		{
			name: "template literal with as",
			code: "const t = `marked as done`;",
			want: "const t = `marked as done`;",
		},
		{
			name: "sql alias inside string",
			code: `db.query({ sql: "select count(*) as n from t" });`,
			want: `db.query({ sql: "select count(*) as n from t" });`,
		},
		{
			name: "as cast outside literal still stripped, literal preserved",
			code: `const x = (result as Foo); print("done as planned");`,
			want: `const x = (result); print("done as planned");`,
		},
		{
			name: "colon inside string not treated as type annotation",
			code: `const s = "key: value, other: thing";`,
			want: `const s = "key: value, other: thing";`,
		},
		{
			name: "escaped quote inside string does not end literal early",
			code: `const s = "she said \"go as fast\" now";`,
			want: `const s = "she said \"go as fast\" now";`,
		},
		{
			name: "template with embedded quote and as",
			code: "const t = `it's done as expected`;",
			want: "const t = `it's done as expected`;",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := StripTypeScript(tc.code)
			if got != tc.want {
				t.Fatalf("\n want: %q\n got:  %q", tc.want, got)
			}
		})
	}
}

// TestStripTypeScript_LiteralMaskingRoundTrips guards that masking does not
// disturb code that contains no TypeScript at all but does contain literals
// rich in `as`/`:`/braces.
func TestStripTypeScript_LiteralMaskingRoundTrips(t *testing.T) {
	code := `const cfg = { prompt: "treat X as Y: do { the thing }" };
const msg = ` + "`status: marked as done`" + `;
print(cfg, msg);`

	got := StripTypeScript(code)

	if !strings.Contains(got, `"treat X as Y: do { the thing }"`) {
		t.Errorf("string literal content corrupted, got:\n%s", got)
	}
	if !strings.Contains(got, "`status: marked as done`") {
		t.Errorf("template literal content corrupted, got:\n%s", got)
	}
}
