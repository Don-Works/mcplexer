package taskstatus

import "testing"

func TestNormalize(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"open", "open"},
		{"  Open  ", "open"},
		{"IN_PROGRESS", "in_progress"},
	}
	for _, tt := range tests {
		if got := Normalize(tt.in); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDefaultKind(t *testing.T) {
	tests := []struct {
		status string
		kind   string
		ok     bool
	}{
		{"open", KindOpen, true},
		{"doing", KindWorking, true},
		{"in-progress", KindWorking, true},
		{"blocked", KindBlocked, true},
		{"review", KindReview, true},
		{"done", KindDone, true},
		{"cancelled", KindCancelled, true},
		{"abandoned", KindCancelled, true},
		{"wontfix", KindCancelled, true},
		{"unknown", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		kind, ok := DefaultKind(tt.status)
		if ok != tt.ok || kind != tt.kind {
			t.Errorf("DefaultKind(%q) = (%q, %v), want (%q, %v)", tt.status, kind, ok, tt.kind, tt.ok)
		}
	}
}

func TestIsValidKind(t *testing.T) {
	valid := []string{KindOpen, KindWorking, KindBlocked, KindReview, KindDone, KindCancelled}
	for _, k := range valid {
		if !IsValidKind(k) {
			t.Errorf("IsValidKind(%q) = false, want true", k)
		}
	}
	invalid := []string{"", "unknown", "pending", "Open"}
	for _, k := range invalid {
		if IsValidKind(k) {
			t.Errorf("IsValidKind(%q) = true, want false", k)
		}
	}
}

func TestIsTerminalKind(t *testing.T) {
	terminal := []string{KindDone, KindCancelled}
	for _, k := range terminal {
		if !IsTerminalKind(k) {
			t.Errorf("IsTerminalKind(%q) = false, want true", k)
		}
	}
	nonTerminal := []string{KindOpen, KindWorking, KindBlocked, KindReview}
	for _, k := range nonTerminal {
		if IsTerminalKind(k) {
			t.Errorf("IsTerminalKind(%q) = true, want false", k)
		}
	}
}

func TestTerminalDefaultStatuses(t *testing.T) {
	statuses := TerminalDefaultStatuses()
	if len(statuses) == 0 {
		t.Fatal("TerminalDefaultStatuses() returned empty")
	}
	for _, s := range statuses {
		kind, ok := DefaultKind(s)
		if !ok {
			t.Errorf("TerminalDefaultStatuses contains %q but DefaultKind has no entry", s)
		}
		if !IsTerminalKind(kind) {
			t.Errorf("TerminalDefaultStatuses contains %q (kind=%q) which is not terminal", s, kind)
		}
	}
}

func TestDefaultKindMap(t *testing.T) {
	m := DefaultKindMap()
	if len(m) != len(DefaultKinds) {
		t.Errorf("DefaultKindMap() has %d entries, want %d", len(m), len(DefaultKinds))
	}
	for status, kind := range DefaultKinds {
		if m[status] != kind {
			t.Errorf("DefaultKindMap()[%q] = %q, want %q", status, m[status], kind)
		}
	}
}
