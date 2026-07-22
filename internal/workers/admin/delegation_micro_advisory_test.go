package admin

import (
	"strings"
	"testing"
)

// TestMicroDelegationAdvisory pins the break-even hint's firing conditions. The
// advisory is non-blocking signal, so the contract is purely "does it speak",
// and it must stay quiet unless the caller's OWN estimate says the slice is
// small AND there is no other reason (parallel fan-out, review) to delegate it.
func TestMicroDelegationAdvisory(t *testing.T) {
	cases := []struct {
		name     string
		in       DelegationInput
		wantFire bool
	}{
		{
			name:     "small single execute -> fires",
			in:       DelegationInput{BaselineTokensEstimate: 3000, Parallelism: 1, WorkerMode: "execute"},
			wantFire: true,
		},
		{
			name:     "just below threshold -> fires",
			in:       DelegationInput{BaselineTokensEstimate: microDelegationBreakEvenTokens - 1, WorkerMode: "execute"},
			wantFire: true,
		},
		{
			name:     "at threshold -> silent (break-even is not below break-even)",
			in:       DelegationInput{BaselineTokensEstimate: microDelegationBreakEvenTokens, WorkerMode: "execute"},
			wantFire: false,
		},
		{
			name:     "large estimate -> silent",
			in:       DelegationInput{BaselineTokensEstimate: 50000, WorkerMode: "execute"},
			wantFire: false,
		},
		{
			name:     "no estimate given -> silent (absent is not small)",
			in:       DelegationInput{BaselineTokensEstimate: 0, WorkerMode: "execute"},
			wantFire: false,
		},
		{
			name:     "negative estimate -> silent",
			in:       DelegationInput{BaselineTokensEstimate: -5, WorkerMode: "execute"},
			wantFire: false,
		},
		{
			name:     "small but parallel fan-out -> silent (throughput is the point)",
			in:       DelegationInput{BaselineTokensEstimate: 2000, Parallelism: 4, WorkerMode: "execute"},
			wantFire: false,
		},
		{
			name:     "small but review -> silent (second opinion, not token savings)",
			in:       DelegationInput{BaselineTokensEstimate: 2000, Parallelism: 1, WorkerMode: "review"},
			wantFire: false,
		},
		{
			name:     "small review, mixed case -> silent (case-insensitive)",
			in:       DelegationInput{BaselineTokensEstimate: 2000, WorkerMode: "Review"},
			wantFire: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := microDelegationAdvisory(tc.in)
			if (len(got) > 0) != tc.wantFire {
				t.Fatalf("microDelegationAdvisory fired=%v want=%v (msgs=%v)", len(got) > 0, tc.wantFire, got)
			}
			if tc.wantFire {
				if len(got) != 1 {
					t.Fatalf("expected exactly one advisory, got %d", len(got))
				}
				// The number the caller gave must appear in the message so the
				// advisory is self-explanatory, not a bare scold.
				if !strings.Contains(got[0], "break-even") {
					t.Errorf("advisory should name the break-even concept: %q", got[0])
				}
			}
		})
	}
}
