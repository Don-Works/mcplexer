// work_context_test.go — round-trip + validation coverage for the
// frontmatter-on-meta work-context layer. Critical guarantees under
// test: Parse + Merge preserves non-work-context lines verbatim, Merge
// is idempotent over identical patches, and the validators reject
// junk inputs at parse time.
package tasks_test

import (
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/tasks"
)

func TestParseWorkContextEmptyMeta(t *testing.T) {
	got, err := tasks.ParseWorkContext("")
	if err != nil {
		t.Fatalf("Parse empty: %v", err)
	}
	if (got != tasks.WorkContext{}) {
		t.Fatalf("expected zero value, got %+v", got)
	}
}

func TestParseWorkContextExtractsAllKeys(t *testing.T) {
	meta := strings.Join([]string{
		"composed_by: 01ABCPARENT",
		"worktree: /Users/example/worktrees/feat-x",
		"branch: feat/x",
		"pr: https://github.com/me/repo/pull/42",
		"commits: 1234567..89abcde",
		"peer: QmYwAPJzv5CZsnA625s3Xf2nemtYgPpHdWEz79ojWnPbdG",
		"session: dashboard-9f9f9f9f",
		"linear: ENG-123",
		"mesh_thread: 01THREADROOT",
		"some_custom: keep-me",
	}, "\n")
	wc, err := tasks.ParseWorkContext(meta)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if wc.Worktree != "/Users/example/worktrees/feat-x" {
		t.Errorf("worktree: got %q", wc.Worktree)
	}
	if wc.Branch != "feat/x" {
		t.Errorf("branch: got %q", wc.Branch)
	}
	if wc.PR != "https://github.com/me/repo/pull/42" {
		t.Errorf("pr: got %q", wc.PR)
	}
	if wc.Commits != "1234567..89abcde" {
		t.Errorf("commits: got %q", wc.Commits)
	}
	if wc.Peer != "QmYwAPJzv5CZsnA625s3Xf2nemtYgPpHdWEz79ojWnPbdG" {
		t.Errorf("peer: got %q", wc.Peer)
	}
	if wc.Session != "dashboard-9f9f9f9f" {
		t.Errorf("session: got %q", wc.Session)
	}
	if wc.Linear != "ENG-123" {
		t.Errorf("linear: got %q", wc.Linear)
	}
	if wc.MeshThread != "01THREADROOT" {
		t.Errorf("mesh_thread: got %q", wc.MeshThread)
	}
}

// As of migration 072 the canonical meta shape is a JSON object, not
// frontmatter — so these merge assertions check semantic round-trips
// (ParseWorkContext + MetaListGet) rather than raw substrings. The
// dual-read path means legacy frontmatter on the LHS of a merge still
// works, but the output always comes out as JSON.
func TestMergeWorkContextPreservesOtherLines(t *testing.T) {
	meta := strings.Join([]string{
		"composed_by: 01PARENT",
		"composes: 01CHILD",
		"some_custom: keep-me",
	}, "\n")
	patch := tasks.WorkContext{
		Branch: "feat/work-context",
		PR:     "https://github.com/foo/bar/pull/9",
	}
	out, err := tasks.MergeWorkContext(meta, patch)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if got := tasks.MetaListGet(out, "composed_by"); len(got) != 1 || got[0] != "01PARENT" {
		t.Errorf("composed_by lost: got %v\n meta=%q", got, out)
	}
	if got := tasks.MetaListGet(out, "composes"); len(got) != 1 || got[0] != "01CHILD" {
		t.Errorf("composes lost: got %v\n meta=%q", got, out)
	}
	if got := tasks.MetaListGet(out, "some_custom"); len(got) != 1 || got[0] != "keep-me" {
		t.Errorf("custom key dropped: got %v\n meta=%q", got, out)
	}
	wc, err := tasks.ParseWorkContext(out)
	if err != nil {
		t.Fatalf("ParseWorkContext: %v", err)
	}
	if wc.Branch != "feat/work-context" {
		t.Errorf("branch missing: %+v\n meta=%q", wc, out)
	}
	if wc.PR != "https://github.com/foo/bar/pull/9" {
		t.Errorf("pr missing: %+v\n meta=%q", wc, out)
	}
}

func TestMergeWorkContextUpdatesExistingLineInPlace(t *testing.T) {
	meta := strings.Join([]string{
		"worktree: /old/path",
		"composes: 01CHILD",
	}, "\n")
	patch := tasks.WorkContext{Worktree: "/new/path"}
	out, err := tasks.MergeWorkContext(meta, patch)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if strings.Contains(out, "/old/path") {
		t.Errorf("old worktree leaked through: %s", out)
	}
	wc, err := tasks.ParseWorkContext(out)
	if err != nil {
		t.Fatalf("ParseWorkContext: %v", err)
	}
	if wc.Worktree != "/new/path" {
		t.Errorf("new worktree missing: %+v\n meta=%q", wc, out)
	}
	// composes line must remain intact (preserved through the merge).
	if got := tasks.MetaListGet(out, "composes"); len(got) != 1 || got[0] != "01CHILD" {
		t.Errorf("composes dropped during in-place rewrite: got %v\n meta=%q", got, out)
	}
}

func TestMergeWorkContextRoundTrip(t *testing.T) {
	patch := tasks.WorkContext{
		Worktree: "/Users/example/wt/x",
		Branch:   "feat/x",
		PR:       "https://github.com/me/repo/pull/1",
		Commits:  "abcdef0..1234567",
		Linear:   "ENG-42",
	}
	meta, err := tasks.MergeWorkContext("composed_by: 01PARENT\n", patch)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	parsed, err := tasks.ParseWorkContext(meta)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed != patch {
		t.Fatalf("round-trip mismatch:\nwrote: %+v\n read: %+v\nmeta: %q", patch, parsed, meta)
	}
}

func TestMergeWorkContextIdempotent(t *testing.T) {
	patch := tasks.WorkContext{Branch: "feat/x", PR: "https://github.com/me/repo/pull/2"}
	first, err := tasks.MergeWorkContext("", patch)
	if err != nil {
		t.Fatalf("first Merge: %v", err)
	}
	second, err := tasks.MergeWorkContext(first, patch)
	if err != nil {
		t.Fatalf("second Merge: %v", err)
	}
	if first != second {
		t.Fatalf("not idempotent:\nfirst:  %q\nsecond: %q", first, second)
	}
}

func TestParseWorkContextRejectsInvalidPRUrl(t *testing.T) {
	cases := []string{
		"pr: PR-123",
		"pr: not a url",
		"pr: ftp://example.com/x",
		"pr: /relative/path",
	}
	for _, meta := range cases {
		_, err := tasks.ParseWorkContext(meta)
		if err == nil {
			t.Errorf("expected error for meta=%q", meta)
		}
	}
}

func TestParseWorkContextRejectsBadPeerLength(t *testing.T) {
	cases := []string{
		"peer: short",
		"peer: " + strings.Repeat("x", 30),
		"peer: " + strings.Repeat("x", 60),
	}
	for _, meta := range cases {
		_, err := tasks.ParseWorkContext(meta)
		if err == nil {
			t.Errorf("expected error for meta=%q", meta)
		}
	}
}

func TestParseWorkContextRejectsBadCommitsRange(t *testing.T) {
	cases := []string{
		"commits: not-a-range",
		"commits: abc..def",         // too short
		"commits: 1234567",          // no range
		"commits: ZZZZZZZ..ABCDEF1", // non-hex
	}
	for _, meta := range cases {
		_, err := tasks.ParseWorkContext(meta)
		if err == nil {
			t.Errorf("expected error for meta=%q", meta)
		}
	}
}

func TestMergeWorkContextAcceptsValidPeerAndCommits(t *testing.T) {
	// Sanity check: the validators don't reject legitimate inputs.
	patch := tasks.WorkContext{
		Commits: "1234567...89abcdef",
		Peer:    "QmYwAPJzv5CZsnA625s3Xf2nemtYgPpHdWEz79ojWnPbdG",
	}
	if _, err := tasks.MergeWorkContext("", patch); err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
}
