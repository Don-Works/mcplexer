package store

import "testing"

// TestCadenceNormalizeCollapsesReleaseVolatileAtoms pins both halves of the
// contract: line numbers stop splitting a job's history at every deploy, and
// nothing else about the message text moves.
func TestCadenceNormalizeCollapsesReleaseVolatileAtoms(t *testing.T) {
	tests := []struct {
		name   string
		masked string
		want   string
	}{
		{
			name:   "a go code location loses only its line number",
			masked: "order sync completed batch=<n> ordersync.go:142",
			want:   "order sync completed batch=<n> ordersync.go:<line>",
		},
		{
			name:   "a pathed location keeps its path",
			masked: "flush failed internal/jobs/sync.go:87",
			want:   "flush failed internal/jobs/sync.go:<line>",
		},
		{
			name:   "other languages are covered too",
			masked: "worker tick app/jobs/sync.rb:12 done",
			want:   "worker tick app/jobs/sync.rb:<line> done",
		},
		{
			name:   "a bare number is not a code location",
			masked: "processed <n> orders in <dur>",
			want:   "processed <n> orders in <dur>",
		},
		{
			name:   "a host:port is not a code location",
			masked: "dial <ip> failed after <dur>",
			want:   "dial <ip> failed after <dur>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CadenceNormalize(tt.masked); got != tt.want {
				t.Errorf("CadenceNormalize(%q) = %q; want %q", tt.masked, got, tt.want)
			}
		})
	}
}

// TestCadenceKeyMergesReleasesNotDistinctJobs is the safety check on the
// redeploy fix. Collapsing a line number must reunite ONE job across releases
// without ever merging two genuinely different templates — including the
// dangerous near-miss of two messages that share a long prefix.
func TestCadenceKeyMergesReleasesNotDistinctJobs(t *testing.T) {
	const src = "src-1"
	key := func(masked string) string { return CadenceKey(src, masked) }

	before := key("order sync completed batch=<n> ordersync.go:142")
	after := key("order sync completed batch=<n> ordersync.go:151")
	if before != after {
		t.Error("a redeploy that only shifted the line number split the cadence history; " +
			"this is the defect that stopped a rule ever bootstrapping between releases")
	}

	distinct := map[string]string{
		"a different message in the same file": "order sync FAILED batch=<n> ordersync.go:142",
		"a shared prefix, different tail":      "order sync completed batch=<n> retried ordersync.go:142",
		"the same message in another file":     "order sync completed batch=<n> billing.go:142",
	}
	for name, masked := range distinct {
		t.Run(name, func(t *testing.T) {
			if key(masked) == before {
				t.Errorf("%q merged into the order-sync cadence; distinct jobs must stay distinct",
					masked)
			}
		})
	}

	// Identity is per source: two hosts running the same job are separate
	// cadences, because one can stop while the other keeps running.
	if CadenceKey("src-2", "order sync completed batch=<n> ordersync.go:142") == before {
		t.Error("cadence key ignored the source; two hosts must not share one baseline")
	}
}

// TestDeriveMatchSubstringSkipsLineNumbers guards the promoted rule itself. A
// matcher containing a code location would stop matching at the next release
// and raise an absence alert forever.
func TestDeriveMatchSubstringSkipsLineNumbers(t *testing.T) {
	masked := "order sync completed batch=<n> ordersync.go:142"
	got := DeriveMatchSubstring(CadenceNormalize(masked))
	if got != "order sync completed batch=" {
		t.Fatalf("matcher = %q; want the literal run with no line number in it", got)
	}
}
