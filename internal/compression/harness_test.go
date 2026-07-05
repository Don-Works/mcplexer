package compression

import (
	"testing"
	"time"
)

// TestGimmickGate is the CI gate: every registered transform must satisfy all
// three hard constraints on the representative corpus, or the build fails.
func TestGimmickGate(t *testing.T) {
	metrics := RunGate(DefaultTransforms(), GateCorpus())
	if len(metrics) == 0 {
		t.Fatal("no transforms registered")
	}
	for _, m := range metrics {
		if m.Lossless && !m.LosslessOK {
			t.Errorf("%s: lossless transform altered a JSON value: %s", m.Transform, m.FirstViolation)
		}
		if !m.SecretSafe {
			t.Errorf("%s: dropped a must-survive token: %s", m.Transform, m.FirstViolation)
		}
		if !m.RecoverableOK {
			t.Errorf("%s: dropped content irreversibly (lossy without stash): %s", m.Transform, m.FirstViolation)
		}
		if m.MaxLatency > 100*time.Millisecond {
			t.Errorf("%s: exceeded per-payload latency budget: %v", m.Transform, m.MaxLatency)
		}
		// No-gimmick: a transform that changes payloads must show a real byte win.
		if m.Changed > 0 && m.TotalSavedBytes <= 0 {
			t.Errorf("%s: changed %d payloads but saved no bytes (gimmick)", m.Transform, m.Changed)
		}
	}
}

// TestGateJSONMinifyBehaviour pins the first transform's expected corpus
// behaviour: it must win on the pretty-printed fixtures, stay a no-op on
// already-compact / non-JSON payloads, and never touch a secret.
func TestGateJSONMinifyBehaviour(t *testing.T) {
	metrics := RunGate([]Transform{jsonMinify{}}, GateCorpus())
	m := metrics[0]
	if m.Changed == 0 {
		t.Fatal("json_minify saved nothing across the corpus — broken or a gimmick")
	}
	if m.TotalSavedBytes <= 0 {
		t.Errorf("json_minify net savings not positive: %d", m.TotalSavedBytes)
	}
	if !m.LosslessOK {
		t.Errorf("json_minify is not value-lossless: %s", m.FirstViolation)
	}
	if !m.SecretSafe {
		t.Errorf("json_minify mangled a secret: %s", m.FirstViolation)
	}
	// Exactly the 3 pretty/whitespace-heavy fixtures should change; the compact,
	// plain-text, and error fixtures must be untouched.
	if m.Changed != 3 {
		t.Errorf("json_minify changed %d fixtures, want 3 (the pretty ones only)", m.Changed)
	}
}
