package gateway

import (
	"encoding/json"
	"testing"
)

// TestWorkspaceHasTag pins the workspace-tag matching used by the
// admin-trusted gate. This is a load-bearing security feature: a matching
// tag grants full admin access bypassing the CWD gate, so the cases that
// MUST NOT match (empty, malformed JSON) are as important as the ones that do.
func TestWorkspaceHasTag(t *testing.T) {
	tests := []struct {
		name string
		tags json.RawMessage
		want string
		ok   bool
	}{
		{"nil tags", nil, adminTrustedTag, false},
		{"empty tags", json.RawMessage(``), adminTrustedTag, false},
		{"empty array", json.RawMessage(`[]`), adminTrustedTag, false},
		{"present exact", json.RawMessage(`["admin-trusted"]`), adminTrustedTag, true},
		{"present among others", json.RawMessage(`["telegram","admin-trusted","concierge"]`), adminTrustedTag, true},
		{"absent", json.RawMessage(`["telegram","concierge"]`), adminTrustedTag, false},
		{"case-insensitive upper", json.RawMessage(`["ADMIN-TRUSTED"]`), adminTrustedTag, true},
		{"case-insensitive mixed", json.RawMessage(`["Admin-Trusted"]`), adminTrustedTag, true},
		{"surrounding whitespace trimmed", json.RawMessage(`["  admin-trusted  "]`), adminTrustedTag, true},
		// Malformed JSON must fall through as "no match" — a corrupted tags
		// column can NOT be allowed to accidentally grant trust.
		{"malformed json object", json.RawMessage(`{"admin-trusted":true}`), adminTrustedTag, false},
		{"malformed json truncated", json.RawMessage(`["admin-trusted"`), adminTrustedTag, false},
		{"malformed not array", json.RawMessage(`"admin-trusted"`), adminTrustedTag, false},
		{"malformed garbage", json.RawMessage(`not json at all`), adminTrustedTag, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := workspaceHasTag(tt.tags, tt.want); got != tt.ok {
				t.Errorf("workspaceHasTag(%s, %q) = %v, want %v",
					string(tt.tags), tt.want, got, tt.ok)
			}
		})
	}
}

// TestResolveChain_AdminTrusted asserts resolveChainForPath sets sm.adminTrusted
// exactly when a workspace in the resolved chain (path-ancestor OR parent-chain)
// carries the admin-trusted tag, and leaves it false otherwise. The parent-chain
// case is critical: a trusted PARENT workspace (reachable only via parent_id,
// not as a path ancestor) must still grant trust.
func TestResolveChain_AdminTrusted(t *testing.T) {
	trusted := json.RawMessage(`["admin-trusted"]`)
	untrusted := json.RawMessage(`["telegram"]`)

	tests := []struct {
		name       string
		workspaces []mockWorkspace
		clientRoot string
		want       bool
	}{
		{
			name: "no tags anywhere",
			workspaces: []mockWorkspace{
				{id: "ws", rootPath: "/code/proj"},
			},
			clientRoot: "/code/proj/src",
			want:       false,
		},
		{
			name: "path-ancestor carries tag",
			workspaces: []mockWorkspace{
				{id: "ws", rootPath: "/code/proj", tags: trusted},
			},
			clientRoot: "/code/proj/src",
			want:       true,
		},
		{
			name: "non-matching tag does not grant",
			workspaces: []mockWorkspace{
				{id: "ws", rootPath: "/code/proj", tags: untrusted},
			},
			clientRoot: "/code/proj/src",
			want:       false,
		},
		{
			name: "parent-chain workspace carries tag",
			workspaces: []mockWorkspace{
				// child is the path ancestor; the trusted tag lives on the
				// parent which is reachable ONLY via parent_id.
				{id: "child", rootPath: "/code/proj", parentID: "org"},
				{id: "org", rootPath: "", tags: trusted},
			},
			clientRoot: "/code/proj/src",
			want:       true,
		},
		{
			name: "unrelated trusted workspace does not leak trust",
			workspaces: []mockWorkspace{
				{id: "ws", rootPath: "/code/proj"},
				{id: "other", rootPath: "/elsewhere", tags: trusted},
			},
			clientRoot: "/code/proj/src",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := &sessionManager{store: &mockStore{workspaces: tt.workspaces}}
			sm.resolveChainForPath(t.Context(), tt.clientRoot)
			if got := sm.isAdminTrusted(); got != tt.want {
				t.Errorf("adminTrusted = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestResolveChain_AdminTrustedResets verifies the flag is recomputed (not
// sticky) on each resolve: a session that was trusted must drop trust if the
// workspace set no longer carries the tag.
func TestResolveChain_AdminTrustedResets(t *testing.T) {
	store := &mockStore{
		workspaces: []mockWorkspace{
			{id: "ws", rootPath: "/code/proj", tags: json.RawMessage(`["admin-trusted"]`)},
		},
	}
	sm := &sessionManager{store: store}

	sm.resolveChainForPath(t.Context(), "/code/proj")
	if !sm.isAdminTrusted() {
		t.Fatal("expected adminTrusted after resolving a trusted workspace")
	}

	// Drop the tag and re-resolve; trust must be recomputed to false.
	store.workspaces[0].tags = nil
	sm.resolveChainForPath(t.Context(), "/code/proj")
	if sm.isAdminTrusted() {
		t.Error("adminTrusted stayed true after the tag was removed; flag is sticky")
	}
}
