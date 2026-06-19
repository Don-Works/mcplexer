package harnesssync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestRender_GoldenAndMarkers(t *testing.T) {
	tests := []struct {
		name    string
		key     HarnessKey
		version int
		golden  string
	}{
		{"claude_v1", Claude, 1, "testdata/claude.txt"},
		{"codex_v1", Codex, 1, "testdata/codex.txt"},
		{"opencode_v1", OpenCode, 1, "testdata/opencode.txt"},
		{"gemini_v1", Gemini, 1, "testdata/gemini.txt"},
		{"grok_v1", Grok, 1, "testdata/grok.txt"},
		{"mimo_v1", MiMo, 1, "testdata/mimo.txt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Render(tc.key, tc.version)
			if !strings.Contains(got, "MCPLEXER:HARNESS-SYNC:BEGIN") {
				t.Errorf("missing BEGIN marker for %s", tc.key)
			}
			if !strings.Contains(got, "MCPLEXER:HARNESS-SYNC:END") {
				t.Errorf("missing END marker for %s", tc.key)
			}
			if tc.key == Grok {
				if strings.Contains(got, "<!--") {
					t.Fatalf("grok render must be valid TOML comments, got HTML marker:\n%s", got)
				}
				var cfg map[string]any
				if err := toml.Unmarshal([]byte("[cli]\ninstaller = \"internal\"\n\n"+got), &cfg); err != nil {
					t.Fatalf("grok render is not TOML-safe: %v\n%s", err, got)
				}
			}
			if tc.golden != "" {
				want, err := os.ReadFile(tc.golden)
				if err != nil {
					t.Fatalf("read golden %s: %v", tc.golden, err)
				}
				// normalize trailing for compare
				if strings.TrimSpace(got) != strings.TrimSpace(string(want)) {
					t.Errorf("golden mismatch for %s:\ngot:\n%s\nwant:\n%s", tc.key, got, want)
				}
			}
			h := RenderedHash(tc.key, tc.version)
			if len(h) != 64 {
				t.Errorf("hash len %d want 64", len(h))
			}
		})
	}
}

func TestInstall_GrokReplacesLegacyHTMLBlockWithTOMLComments(t *testing.T) {
	dir := t.TempDir()
	target := TargetPath(dir, Grok)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `[cli]
installer = "internal"

<!-- MCPLEXER:HARNESS-SYNC:BEGIN v1 (grok) -->

` + usingMcplexerPointer + `

<!-- MCPLEXER:HARNESS-SYNC:END -->
`
	if err := os.WriteFile(target, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, st, err := Install(dir, Grok, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || !st.BootstrapInstalled {
		t.Fatalf("install should replace legacy block: changed=%v status=%+v", changed, st)
	}

	cur, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cur), "<!--") {
		t.Fatalf("legacy HTML marker remains in Grok TOML:\n%s", cur)
	}
	if !strings.Contains(string(cur), "# MCPLEXER:HARNESS-SYNC:BEGIN v1 (grok)") {
		t.Fatalf("new TOML marker missing:\n%s", cur)
	}
	var cfg map[string]any
	if err := toml.Unmarshal(cur, &cfg); err != nil {
		t.Fatalf("rewritten Grok config should parse as TOML: %v\n%s", err, cur)
	}

	st, err = Recheck(dir, Grok, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !st.BootstrapInstalled || st.Drifted {
		t.Fatalf("rewritten Grok block should be clean: %+v", st)
	}
}

func TestRecheck_Drift(t *testing.T) {
	dir := t.TempDir()
	k := Codex
	ver := 1

	// fresh: not installed
	st, err := Recheck(dir, k, ver)
	if err != nil {
		t.Fatal(err)
	}
	if st.BootstrapInstalled || st.Drifted {
		t.Errorf("fresh should not be installed/drifted: %+v", st)
	}

	// install
	changed, st, err := Install(dir, k, ver)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || !st.BootstrapInstalled {
		t.Errorf("install should change and install: %+v", st)
	}

	// recheck clean
	st2, err := Recheck(dir, k, ver)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Drifted {
		t.Errorf("should not be drifted after fresh install")
	}

	// corrupt the block
	p := TargetPath(dir, k)
	data, _ := os.ReadFile(p)
	bad := strings.Replace(string(data), "top-level tools", "HACKED tools", 1)
	_ = os.WriteFile(p, []byte(bad), 0o644)

	st3, _ := Recheck(dir, k, ver)
	if !st3.Drifted {
		t.Errorf("should detect drift after edit")
	}
}

func TestClaude_SkillSidecar(t *testing.T) {
	dir := t.TempDir()
	_, _, err := Install(dir, Claude, 1)
	if err != nil {
		t.Fatal(err)
	}
	skill := ClaudeSkillPath(dir)
	b, err := os.ReadFile(skill)
	if err != nil {
		t.Fatalf("claude skill not written: %v", err)
	}
	if !strings.Contains(string(b), "# Using MCPlexer") {
		preview := string(b)
		if len(preview) > 120 {
			preview = preview[:120]
		}
		t.Errorf("skill sidecar should contain full seed body, got: %s", preview)
	}
	if !strings.Contains(string(b), "mcpx__search_tools") {
		t.Errorf("skill sidecar missing mcpx__search_tools from seed")
	}
	if strings.Contains(string(b), "Registry-first: mcpx.skill_search") {
		t.Error("skill sidecar should be full seed body, not slim managed pointer")
	}
}

func TestClaude_SkillSidecarRecreatedWhenBlockCurrent(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Install(dir, Claude, 1); err != nil {
		t.Fatal(err)
	}
	skill := ClaudeSkillPath(dir)
	if err := os.Remove(skill); err != nil {
		t.Fatal(err)
	}

	st, err := Recheck(dir, Claude, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Drifted {
		t.Fatal("missing sidecar should mark claude install drifted")
	}

	changed, st, err := Install(dir, Claude, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("install should report changed when recreating missing sidecar")
	}
	if !st.BootstrapInstalled {
		t.Fatal("bootstrap should remain installed")
	}
	if _, err := os.Stat(skill); err != nil {
		t.Fatalf("sidecar was not recreated: %v", err)
	}

	st, err = Recheck(dir, Claude, 1)
	if err != nil {
		t.Fatal(err)
	}
	if st.Drifted {
		t.Fatalf("recreated sidecar should clear drift: %+v", st)
	}
}

func TestOpenCode_SkillSidecar(t *testing.T) {
	dir := t.TempDir()
	_, _, err := Install(dir, OpenCode, 1)
	if err != nil {
		t.Fatal(err)
	}
	skill := OpenCodeSkillPath(dir)
	b, err := os.ReadFile(skill)
	if err != nil {
		t.Fatalf("opencode skill not written: %v", err)
	}
	if !strings.Contains(string(b), "# Using MCPlexer") {
		preview := string(b)
		if len(preview) > 120 {
			preview = preview[:120]
		}
		t.Errorf("skill sidecar should contain full seed body, got: %s", preview)
	}
	if !strings.Contains(string(b), "mcpx__search_tools") {
		t.Errorf("skill sidecar missing mcpx__search_tools from seed")
	}
	if strings.Contains(string(b), "Registry-first: mcpx.skill_search") {
		t.Error("skill sidecar should be full seed body, not slim managed pointer")
	}
}

func TestOpenCode_SkillSidecarRecreatedWhenBlockCurrent(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Install(dir, OpenCode, 1); err != nil {
		t.Fatal(err)
	}
	skill := OpenCodeSkillPath(dir)
	if err := os.Remove(skill); err != nil {
		t.Fatal(err)
	}

	st, err := Recheck(dir, OpenCode, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Drifted {
		t.Fatal("missing sidecar should mark opencode install drifted")
	}

	changed, st, err := Install(dir, OpenCode, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("install should report changed when recreating missing sidecar")
	}
	if !st.BootstrapInstalled {
		t.Fatal("bootstrap should remain installed")
	}
	if _, err := os.Stat(skill); err != nil {
		t.Fatalf("sidecar was not recreated: %v", err)
	}

	st, err = Recheck(dir, OpenCode, 1)
	if err != nil {
		t.Fatal(err)
	}
	if st.Drifted {
		t.Fatalf("recreated sidecar should clear drift: %+v", st)
	}
}

func TestOpenCode_RenderedBlockIsSlimPointer(t *testing.T) {
	dir := t.TempDir()
	changed, _, err := Install(dir, OpenCode, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first install should change")
	}

	target := TargetPath(dir, OpenCode)
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	// The AGENTS.md block should be a slim pointer, not the full seed body
	if !strings.Contains(content, "using-mcplexer skill (v1) installed") {
		t.Errorf("AGENTS.md block should contain pointer text, got:\n%s", content)
	}
	if !strings.Contains(content, "SKILL.md") {
		t.Errorf("AGENTS.md block should reference sidecar path, got:\n%s", content)
	}
	// The full seed body should NOT be inlined in AGENTS.md
	if strings.Contains(content, "# Using MCPlexer") {
		t.Error("AGENTS.md should not contain full seed body inline")
	}

	// The sidecar should contain the full body
	skill := OpenCodeSkillPath(dir)
	skillBody, err := os.ReadFile(skill)
	if err != nil {
		t.Fatalf("sidecar not written: %v", err)
	}
	if !strings.Contains(string(skillBody), "# Using MCPlexer") {
		t.Error("sidecar should contain full seed body")
	}
}

func TestInstall_PreservesExistingContent(t *testing.T) {
	dir := t.TempDir()
	k := Codex
	ver := 1

	// Create file with existing content (simulating ~/.codex/AGENTS.md)
	existing := "# Existing Agents\n\nSome manually added instructions.\n\nMore content here.\n"
	target := TargetPath(dir, k)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	before, err := Recheck(dir, k, ver)
	if err != nil {
		t.Fatal(err)
	}
	if before.BootstrapInstalled || before.Drifted {
		t.Fatalf("plain existing file should not be installed/drifted: %+v", before)
	}

	// Install should preserve existing content, insert/replace our block only
	changed, st, err := Install(dir, k, ver)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("install should report changed when adding block to existing file")
	}
	if !st.BootstrapInstalled {
		t.Error("bootstrap should be installed")
	}

	// Verify existing content is preserved
	cur, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cur), "Existing Agents") {
		t.Error("existing content not preserved")
	}
	if !strings.Contains(string(cur), "manually added") {
		t.Error("existing content not preserved")
	}
	if !strings.Contains(string(cur), "MCPLEXER:HARNESS-SYNC:BEGIN") {
		t.Error("managed block not inserted")
	}

	after, err := Recheck(dir, k, ver)
	if err != nil {
		t.Fatal(err)
	}
	if !after.BootstrapInstalled || after.Drifted {
		t.Fatalf("preserved content should not make managed block drift: %+v", after)
	}

	changed, _, err = Install(dir, k, ver)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second install of unchanged managed block should be idempotent")
	}
}

func TestInstall_ReplacesExistingBlock(t *testing.T) {
	dir := t.TempDir()
	k := Codex
	ver := 1

	// Create file with existing block (same version)
	oldBlock := `<!-- MCPLEXER:HARNESS-SYNC:BEGIN v1 (codex) -->

old content

<!-- MCPLEXER:HARNESS-SYNC:END -->
`
	existing := "# Codex\n\nSome instructions.\n\n" + oldBlock
	target := TargetPath(dir, k)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	// Install should replace existing block with new content (different body), preserve rest
	changed, st, err := Install(dir, k, ver)
	if err != nil {
		t.Fatal(err)
	}
	// changed is true because we're replacing the block content (new rendered vs old content)
	if !changed {
		t.Error("install should report changed when replacing stale block content")
	}
	if !st.BootstrapInstalled {
		t.Error("bootstrap should be installed")
	}

	// Verify existing content preserved, block updated
	cur, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cur), "Some instructions") {
		t.Error("existing non-block content not preserved")
	}
	if !strings.Contains(string(cur), "MCPLEXER:HARNESS-SYNC:BEGIN") {
		t.Error("managed block missing")
	}
	if strings.Contains(string(cur), "old content") {
		t.Error("old block content should be replaced")
	}
}

func TestInstall_UnknownHarness(t *testing.T) {
	dir := t.TempDir()
	// Unknown harness key should error on Install (empty TargetPath would be used)
	unknown := HarnessKey("unknown")
	_, _, err := Install(dir, unknown, 1)
	if err == nil {
		t.Error("unknown harness should error on Install")
	}
}

func TestValid_HarnessKey(t *testing.T) {
	tests := []struct {
		k      HarnessKey
		expect bool
	}{
		{Claude, true},
		{Codex, true},
		{OpenCode, true},
		{Gemini, true},
		{Grok, true},
		{MiMo, true},
		{Pi, true},
		{"unknown", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(string(tc.k), func(t *testing.T) {
			if got := Valid(tc.k); got != tc.expect {
				t.Errorf("Valid(%q) = %v, want %v", tc.k, got, tc.expect)
			}
		})
	}
}

// TestSetupJSONShape exercises the row shape used by the /api/v1/setup/*
// endpoints (contract fields present).
func TestSetupJSONShape(t *testing.T) {
	v := 1
	row := HarnessStatus{
		Key:                "claude",
		MCPWired:           true,
		ConfigPath:         "/tmp/.claude.json",
		BootstrapInstalled: true,
		BootstrapVersion:   &v,
		RegistryVersion:    1,
		Drifted:            false,
	}
	if row.Key != "claude" || !row.MCPWired || row.RegistryVersion != 1 {
		t.Errorf("row shape wrong: %+v", row)
	}
	// error envelope shape (used by handler) - compile time check only
	type setupErr struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Hint    string `json:"hint,omitempty"`
		} `json:"error"`
	}
	_ = setupErr{}
}
