package store

import "testing"

// TestConditionalTerminalPhrase covers the trap that produced the measured
// invoice-sync case: a job whose only OBSERVABLE terminal line is the branch it
// takes when there is nothing to do.
func TestConditionalTerminalPhrase(t *testing.T) {
	tests := []struct {
		name    string
		masked  string
		refused bool
	}{
		// --- must be refused ---
		{
			name: "the measured prod case",
			// The real shape: 'finished scheduled job for invoice sync' has
			// never once appeared in seven days, because this branch always
			// wins. Learning THIS line inverts the alarm.
			masked:  "no invoices to send for <n> accounts",
			refused: true,
		},
		{name: "nothing to do", masked: "sync tick complete, nothing to do", refused: true},
		{name: "no pending work", masked: "worker <n>: no pending work", refused: true},
		{name: "no rows found", masked: "reconcile: no rows found in <dur>", refused: true},
		{name: "no orders to process", masked: "no orders to process", refused: true},
		{name: "skipping", masked: "skipping run, lock held by <host>", refused: true},
		{name: "already up to date", masked: "catalogue already up to date", refused: true},
		{name: "empty queue", masked: "dispatcher: empty queue, sleeping <dur>", refused: true},
		{name: "case and spacing are ignored", masked: "  NO   Invoices  To  Send ", refused: true},

		// --- must NOT be refused: these are genuine unconditional terminals ---
		{name: "the order sync completion", masked: "order sync completed batch=<n> in <dur>"},
		{name: "counts are fine", masked: "processed <n> orders in <dur>"},
		{name: "plain success", masked: "finished scheduled job for invoice sync"},
		{name: "notify is not nothing", masked: "notified <n> subscribers"},
		{name: "north is not no", masked: "northbound replication ok <dur>"},
		{name: "nonce is not no", masked: "nonce rotated for <uuid>"},
		{name: "empty masked", masked: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			phrase := ConditionalTerminalPhrase(tt.masked)
			if tt.refused && phrase == "" {
				t.Fatalf("ConditionalTerminalPhrase(%q) = \"\"; this line asserts no work "+
					"was done and must be refused as a success observable", tt.masked)
			}
			if !tt.refused && phrase != "" {
				t.Fatalf("ConditionalTerminalPhrase(%q) = %q; a genuine completion line "+
					"was refused, which costs real coverage", tt.masked, phrase)
			}
		})
	}
}

// TestEvaluateBaselineCandidateRefusesConditionalTerminal proves the refusal is
// reached from the promotion ladder, not just available as a helper — and that
// it beats the statistics. The candidate here is a TEXTBOOK cadence: perfectly
// periodic, continuous, a month of clean day history. Every number says promote.
// Only the text says otherwise, and the text must win.
func TestEvaluateBaselineCandidateRefusesConditionalTerminal(t *testing.T) {
	c := promotableCandidate()
	if v := EvaluateBaselineCandidate(c); v.Decision != BaselinePromoted {
		t.Fatalf("fixture is wrong: baseline candidate is %q, not promoted", v.Decision)
	}

	c.Masked = "no invoices to send for <n> accounts"
	c.MatchSubstring = "no invoices to send for"
	v := EvaluateBaselineCandidate(c)

	if v.Decision != BaselineRejectConditionalTerminal {
		t.Fatalf("decision = %q; want conditional_terminal — the statistics of a job "+
			"that reliably has nothing to do are indistinguishable from a job that "+
			"reliably works, so the ladder must refuse on the text", v.Decision)
	}
	if v.Window != 0 {
		t.Errorf("refused candidate proposed a %s window; want 0", v.Window)
	}
	if v.Reason == "" {
		t.Error("a refusal with no reason is the shrug this feature exists to avoid")
	}
}
