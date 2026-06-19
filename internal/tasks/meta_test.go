package tasks_test

import (
	"testing"

	"github.com/don-works/mcplexer/internal/tasks"
)

// TestMetaIsLegacyFrontmatter exercises the dual-read discriminator.
// Empty + JSON-shaped inputs are NOT legacy; frontmatter is.
func TestMetaIsLegacyFrontmatter(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"{}", false},
		{"  \n\n{\"composed_by\":\"01EPIC\"}", false},
		{"composed_by: 01PARENT", true},
		{"worktree: /tmp\nbranch: main", true},
	}
	for _, c := range cases {
		if got := tasks.MetaIsLegacyFrontmatter(c.in); got != c.want {
			t.Errorf("MetaIsLegacyFrontmatter(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestMetaToJSONFrontmatterRoundTrip — the dominant migration path.
// Round-trips every shape the post-072 backfill cares about.
func TestMetaToJSONFrontmatterRoundTrip(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"single-scalar", "composed_by: 01PARENT", `{"composed_by":"01PARENT"}`},
		{"multi-element list", "composes: 01A, 01B, 01C", `{"composes":["01A","01B","01C"]}`},
		{"multi-key", "branch: main\nworktree: /tmp", `{"branch":"main","worktree":"/tmp"}`},
		{"keys-with-commas-and-spaces", "composed_by: 01P\ncomposes: 01A,01B", `{"composed_by":"01P","composes":["01A","01B"]}`},
		{"json-passthrough", `{"a":"b"}`, `{"a":"b"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := tasks.MetaToJSON(c.in)
			if err != nil {
				t.Fatalf("MetaToJSON: %v", err)
			}
			if got != c.want {
				t.Fatalf("\n in: %q\nwant: %q\n got: %q", c.in, c.want, got)
			}
		})
	}
}

// TestMetaListGetDualRead — same key reads identically whether the
// row is in legacy frontmatter or JSON form.
func TestMetaListGetDualRead(t *testing.T) {
	legacy := "composed_by: 01P, 01Q\nworktree: /a"
	canonical := `{"composed_by":["01P","01Q"],"worktree":"/a"}`
	if got := tasks.MetaListGet(legacy, "composed_by"); len(got) != 2 || got[0] != "01P" || got[1] != "01Q" {
		t.Errorf("legacy multi: got %v", got)
	}
	if got := tasks.MetaListGet(canonical, "composed_by"); len(got) != 2 || got[0] != "01P" || got[1] != "01Q" {
		t.Errorf("canonical multi: got %v", got)
	}
	if got := tasks.MetaListGet(legacy, "worktree"); len(got) != 1 || got[0] != "/a" {
		t.Errorf("legacy scalar: got %v", got)
	}
	if got := tasks.MetaListGet(canonical, "worktree"); len(got) != 1 || got[0] != "/a" {
		t.Errorf("canonical scalar: got %v", got)
	}
	if got := tasks.MetaListGet(legacy, "missing"); got != nil {
		t.Errorf("missing key: got %v", got)
	}
}

// TestMetaListAppend covers the three regimes: empty meta, fresh key,
// existing scalar promoted to array, idempotent re-append.
func TestMetaListAppend(t *testing.T) {
	cases := []struct {
		name, in, key, val, want string
	}{
		{"empty", "", "composed_by", "01P", `{"composed_by":"01P"}`},
		{"existing-scalar-promoted", `{"composed_by":"01P"}`, "composed_by", "01Q", `{"composed_by":["01P","01Q"]}`},
		{"append-to-array", `{"composed_by":["01P","01Q"]}`, "composed_by", "01R", `{"composed_by":["01P","01Q","01R"]}`},
		{"idempotent-scalar", `{"composed_by":"01P"}`, "composed_by", "01P", `{"composed_by":"01P"}`},
		{"idempotent-array", `{"composed_by":["01P","01Q"]}`, "composed_by", "01Q", `{"composed_by":["01P","01Q"]}`},
		// Legacy input on read path; output is always JSON.
		{"legacy-input", "composed_by: 01P", "composed_by", "01Q", `{"composed_by":["01P","01Q"]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := tasks.MetaListAppend(c.in, c.key, c.val)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != c.want {
				t.Fatalf("\nin:   %q\nkey:  %q val: %q\nwant: %q\ngot:  %q", c.in, c.key, c.val, c.want, got)
			}
		})
	}
}

// TestMetaListRemove — drops a value from a list, collapses to scalar
// when one remains, removes the key when empty.
func TestMetaListRemove(t *testing.T) {
	cases := []struct {
		name, in, key, val, want string
	}{
		{"empty", "", "composed_by", "01P", ""},
		{"only-one-removed", `{"composed_by":"01P"}`, "composed_by", "01P", ""},
		{"array-to-scalar", `{"composed_by":["01P","01Q"]}`, "composed_by", "01Q", `{"composed_by":"01P"}`},
		{"array-preserve", `{"composed_by":["01P","01Q","01R"]}`, "composed_by", "01Q", `{"composed_by":["01P","01R"]}`},
		{"missing-no-op", `{"composed_by":"01P"}`, "worktree", "x", `{"composed_by":"01P"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := tasks.MetaListRemove(c.in, c.key, c.val)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != c.want {
				t.Fatalf("\nin:   %q\nkey:  %q val: %q\nwant: %q\ngot:  %q", c.in, c.key, c.val, c.want, got)
			}
		})
	}
}

// TestMetaMatch checks the in-Go evaluator. Used by tests + a fallback
// when the SQL filter can't push the predicate down.
func TestMetaMatch(t *testing.T) {
	meta := `{"composed_by":"01P","tags":["x","y"]}`
	if !tasks.MetaMatch(meta, map[string]string{"composed_by": "01P"}) {
		t.Errorf("scalar match should succeed")
	}
	if !tasks.MetaMatch(meta, map[string]string{"tags": "x"}) {
		t.Errorf("array containment match should succeed")
	}
	if tasks.MetaMatch(meta, map[string]string{"composed_by": "01Q"}) {
		t.Errorf("mismatch must fail")
	}
	if tasks.MetaMatch(meta, map[string]string{"composed_by": "01P", "missing_key": "x"}) {
		t.Errorf("missing key in want must fail")
	}
	// Legacy input on read path.
	if !tasks.MetaMatch("composed_by: 01P", map[string]string{"composed_by": "01P"}) {
		t.Errorf("legacy frontmatter match should succeed")
	}
}

// TestMetaHasKey
func TestMetaHasKey(t *testing.T) {
	meta := `{"branch":"main","worktree":""}`
	if !tasks.MetaHasKey(meta, "branch") {
		t.Errorf("present key should match")
	}
	if !tasks.MetaHasKey(meta, "worktree") {
		t.Errorf("present-but-empty key should match (has_key semantics)")
	}
	if tasks.MetaHasKey(meta, "pr") {
		t.Errorf("absent key should not match")
	}
}

// TestMetaIn checks the "value in [...]" evaluator.
func TestMetaIn(t *testing.T) {
	meta := `{"status_kind":"working","tags":["a","b"]}`
	if !tasks.MetaIn(meta, map[string][]string{"status_kind": {"open", "working"}}) {
		t.Errorf("scalar-in-list should match")
	}
	if !tasks.MetaIn(meta, map[string][]string{"tags": {"b"}}) {
		t.Errorf("array containment via in should match")
	}
	if tasks.MetaIn(meta, map[string][]string{"status_kind": {"done", "cancelled"}}) {
		t.Errorf("none-match should fail")
	}
}

// TestMetaKeys returns distinct keys in sorted order.
func TestMetaKeys(t *testing.T) {
	keys := tasks.MetaKeys(`{"branch":"main","worktree":"/tmp","composed_by":"01P"}`)
	want := []string{"branch", "composed_by", "worktree"}
	if len(keys) != len(want) {
		t.Fatalf("len: got %v, want %v", keys, want)
	}
	for i, k := range want {
		if keys[i] != k {
			t.Errorf("[%d]: got %q, want %q", i, keys[i], k)
		}
	}
	// Legacy reads too.
	keys2 := tasks.MetaKeys("composed_by: 01P\nworktree: /tmp")
	if len(keys2) != 2 || keys2[0] != "composed_by" || keys2[1] != "worktree" {
		t.Errorf("legacy keys: got %v", keys2)
	}
}
