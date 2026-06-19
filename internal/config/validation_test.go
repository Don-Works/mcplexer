package config

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateDownstreamServers covers the downstream_servers validation
// branches in validate(): required id, required tool_namespace, duplicate id,
// duplicate namespace, and invalid transport. These are the branches most
// likely to silently regress when fields are added/reordered, and only the
// audit_retention path had direct coverage before.
func TestValidateDownstreamServers(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantErr  bool
		wantMsgs []string // substrings that must appear in the ValidationError
	}{
		{
			name: "valid full config",
			yaml: `
downstream_servers:
  - id: a
    tool_namespace: ans
    transport: stdio
  - id: b
    tool_namespace: bns
    transport: http
`,
			wantErr: false,
		},
		{
			name: "valid empty transport defaults ok",
			yaml: `
downstream_servers:
  - id: a
    tool_namespace: ans
`,
			wantErr: false,
		},
		{
			name: "missing id",
			yaml: `
downstream_servers:
  - tool_namespace: ans
    transport: stdio
`,
			wantErr:  true,
			wantMsgs: []string{"id is required"},
		},
		{
			name: "missing tool_namespace",
			yaml: `
downstream_servers:
  - id: a
    transport: stdio
`,
			wantErr:  true,
			wantMsgs: []string{"tool_namespace is required"},
		},
		{
			name: "duplicate id",
			yaml: `
downstream_servers:
  - id: dup
    tool_namespace: ns1
    transport: stdio
  - id: dup
    tool_namespace: ns2
    transport: stdio
`,
			wantErr:  true,
			wantMsgs: []string{`duplicate id "dup"`},
		},
		{
			name: "duplicate namespace",
			yaml: `
downstream_servers:
  - id: a
    tool_namespace: shared
    transport: stdio
  - id: b
    tool_namespace: shared
    transport: stdio
`,
			wantErr:  true,
			wantMsgs: []string{`duplicate namespace "shared"`},
		},
		{
			name: "invalid transport",
			yaml: `
downstream_servers:
  - id: a
    tool_namespace: ans
    transport: carrier-pigeon
`,
			wantErr:  true,
			wantMsgs: []string{"invalid transport"},
		},
		{
			name: "log_rotation negatives rejected",
			yaml: `
log_rotation:
  max_size_mb: -1
  max_backups: -2
  max_age_days: -3
`,
			wantErr: true,
			wantMsgs: []string{
				"max_size_mb must be >= 0",
				"max_backups must be >= 0",
				"max_age_days must be >= 0",
			},
		},
		{
			name: "multiple errors accumulate",
			yaml: `
downstream_servers:
  - tool_namespace: ans
    transport: bogus
`,
			wantErr:  true,
			wantMsgs: []string{"id is required", "invalid transport"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if tc.wantErr && err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error %T (%v) is not a *ValidationError", err, err)
			}
			for _, msg := range tc.wantMsgs {
				if !strings.Contains(err.Error(), msg) {
					t.Errorf("error %q does not contain %q", err.Error(), msg)
				}
			}
		})
	}
}
