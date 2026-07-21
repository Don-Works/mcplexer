package audit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// hintedScopeStore returns an AuthScope whose RedactionHints decode to
// the supplied per-scope hints. Used to exercise the hint-aware
// ErrorMessage redaction path (H1 in the security audit).
type hintedScopeStore struct {
	store.AuthScopeStore
	hints []string
}

func (h hintedScopeStore) GetAuthScope(context.Context, string) (*store.AuthScope, error) {
	raw, _ := json.Marshal(h.hints)
	return &store.AuthScope{ID: "scope-1", RedactionHints: raw}, nil
}

// TestLogger_RecordRedactsErrorMessage covers the credential shapes
// listed in H1: Stripe/OpenAI sk_ keys, GitHub PATs, Slack tokens,
// JWT-shaped bearer fragments. Each plant is dropped into ErrorMessage
// and must not survive Record. Hints are also exercised so a per-scope
// label like "GITHUB_TOKEN" catches the raw value when it doesn't
// match a globally-known prefix.
func TestLogger_RecordRedactsErrorMessage(t *testing.T) {
	cases := []struct {
		name     string
		errMsg   string
		hints    []string
		mustMiss []string // substrings that must NOT survive
		mustHave []string // substrings that must survive (context preserved)
	}{
		{
			name:     "stripe sk_live",
			errMsg:   "claude_cli: run: exit status 1 (stderr: api_key=" + secretFixture("sk", "_live_", "aBcDeFgHiJkLmNoPqRsTuV1234") + ")",
			mustMiss: []string{secretFixture("sk", "_live_", "aBcDeFgHiJkLmNoPqRsTuV1234")},
			mustHave: []string{"claude_cli", "exit status"},
		},
		{
			name:     "openai sk-proj",
			errMsg:   "anthropic: 401: invalid key " + secretFixture("sk", "-proj-", "abcdefghij1234567890abcdefghij"),
			mustMiss: []string{secretFixture("sk", "-proj-", "abcdefghij1234567890abcdefghij")},
			mustHave: []string{"anthropic", "401"},
		},
		{
			name:     "github ghp token",
			errMsg:   "github: token " + secretFixture("ghp", "_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789") + " rejected",
			mustMiss: []string{secretFixture("ghp", "_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789")},
			mustHave: []string{"github:", "rejected"},
		},
		{
			name:     "slack xoxb bot token",
			errMsg:   "slack post failed: " + secretFixture("xoxb", "-12345-67890-aBcDeFgHiJkLmN"),
			mustMiss: []string{secretFixture("xoxb", "-12345-67890-aBcDeFgHiJkLmN")},
			mustHave: []string{"slack post failed"},
		},
		{
			name:     "bearer-shaped JWT in stderr capture",
			errMsg:   "got 401: Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature_part_long_enough",
			mustMiss: []string{"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"},
			mustHave: []string{"got 401"},
		},
		{
			name:     "gitlab pat",
			errMsg:   "gitlab: token glpat-xxxxxxxxxxxxxxxxxxxx invalid",
			mustMiss: []string{"glpat-xxxxxxxxxxxxxxxxxxxx"},
			mustHave: []string{"gitlab:", "invalid"},
		},
		{
			name:     "hint-matched custom token name",
			errMsg:   "downstream rejected: corp_internal_token_xyz",
			hints:    []string{"corp_internal_token_xyz"},
			mustMiss: []string{"corp_internal_token_xyz"},
			mustHave: []string{"downstream rejected"},
		},
		{
			name:     "plain go error preserved",
			errMsg:   "get worker: not found",
			mustHave: []string{"get worker: not found"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &fakeAuditStore{}
			l := NewLogger(a, hintedScopeStore{hints: c.hints}, nil)
			rec := &store.AuditRecord{
				ToolName:     "worker_run.failed",
				AuthScopeID:  "scope-1",
				ErrorMessage: c.errMsg,
			}
			if err := l.Record(context.Background(), rec); err != nil {
				t.Fatalf("Record: %v", err)
			}
			got := a.records[0].ErrorMessage
			for _, miss := range c.mustMiss {
				if strings.Contains(got, miss) {
					t.Errorf("ErrorMessage still contains %q: %s", miss, got)
				}
			}
			for _, hit := range c.mustHave {
				if !strings.Contains(got, hit) {
					t.Errorf("ErrorMessage lost expected context %q: %s", hit, got)
				}
			}
		})
	}
}

// TestLogger_RecordRedactsMemoryBody is the end-to-end contract for the
// memory deep-redaction scenario (D7.5). memory__save's `content` field
// is a free-form markdown body that flows straight into the audit row's
// ParamsRedacted via gateway dispatch (recordAudit → auditor.Record).
// A canonical secret embedded in that body MUST be replaced with
// [REDACTED] before the row hits the store — the body the user stored
// is fine (that's their data), but the audit ledger must stay clean.
//
// This test pairs the fixture-driven coverage in
// redact_patterns_test.go (which exercises Redact alone) with the
// Logger.Record path (Redact + ensureCorrelationInParams + Insert)
// so a future refactor that re-orders the redaction step inside
// Logger.Record will be caught here, not just at the helper level.
func TestLogger_RecordRedactsMemoryBody(t *testing.T) {
	cases := []struct {
		name      string
		plaintext string
	}{
		{"openai_sk_proj", secretFixture("sk", "-proj-", "abcdefghij1234567890abcdefghij1234567890abcd")},
		{"anthropic_sk_ant", secretFixture("sk", "-ant-api03-", "aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789aBcDeFgH")},
		{"github_pat_classic", secretFixture("ghp", "_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789")},
		{"github_oauth_user", secretFixture("ghu", "_16C7e42F292c6912E7710c838347Ae178B4a")},
		{"stripe_sk_live", secretFixture("sk", "_live_", "aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789")},
		{"aws_access_key", secretFixture("AKIA", "IOSFODNN7EXAMPLE")},
		{"bearer_jwt", "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature_part_long_enough"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &fakeAuditStore{}
			l := NewLogger(a, nopScopeStore{}, nil)
			// Shape mirrors the JSON-RPC params for memory__save: a
			// top-level object with `name`, `content`, `tags`. The
			// secret rides in `content` — the longest, most common
			// place for leaks per the D7.5 finding.
			params, err := json.Marshal(map[string]any{
				"name":    "leaky-memory",
				"content": "rotation note: " + c.plaintext + " expires Q4",
				"tags":    []string{"audit-test"},
			})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			rec := &store.AuditRecord{
				ToolName:       "memory__save",
				ParamsRedacted: params,
			}
			if err := l.Record(context.Background(), rec); err != nil {
				t.Fatalf("Record: %v", err)
			}
			if len(a.records) != 1 {
				t.Fatalf("got %d records, want 1", len(a.records))
			}
			got := string(a.records[0].ParamsRedacted)
			if strings.Contains(got, c.plaintext) {
				t.Errorf("plaintext %q leaked into audit row:\n%s", c.plaintext, got)
			}
			// Sanity: the audit row should still be a structured JSON
			// object the dashboard can parse — redaction replaces the
			// value, it doesn't smash the shape.
			var probe map[string]any
			if err := json.Unmarshal([]byte(got), &probe); err != nil {
				t.Errorf("audit row no longer valid JSON: %v\n%s", err, got)
			}
			if probe["name"] != "leaky-memory" {
				t.Errorf("non-secret fields mangled: %v", probe)
			}
		})
	}
}

// TestLogger_RecordEmptyErrorMessageUnchanged guards against spurious
// allocation / mutation when ErrorMessage is empty (the common case).
func TestLogger_RecordEmptyErrorMessageUnchanged(t *testing.T) {
	a := &fakeAuditStore{}
	l := NewLogger(a, nopScopeStore{}, nil)
	rec := &store.AuditRecord{ToolName: "x"}
	if err := l.Record(context.Background(), rec); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if a.records[0].ErrorMessage != "" {
		t.Fatalf("ErrorMessage was %q, want empty", a.records[0].ErrorMessage)
	}
}

// TestRedactString_TableDriven exercises RedactString in isolation so
// the helper has direct coverage independent of the Logger.Record path.
func TestRedactString_TableDriven(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		hints    []string
		want     string // exact match (skipped when empty)
		mustMiss []string
		mustHave []string
	}{
		{
			name: "empty stays empty",
			in:   "",
			want: "",
		},
		{
			name: "go error survives unchanged",
			in:   "get worker: not found",
			want: "get worker: not found",
		},
		{
			name:     "stripe key redacted",
			in:       "stderr: " + secretFixture("sk", "_live_", "abcdef1234567890abcdef1234567890"),
			mustMiss: []string{secretFixture("sk", "_live_", "abcdef1234567890abcdef1234567890")},
			mustHave: []string{"stderr:", redactedValue},
		},
		{
			name:     "github pat redacted",
			in:       secretFixture("ghp", "_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789") + " here",
			mustMiss: []string{secretFixture("ghp", "_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789")},
		},
		{
			name:     "slack xoxb redacted",
			in:       secretFixture("xoxb", "-12345-67890-aBcDeFgHiJkLmN") + " trailing",
			mustMiss: []string{secretFixture("xoxb", "-12345-67890-aBcDeFgHiJkLmN")},
			mustHave: []string{"trailing"},
		},
		{
			name:     "jwt redacted",
			in:       "tok eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature_part_long_enough end",
			mustMiss: []string{"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"},
			mustHave: []string{"tok", "end"},
		},
		{
			name:     "gitlab pat redacted",
			in:       "glpat-xxxxxxxxxxxxxxxxxxxx fail",
			mustMiss: []string{"glpat-xxxxxxxxxxxxxxxxxxxx"},
		},
		{
			name:     "hint substring redacted case-insensitively",
			in:       "value MyCustomSecret leaked",
			hints:    []string{"mycustomsecret"},
			mustMiss: []string{"MyCustomSecret"},
			mustHave: []string{"value", "leaked"},
		},
		{
			name:  "empty hint is a no-op",
			in:    "no secrets here",
			hints: []string{""},
			want:  "no secrets here",
		},
		{
			// Regression (length-changing lowercase): 'İ' (U+0130, 2 bytes)
			// lowercases to a different byte length, so the previous
			// lowercase-and-index implementation drifted its slice offsets,
			// corrupting the multibyte rune and clipping surrounding text.
			// The redaction must hit only the hint and leave the multibyte
			// char + spacing intact (no corruption, no panic).
			name:     "length-changing unicode (İ) around hint does not corrupt",
			in:       "prefix İ secret suffix",
			hints:    []string{"secret"},
			want:     "prefix İ " + redactedValue + " suffix",
			mustMiss: []string{"secret"},
			mustHave: []string{"İ", "prefix ", " suffix"},
		},
		{
			// 'ß' (U+00DF) — exercises the no-byte-drift property of the
			// regexp-based matcher around a multibyte char.
			name:     "sharp-s near hint stays intact",
			in:       "straße token here",
			hints:    []string{"token"},
			want:     "straße " + redactedValue + " here",
			mustHave: []string{"straße"},
			mustMiss: []string{"token"},
		},
		{
			// Adversarial: a hint whose own bytes change length on lower —
			// must not panic and must redact the matching occurrence
			// case-insensitively.
			name:     "length-changing unicode hint",
			in:       "value İSTANBUL secret",
			hints:    []string{"İstanbul"},
			mustMiss: []string{"İSTANBUL"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RedactString(c.in, c.hints)
			if c.want != "" && got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
			for _, m := range c.mustMiss {
				if strings.Contains(got, m) {
					t.Errorf("output still contains %q: %s", m, got)
				}
			}
			for _, m := range c.mustHave {
				if !strings.Contains(got, m) {
					t.Errorf("output missing %q: %s", m, got)
				}
			}
		})
	}
}
