package gateway

import (
	"encoding/json"
	"testing"
)

func TestScopePolicy_EmptyAllowsEverything(t *testing.T) {
	p, err := NewScopePolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Enabled() {
		t.Fatal("empty policy should not be enabled")
	}
	extracted := map[string]map[string]struct{}{
		"org":  {"evil": {}},
		"repo": {"evil/secret": {}},
	}
	if err := p.Enforce(extracted); err != nil {
		t.Fatalf("empty policy should allow everything: %v", err)
	}
}

func TestScopePolicy_EmptyObjectAllowsEverything(t *testing.T) {
	p, err := NewScopePolicy(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Enabled() {
		t.Fatal("empty object policy should not be enabled")
	}
}

func TestScopePolicy_SingleResourceType(t *testing.T) {
	p, err := NewScopePolicy(json.RawMessage(`{"org": ["acme"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enabled() {
		t.Fatal("policy with constraints should be enabled")
	}

	tests := []struct {
		name      string
		extracted map[string]map[string]struct{}
		wantErr   bool
	}{
		{
			name:      "allowed org",
			extracted: map[string]map[string]struct{}{"org": {"acme": {}}},
			wantErr:   false,
		},
		{
			name:      "denied org",
			extracted: map[string]map[string]struct{}{"org": {"evil": {}}},
			wantErr:   true,
		},
		{
			name:      "unconstrained type passes",
			extracted: map[string]map[string]struct{}{"repo": {"evil/secret": {}}},
			wantErr:   false,
		},
		{
			name:      "no extracted values passes",
			extracted: map[string]map[string]struct{}{},
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.Enforce(tt.extracted)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestScopePolicy_MultipleResourceTypes(t *testing.T) {
	p, err := NewScopePolicy(json.RawMessage(`{
		"org": ["acme"],
		"repo": ["acme/api", "acme/web"]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		extracted map[string]map[string]struct{}
		wantErr   bool
	}{
		{
			name: "both allowed",
			extracted: map[string]map[string]struct{}{
				"org":  {"acme": {}},
				"repo": {"acme/api": {}},
			},
			wantErr: false,
		},
		{
			name: "org allowed, repo denied",
			extracted: map[string]map[string]struct{}{
				"org":  {"acme": {}},
				"repo": {"acme/secret": {}},
			},
			wantErr: true,
		},
		{
			name: "org denied",
			extracted: map[string]map[string]struct{}{
				"org": {"evil": {}},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.Enforce(tt.extracted)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestScopePolicy_CaseInsensitive(t *testing.T) {
	p, err := NewScopePolicy(json.RawMessage(`{"org": ["Acme"]}`))
	if err != nil {
		t.Fatal(err)
	}

	// "Acme" should be normalised to "acme" during parsing.
	extracted := map[string]map[string]struct{}{"org": {"acme": {}}}
	if err := p.Enforce(extracted); err != nil {
		t.Fatalf("case-insensitive match should pass: %v", err)
	}
}

func TestScopePolicy_InvalidJSON(t *testing.T) {
	_, err := NewScopePolicy(json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected parse error for invalid JSON")
	}

	_, err = NewScopePolicy(json.RawMessage(`["array"]`))
	if err == nil {
		t.Fatal("expected parse error for non-object")
	}
}

func TestScopeRegistry_Get(t *testing.T) {
	r := NewScopeRegistry()
	r.Register("github", GitHubExtractor{})

	if r.Get("github__create_issue") == nil {
		t.Fatal("expected extractor for github__ tool")
	}
	if r.Get("slack__post_message") != nil {
		t.Fatal("expected nil for unregistered namespace")
	}
	if r.Get("no_namespace") != nil {
		t.Fatal("expected nil for tool without namespace")
	}
}

func TestValidateScopePolicy(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty", "", false},
		{"empty object", "{}", false},
		{"valid", `{"org": ["acme"]}`, false},
		{"invalid json", "bad", true},
		{"non-object", `["array"]`, true},
		{"empty key", `{"": ["acme"]}`, true},
		{"empty value", `{"org": [""]}`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateScopePolicy(json.RawMessage(tt.input))
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
