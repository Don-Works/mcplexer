package runner

import (
	"fmt"
	"strings"
	"testing"
)

func TestReadDelegationIsolationPolicyRejectsFalseConstraintMetadata(t *testing.T) {
	tests := []struct {
		name string
		meta string
		want string
	}{
		{
			name: "none with claims",
			meta: `{"id":"d","worker_isolation":"none","touches_files":["internal/a.go"]}`,
			want: "requires worker_isolation=worktree",
		},
		{
			name: "unknown worker mode",
			meta: `{"id":"d","worker_isolation":"worktree","worker_mode":"write-anything"}`,
			want: "unsupported delegation worker mode",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readDelegationIsolationPolicy(`{"_mcplexer_delegation":` + tc.meta + `}`)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestReadDelegationIsolationPolicyRejectsExcessClaims(t *testing.T) {
	claims := make([]string, maxDelegationClaims+1)
	for i := range claims {
		claims[i] = fmt.Sprintf(`"file-%d"`, i)
	}
	parameters := `{"_mcplexer_delegation":{"id":"d","worker_isolation":"worktree","touches_files":[` + strings.Join(claims, ",") + `]}}`
	_, err := readDelegationIsolationPolicy(parameters)
	if err == nil || !strings.Contains(err.Error(), "max 256") {
		t.Fatalf("error = %v, want claim cap", err)
	}
}

func TestReadDelegationIsolationPolicyLegacyEmptyModeIsExecute(t *testing.T) {
	policy, err := readDelegationIsolationPolicy(`{"_mcplexer_delegation":{"id":"d","worker_isolation":"worktree"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.required() || policy.reviewOnly {
		t.Fatalf("policy = %+v, want isolated execute", policy)
	}
}
