package runner

import "testing"

// TestResolveMeshPriority pins the four-way contract for the mesh-output
// priority selector:
//
//  1. Status=success → ch.Priority is used (the legacy path).
//  2. Status=failure + priority_on_fail set → priority_on_fail wins.
//  3. Status=failure + priority_on_fail unset → ch.Priority preserved
//     (default behaviour unchanged for templates that don't opt in).
//  4. ch.Priority unset → "normal" floor.
//
// The legacy path (priority_on_fail unset) MUST continue to return the
// static priority regardless of run status, so every template shipped
// before this field landed keeps the exact same wire shape.
func TestResolveMeshPriority(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		priority string
		onFail   string
		status   string
		want     string
	}{
		// --- success path: priority_on_fail is irrelevant ---
		{"success uses priority", "low", "high", StatusSuccess, "low"},
		{"success ignores priority_on_fail", "normal", "critical", StatusSuccess, "normal"},
		{"success empty priority floors to normal", "", "high", StatusSuccess, "normal"},
		{"success empty both → normal", "", "", StatusSuccess, "normal"},

		// --- non-success path with priority_on_fail set: override wins ---
		{"failure uses priority_on_fail", "low", "high", StatusFailure, "high"},
		{"cap_exceeded uses priority_on_fail", "low", "high", StatusCapExceeded, "high"},
		{"awaiting_approval uses priority_on_fail", "normal", "critical", StatusAwaitingApproval, "critical"},
		{"rejected uses priority_on_fail", "low", "high", StatusRejected, "high"},

		// --- non-success path WITHOUT priority_on_fail: legacy behaviour ---
		{"failure no override falls back", "low", "", StatusFailure, "low"},
		{"cap_exceeded no override falls back", "normal", "", StatusCapExceeded, "normal"},
		{"failure empty priority + no override → normal", "", "", StatusFailure, "normal"},

		// --- edge: empty status (not expected in practice) defaults to
		// the priority path so we never accidentally promote a missing
		// status into the failure branch.
		{"empty status uses priority", "low", "high", "", "low"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ch := outputChannel{Priority: tc.priority, PriorityOnFail: tc.onFail}
			got := resolveMeshPriority(ch, tc.status)
			if got != tc.want {
				t.Errorf("resolveMeshPriority(priority=%q,on_fail=%q,status=%q) = %q, want %q",
					tc.priority, tc.onFail, tc.status, got, tc.want)
			}
		})
	}
}

// TestParseOutputChannels_PriorityOnFailRoundtrip confirms the new field
// survives the JSON round-trip used to decode worker.output_channels_json.
// Catches a class of regression where a future struct refactor drops the
// tag — the parser would silently ignore the field and the runner would
// fall back to the static priority on every failure.
func TestParseOutputChannels_PriorityOnFailRoundtrip(t *testing.T) {
	t.Parallel()
	in := `[{"type":"mesh","priority":"low","priority_on_fail":"high","tags":"bulletproof,nightly","to_peer":"workstation","broadcast_peers":true}]`
	channels, err := parseOutputChannels(in)
	if err != nil {
		t.Fatalf("parseOutputChannels: %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("len(channels) = %d, want 1", len(channels))
	}
	ch := channels[0]
	if ch.Priority != "low" {
		t.Errorf("Priority = %q, want %q", ch.Priority, "low")
	}
	if ch.PriorityOnFail != "high" {
		t.Errorf("PriorityOnFail = %q, want %q", ch.PriorityOnFail, "high")
	}
	if ch.ToPeer != "workstation" {
		t.Errorf("ToPeer = %q, want %q", ch.ToPeer, "workstation")
	}
	if !ch.BroadcastPeers {
		t.Error("BroadcastPeers = false, want true")
	}
}
