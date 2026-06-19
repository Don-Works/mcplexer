package peerscope

import "testing"

func TestFindByPrefix_Boolean(t *testing.T) {
	d := FindByPrefix("mesh.memory_request")
	if d == nil {
		t.Fatalf("expected match for mesh.memory_request")
	}
	if d.ResourceKind != "" {
		t.Fatalf("expected boolean (no resource kind), got %q", d.ResourceKind)
	}
}

func TestFindByPrefix_ColonPrefix(t *testing.T) {
	cases := []struct {
		scope, wantPrefix string
	}{
		{"trigger_worker:foo", "trigger_worker:"},
		{"trigger_worker:*", "trigger_worker:"},
		{"task_offer:personal", "task_offer:"},
		{"task_assign:*", "task_assign:"},
	}
	for _, c := range cases {
		d := FindByPrefix(c.scope)
		if d == nil {
			t.Errorf("expected match for %q", c.scope)
			continue
		}
		if d.Prefix != c.wantPrefix {
			t.Errorf("scope %q matched prefix %q, want %q", c.scope, d.Prefix, c.wantPrefix)
		}
	}
}

func TestFindByPrefix_NoMatch(t *testing.T) {
	if d := FindByPrefix("totally_unknown_scope"); d != nil {
		t.Fatalf("expected no match, got %+v", d)
	}
	// trigger_worker: alone (no resource) shouldn't match the colon-prefix shape.
	if d := FindByPrefix("trigger_worker:"); d != nil {
		t.Fatalf("expected no match for bare prefix, got %+v", d)
	}
}

func TestKnown_AllPrefixesUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range Known {
		if seen[d.Prefix] {
			t.Errorf("duplicate prefix in Known: %q", d.Prefix)
		}
		seen[d.Prefix] = true
	}
}

func TestKnown_SeverityValid(t *testing.T) {
	valid := map[string]bool{"low": true, "medium": true, "high": true}
	for _, d := range Known {
		if !valid[d.Severity] {
			t.Errorf("scope %q has invalid severity %q (want low|medium|high)", d.Prefix, d.Severity)
		}
	}
}
