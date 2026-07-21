package admin

import (
	"strings"
	"testing"
)

// TestValidateExecuteScript exercises the real codemode.Preflight path: valid
// JS (incl. TypeScript annotations and empty) passes; syntax errors and the
// banned dynamic-code constructors are rejected at write time; the byte cap
// is enforced.
func TestValidateExecuteScript(t *testing.T) {
	cases := []struct {
		name    string
		script  string
		wantErr string // substring; "" = no error
	}{
		{"empty is valid", "", ""},
		{"whitespace is valid", "   \n\t", ""},
		{"plain js ok", `const r = fetch.fetch({url:"https://x"}); if (!r) abort("no");`, ""},
		{"typescript annotations stripped then ok", `const n: number = 1; if (n) abort("x");`, ""},
		{"syntax error rejected", `if (`, "syntax error"},
		{"eval rejected", `eval("2+2")`, "not allowed"},
		{"Function constructor rejected", `new Function("return 1")()`, "not allowed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExecuteScript("pre_execute_script", tc.script)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %q, want substring %q", err.Error(), tc.wantErr)
			}
			if !strings.Contains(err.Error(), "pre_execute_script") {
				t.Fatalf("err = %q, want it to name the field", err.Error())
			}
		})
	}
}

func TestValidateExecuteScript_SizeCap(t *testing.T) {
	huge := strings.Repeat("a", maxWorkerExecuteScriptBytes+1)
	if err := validateExecuteScript("post_execute_script", huge); err == nil ||
		!strings.Contains(err.Error(), "max") {
		t.Fatalf("oversize script err = %v, want a size-cap error", err)
	}
}
