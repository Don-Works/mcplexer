package audit

import (
	"encoding/json"
	"strings"
	"testing"
)

func secretFixture(parts ...string) string {
	return strings.Join(parts, "")
}

func TestRedact_KeyPatterns(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string // substrings that must NOT appear in the output
	}{
		{
			name: "top level token",
			in:   `{"token":"abc123","name":"alice"}`,
			want: []string{"abc123"},
		},
		{
			name: "api_key",
			in:   `{"api_key":"` + secretFixture("sk", "-live-", "1234567890abcdef") + `","ok":1}`,
			want: []string{secretFixture("sk", "-live-", "1234567890abcdef")},
		},
		{
			name: "nested cookie",
			in:   `{"req":{"headers":{"Cookie":"sess=secretvalue"}},"path":"/x"}`,
			want: []string{"secretvalue"},
		},
		{
			name: "client_secret",
			in:   `{"client_secret":"hunter2","client_id":"public"}`,
			want: []string{"hunter2"},
		},
		{
			name: "passphrase",
			in:   `{"passphrase":"correcthorsebatterystaple"}`,
			want: []string{"correcthorsebatterystaple"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := Redact(json.RawMessage(c.in), nil)
			if string(out) == c.in && len(c.want) > 0 {
				t.Errorf("expected redaction, got identity: %s", string(out))
			}
			for _, sub := range c.want {
				if strings.Contains(string(out), sub) {
					t.Errorf("output still contains %q: %s", sub, string(out))
				}
			}
		})
	}
}

func TestRedact_ValuePatterns(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "bearer token in instructions",
			in:   `{"instructions":"Use Bearer abcdef0123456789ABCDEF to call X"}`,
			want: []string{"abcdef0123456789"},
		},
		{
			name: "github PAT in note",
			in:   `{"note":"key is ` + secretFixture("ghp", "_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789") + `"}`,
			want: []string{secretFixture("ghp", "_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789")},
		},
		{
			name: "openai key in plain text",
			in:   `"` + secretFixture("sk", "-proj-", "abcdefghij1234567890abcdefghij") + `"`,
			want: []string{secretFixture("sk", "-proj-", "abcdefghij1234567890abcdefghij")},
		},
		{
			name: "AWS access key in description",
			in:   `{"description":"id ` + secretFixture("AKIA", "IOSFODNN7EXAMPLE") + ` here"}`,
			want: []string{secretFixture("AKIA", "IOSFODNN7EXAMPLE")},
		},
		{
			name: "JWT in description",
			in:   `{"description":"token eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature_part_long_enough"}`,
			want: []string{"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"},
		},
		{
			// Regression: the array branch (redact.go) is load-bearing for
			// tool params that pass lists. A top-level JSON array whose
			// element is credential-shaped must be redacted.
			name: "top-level array with github PAT element",
			in:   `["` + secretFixture("ghp", "_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789") + `","plain"]`,
			want: []string{secretFixture("ghp", "_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789")},
		},
		{
			// Regression: array nested under a non-secret key, credential
			// in a *positional* element (no key match). Pattern matching
			// must still fire on the array element value.
			name: "nested array positional token (no key match)",
			in:   `{"args":["--token","` + secretFixture("ghp", "_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789") + `"]}`,
			want: []string{secretFixture("ghp", "_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789")},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := Redact(json.RawMessage(c.in), nil)
			for _, sub := range c.want {
				if strings.Contains(string(out), sub) {
					t.Errorf("output still contains %q: %s", sub, string(out))
				}
			}
		})
	}
}

// FuzzRedactString feeds arbitrary UTF-8 to RedactString to assert the
// load-bearing property: redaction never panics on adversarial bytes,
// regardless of how lowercasing changes byte lengths.
func FuzzRedactString(f *testing.F) {
	seeds := []string{
		"",
		"prefix İ secret suffix",
		"straße token",
		secretFixture("ghp", "_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789"),
		"İSTANBUL",
		"\x00\xff invalid utf8 secret",
	}
	for _, s := range seeds {
		f.Add(s, "secret")
	}
	f.Fuzz(func(t *testing.T, s, hint string) {
		// Property: never panics; result is well-defined.
		_ = RedactString(s, []string{hint})
	})
}

func TestRedact_PreservesNonSensitive(t *testing.T) {
	in := `{"name":"alice","count":42}`
	out := Redact(json.RawMessage(in), nil)
	if string(out) != in {
		t.Errorf("non-sensitive params should be unchanged; got %s", string(out))
	}
}

func TestRedact_HintsApply(t *testing.T) {
	in := `{"my_field":"hidden value","ok":1}`
	out := Redact(json.RawMessage(in), []string{"my_field"})
	if strings.Contains(string(out), "hidden value") {
		t.Errorf("hint-matched key should be redacted: %s", string(out))
	}
}

func TestRedact_Idempotent(t *testing.T) {
	in := `{"token":"abc"}`
	once := Redact(json.RawMessage(in), nil)
	twice := Redact(once, nil)
	if string(once) != string(twice) {
		t.Errorf("Redact should be idempotent; got %s -> %s", string(once), string(twice))
	}
}

func TestRedactArgs(t *testing.T) {
	cases := []struct {
		name      string
		in        []string
		hints     []string
		wantOut   []string // exact expected output (nil = don't check exactly)
		mustHave  []string // substrings that must appear in joined output
		mustMiss  []string // substrings that must NOT appear in joined output
		wantEmpty bool     // expect a zero-length result
	}{
		{
			name:     "positional bearer token",
			in:       []string{"curl", "-H", "Authorization: Bearer abc1234567890abcdef"},
			mustMiss: []string{"abc1234567890abcdef"},
			mustHave: []string{"curl", "-H", redactedValue},
		},
		{
			name:     "flag=value key match (token)",
			in:       []string{"--token=GHP_REAL_PAT_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},
			wantOut:  []string{"--token=" + redactedValue},
			mustMiss: []string{"GHP_REAL_PAT_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},
		},
		{
			name:    "flag=value key match (password)",
			in:      []string{"--password=hunter2"},
			wantOut: []string{"--password=" + redactedValue},
		},
		{
			name:    "key=value no match passes through",
			in:      []string{"client_id=public"},
			wantOut: []string{"client_id=public"},
		},
		{
			name:     "flag=value hint match",
			in:       []string{"--api-key=" + secretFixture("sk", "-live-", "abcdefghij1234567890abcdef")},
			hints:    []string{"api-key"},
			wantOut:  []string{"--api-key=" + redactedValue},
			mustMiss: []string{secretFixture("sk", "-live-", "abcdefghij1234567890abcdef")},
		},
		{
			name:      "nil input is nil-safe",
			in:        nil,
			wantEmpty: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RedactArgs(c.in, c.hints)
			if c.wantEmpty {
				if len(got) != 0 {
					t.Fatalf("expected empty slice, got %v", got)
				}
				return
			}
			if c.wantOut != nil {
				if len(got) != len(c.wantOut) {
					t.Fatalf("len mismatch: got %v want %v", got, c.wantOut)
				}
				for i := range got {
					if got[i] != c.wantOut[i] {
						t.Errorf("idx %d: got %q want %q", i, got[i], c.wantOut[i])
					}
				}
			}
			joined := strings.Join(got, "\n")
			for _, m := range c.mustHave {
				if !strings.Contains(joined, m) {
					t.Errorf("expected %q in %q", m, joined)
				}
			}
			for _, m := range c.mustMiss {
				if strings.Contains(joined, m) {
					t.Errorf("did not want %q in %q", m, joined)
				}
			}
			// Input must not be mutated.
			if len(c.in) > 0 && &c.in[0] == &got[0] {
				t.Errorf("RedactArgs returned same underlying slice")
			}
		})
	}
}

func TestRedactEnv(t *testing.T) {
	cases := []struct {
		name     string
		in       map[string]string
		hints    []string
		want     map[string]string
		mustMiss []string
		mustHave map[string]string // key -> substring that must appear in value
	}{
		{
			name: "github token key match",
			in:   map[string]string{"GITHUB_TOKEN": secretFixture("ghp", "_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789")},
			want: map[string]string{"GITHUB_TOKEN": redactedValue},
		},
		{
			name: "non-sensitive passes through",
			in:   map[string]string{"NODE_ENV": "production"},
			want: map[string]string{"NODE_ENV": "production"},
		},
		{
			name: "substring authorization key match",
			in:   map[string]string{"DEBUG_AUTHORIZATION": "Bearer xxx1234567890abcdef"},
			want: map[string]string{"DEBUG_AUTHORIZATION": redactedValue},
		},
		{
			name:     "value pattern leaves url but redacts bearer",
			in:       map[string]string{"WEBHOOK_URL": "https://hooks.example.com/Bearer abc1234567890abcdef"},
			mustMiss: []string{"abc1234567890abcdef"},
			mustHave: map[string]string{"WEBHOOK_URL": "https://hooks.example.com/"},
		},
		{
			name: "nil input is nil-safe",
			in:   nil,
			want: map[string]string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RedactEnv(c.in, c.hints)
			if got == nil {
				t.Fatalf("RedactEnv returned nil; want empty map")
			}
			if c.want != nil {
				if len(got) != len(c.want) {
					t.Fatalf("len mismatch: got %v want %v", got, c.want)
				}
				for k, v := range c.want {
					if got[k] != v {
						t.Errorf("key %q: got %q want %q", k, got[k], v)
					}
				}
			}
			for k, sub := range c.mustHave {
				if !strings.Contains(got[k], sub) {
					t.Errorf("key %q value %q missing substring %q", k, got[k], sub)
				}
			}
			for _, m := range c.mustMiss {
				for k, v := range got {
					if strings.Contains(v, m) {
						t.Errorf("key %q value %q contained forbidden %q", k, v, m)
					}
				}
			}
		})
	}
}
