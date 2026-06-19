package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestRedact_CanonicalSecretShapes is the regression contract for the
// audit redactor: every plaintext line in testdata/secret_patterns.txt
// must be stripped from a JSON memory body before that body lands in
// audit_records.params_redacted.
//
// This locks the redactor coverage against the canonical shapes the
// memory-body deep-redaction scenario (D7.5) caught as leaking.
// New shapes go in the fixture first, then the regex follows.
//
// The body shape mirrors how memory__save flows: the gateway hands the
// JSON-RPC params (which include `content`) to Logger.Record, which
// runs Redact() before InsertAuditRecord. Embedding the plaintext in a
// `content` field exercises that path end-to-end through the recursion
// (object → scalar string → valueRedactPatterns).
func TestRedact_CanonicalSecretShapes(t *testing.T) {
	cases := loadSecretFixture(t, "testdata/secret_patterns.txt")
	if len(cases) == 0 {
		t.Fatal("fixture is empty — refusing to declare coverage")
	}
	for _, tc := range cases {
		t.Run(tc.category, func(t *testing.T) {
			// Mirror the memory__save MCP arg shape so the test exercises
			// the same code path the audit emit site uses (recursive
			// Redact on a JSON object with a scalar `content` field).
			body, err := json.Marshal(map[string]any{
				"name":    "leaky-memory",
				"content": "Here is the secret: " + tc.plaintext + " (do not share)",
			})
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			redacted := Redact(json.RawMessage(body), nil)
			if strings.Contains(string(redacted), tc.plaintext) {
				t.Errorf("plaintext for %s survived redaction:\n  in:  %s\n  out: %s",
					tc.category, tc.plaintext, string(redacted))
			}
			if !strings.Contains(string(redacted), redactedValue) {
				t.Errorf("redaction placeholder missing for %s — pattern did not match:\n  out: %s",
					tc.category, string(redacted))
			}
			// Context preservation: the surrounding markdown should
			// survive so an auditor can still tell what happened.
			if !strings.Contains(string(redacted), "do not share") {
				t.Errorf("surrounding context lost for %s:\n  out: %s",
					tc.category, string(redacted))
			}
		})
	}
}

// TestRedact_CanonicalSecretShapes_String exercises RedactString
// directly so the same fixture covers the ErrorMessage-redaction
// path (used by Logger.Record for adapter stderr / subprocess errors)
// without requiring a JSON envelope.
func TestRedact_CanonicalSecretShapes_String(t *testing.T) {
	cases := loadSecretFixture(t, "testdata/secret_patterns.txt")
	for _, tc := range cases {
		t.Run(tc.category, func(t *testing.T) {
			in := "downstream stderr: " + tc.plaintext + " — failure"
			out := RedactString(in, nil)
			if strings.Contains(out, tc.plaintext) {
				t.Errorf("plaintext for %s survived RedactString:\n  in:  %s\n  out: %s",
					tc.category, tc.plaintext, out)
			}
		})
	}
}

type secretFixtureCase struct {
	category  string
	plaintext string
}

// loadSecretFixture parses tab-separated <category>\t<plaintext> rows
// from path, skipping blanks and lines starting with `#`. Fails the
// test on IO or shape errors so a malformed fixture is loud.
func loadSecretFixture(t *testing.T, path string) []secretFixtureCase {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()
	var out []secretFixtureCase
	scanner := bufio.NewScanner(f)
	// 1 MiB — generous for PEM-shaped multi-line plaintexts.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		raw := strings.TrimRight(scanner.Text(), " \t\r")
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		parts := strings.SplitN(raw, "\t", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			t.Fatalf("fixture %s:%d malformed (need <category>\\t<plaintext>): %q",
				path, line, raw)
		}
		// PEM is stored on one line with literal `\n` separators so the
		// fixture file stays single-line-per-row. Provider-token fixtures
		// also split sensitive prefixes with <join> so repository secret
		// scanners never see complete token-shaped strings at rest.
		plaintext := strings.ReplaceAll(parts[1], "<join>", "")
		plaintext = strings.ReplaceAll(plaintext, `\n`, "\n")
		out = append(out, secretFixtureCase{
			category:  parts[0],
			plaintext: plaintext,
		})
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	return out
}
