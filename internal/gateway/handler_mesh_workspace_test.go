package gateway

import (
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// TestDefaultMeshWorkspace_UnboundSessionsAreDirectoryScoped pins the fix for
// cross-workspace mesh cross-talk. Sessions that don't resolve to a registered
// workspace must each get a PRIVATE per-directory identity — never the shared
// "global" namespace, which previously merged every unbound session into one
// mesh and let agents in different repos read each other's messages + agent
// directory (and broadcast across paired peers, since p2p treats "global" as
// the broadcast sentinel).
func TestDefaultMeshWorkspace_UnboundSessionsAreDirectoryScoped(t *testing.T) {
	a := &sessionManager{clientPath: "/Users/dev/github/example/repo-a"}
	b := &sessionManager{clientPath: "/Users/dev/github/example/repo-b"}
	a2 := &sessionManager{clientPath: "/Users/dev/github/example/repo-a"}

	wa := defaultMeshWorkspace(a)
	wb := defaultMeshWorkspace(b)
	wa2 := defaultMeshWorkspace(a2)

	if wa == "global" || wb == "global" {
		t.Fatalf("unbound session with a client root must not fall into the shared \"global\" namespace: a=%q b=%q", wa, wb)
	}
	if wa == wb {
		t.Fatalf("sessions in different directories must be isolated, got identical workspace %q", wa)
	}
	if wa != wa2 {
		t.Fatalf("sessions in the same directory must share a workspace: %q != %q", wa, wa2)
	}
}

// TestDefaultMeshWorkspace_NoRootFallsBackToSession covers the daemon-socket
// case where the client never advertised a root: the session must isolate to
// itself rather than join the shared bucket.
func TestDefaultMeshWorkspace_NoRootFallsBackToSession(t *testing.T) {
	sm := &sessionManager{session: &store.Session{ID: "sess-xyz"}}
	got := defaultMeshWorkspace(sm)
	if got == "global" {
		t.Fatalf("rootless session must isolate to itself, not \"global\"; got %q", got)
	}
	if got != "session:sess-xyz" {
		t.Fatalf("rootless session workspace = %q, want session:sess-xyz", got)
	}
}
