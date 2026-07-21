package gateway

import (
	"context"
	"encoding/json"
	"testing"
)

// TestParseAssigneeUserPrefix verifies that "user:<id>" is parsed as a
// human assignee and not mis-parsed as peer=user/agent=<id>.
func TestParseAssigneeUserPrefix(t *testing.T) {
	h := &handler{}
	tests := []struct {
		in       string
		wantUser string
	}{
		{"user:agent-a", "agent-a"},
		{"user:u-123abc", "u-123abc"},
	}
	for _, tc := range tests {
		raw, _ := json.Marshal(tc.in)
		got, err := h.parseAssignee(context.Background(), raw)
		if err != nil {
			t.Errorf("parseAssignee(%q): unexpected error: %v", tc.in, err)
			continue
		}
		if got == nil {
			t.Errorf("parseAssignee(%q) = nil, want non-nil", tc.in)
			continue
		}
		if got.UserID != tc.wantUser {
			t.Errorf("parseAssignee(%q): UserID = %q, want %q", tc.in, got.UserID, tc.wantUser)
		}
		if got.PeerID != "" {
			t.Errorf("parseAssignee(%q): PeerID = %q, want empty", tc.in, got.PeerID)
		}
	}
}

func TestParseAssigneeEmpty(t *testing.T) {
	h := &handler{}
	got, err := h.parseAssignee(context.Background(), nil)
	if err != nil {
		t.Fatalf("parseAssignee(nil): unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("parseAssignee(nil) = %+v, want nil", got)
	}
}

func TestParseAssigneeNullRawMessage(t *testing.T) {
	h := &handler{}
	got, err := h.parseAssignee(context.Background(), json.RawMessage("null"))
	if err != nil {
		t.Fatalf("parseAssignee(null): unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("parseAssignee(null) = %+v, want nil", got)
	}
}

func TestParseAssigneeEmptyString(t *testing.T) {
	h := &handler{}
	raw, _ := json.Marshal("")
	got, err := h.parseAssignee(context.Background(), raw)
	if err != nil {
		t.Fatalf("parseAssignee(\"\"): unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("parseAssignee(\"\") = %+v, want nil", got)
	}
}

func TestParseAssigneeUserColonEmptyID(t *testing.T) {
	h := &handler{}
	raw, _ := json.Marshal("user:")
	_, err := h.parseAssignee(context.Background(), raw)
	if err == nil {
		t.Fatal("parseAssignee(\"user:\"): expected error, got nil")
	}
}
