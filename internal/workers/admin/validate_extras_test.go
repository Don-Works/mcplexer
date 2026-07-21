// validate_extras_test.go (M0.7) — table-driven coverage for the new
// output-channels + parameters validators. The skill-refs validator is
// covered alongside the multi-skill round-trip tests in
// skill_refs_test.go (whitebox via Service.Create).
package admin

import (
	"strings"
	"testing"
)

func TestValidateOutputChannelsJSON(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr string // substring; "" = no error
	}{
		{"empty", "", ""},
		{"null", "null", ""},
		{"empty array", "[]", ""},
		{"valid mesh", `[{"type":"mesh","priority":"normal"}]`, ""},
		{"valid file", `[{"type":"file","path":"/tmp/x"}]`, ""},
		{"valid mix", `[{"type":"mesh"},{"type":"file","path":"x"},{"type":"webhook","url":"https://a"}]`, ""},
		{"malformed JSON", `not-json`, "must be a JSON array"},
		{"wrong shape (object)", `{"type":"mesh"}`, "must be a JSON array"},
		{"missing type", `[{}]`, "missing or non-string"},
		{"non-string type", `[{"type":123}]`, "missing or non-string"},
		{"unknown type", `[{"type":"discord"}]`, "unknown type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOutputChannelsJSON(tc.raw)
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
		})
	}
}

func TestValidateParametersJSON(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{"empty", "", ""},
		{"null", "null", ""},
		{"empty object", "{}", ""},
		{"object", `{"a":"b","c":1}`, ""},
		{"array rejected", `[1,2,3]`, "must be a JSON object"},
		{"scalar rejected", `"hello"`, "must be a JSON object"},
		{"number rejected", `42`, "must be a JSON object"},
		{"malformed", `{nope}`, "must be valid JSON"},
		{"too large", `{"x":"` + strings.Repeat("a", 64*1024) + `"}`, "parameters_json max"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateParametersJSON(tc.raw)
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
		})
	}
}
