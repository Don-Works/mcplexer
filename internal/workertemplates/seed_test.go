package workertemplates_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/workertemplates"
)

// TestSeedEmbeddedTemplates exercises the seed walker against a fresh
// store, then confirms every embedded JSON file landed as a valid
// template head. Catches both shape regressions (malformed JSON, missing
// required fields) and packaging regressions (a new seed file added but
// not picked up because of a typo in the embed path).
func TestSeedEmbeddedTemplates(t *testing.T) {
	t.Setenv("MCPLEXER_TEST_SEEDS", "1") // include the harness-only seed
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := workertemplates.New(db)

	if err := workertemplates.Seed(context.Background(), reg); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	heads, err := reg.ListHeads(context.Background(), workertemplates.AdminScope(), 0)
	if err != nil {
		t.Fatalf("ListHeads: %v", err)
	}

	// Sanity floor: at least the templates we know are bundled. Adding
	// new templates extends this list — leave the floor low so this
	// test never fails just because someone shipped a new helpful seed.
	required := []string{
		"memory-consolidator",
		"nightly-bulletproof",
		"consolidator-echo",
	}
	got := map[string]bool{}
	for _, h := range heads {
		got[h.Name] = true
	}
	for _, name := range required {
		if !got[name] {
			t.Errorf("expected bundled seed %q to be published, not in heads", name)
		}
	}
}

// TestSeedSkipsTestOnlySeedsByDefault guards the production boot path:
// without MCPLEXER_TEST_SEEDS=1 the self-described test template
// (consolidator-echo) must NOT be published, while the production seeds
// still land.
func TestSeedSkipsTestOnlySeedsByDefault(t *testing.T) {
	t.Setenv("MCPLEXER_TEST_SEEDS", "") // explicit: opt-in absent
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := workertemplates.New(db)
	if err := workertemplates.Seed(context.Background(), reg); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	heads, err := reg.ListHeads(context.Background(), workertemplates.AdminScope(), 0)
	if err != nil {
		t.Fatalf("ListHeads: %v", err)
	}
	got := map[string]bool{}
	for _, h := range heads {
		got[h.Name] = true
	}
	if got["consolidator-echo"] {
		t.Error("consolidator-echo published without MCPLEXER_TEST_SEEDS=1 — test template leaked into a production boot")
	}
	for _, name := range []string{"memory-consolidator", "nightly-bulletproof"} {
		if !got[name] {
			t.Errorf("production seed %q missing — env gate over-filtered", name)
		}
	}
}

// TestNightlyBulletproofShape locks in the load-bearing fields of the
// nightly worker template so a future edit doesn't silently break the
// contract: cron at 02:00 UTC, references the bulletproof skill, posts
// to mesh, opt-in (no Enabled flag in the template JSON since installs
// default to disabled).
func TestNightlyBulletproofShape(t *testing.T) {
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := workertemplates.New(db)
	if err := workertemplates.Seed(context.Background(), reg); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	entry, err := reg.Get(context.Background(),
		workertemplates.AdminScope(), "nightly-bulletproof",
		workertemplates.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	tmpl, err := workertemplates.Unmarshal(entry.Body)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if tmpl.ScheduleSpecHint != "0 2 * * *" {
		t.Errorf("schedule_spec_hint = %q, want %q", tmpl.ScheduleSpecHint, "0 2 * * *")
	}
	if tmpl.SkillName != "test-mcplexer-bulletproof" {
		t.Errorf("skill_name = %q, want %q", tmpl.SkillName, "test-mcplexer-bulletproof")
	}
	if tmpl.ModelProviderHint != "openai_compat" {
		t.Errorf("model_provider_hint = %q, want openai_compat", tmpl.ModelProviderHint)
	}
	// At least one mesh output channel so the run's result reaches the
	// operator without further config. Locks in the dynamic-priority
	// shape: priority=low for green nights, priority_on_fail=high for
	// red ones — the runner reads priority_on_fail when the run status
	// is non-success, so a red night promotes the broadcast to "high"
	// without the prompt having to mesh__send a separate alert.
	var meshHint *workertemplates.OutputChannelHint
	for i := range tmpl.OutputChannelsHint {
		if tmpl.OutputChannelsHint[i].Type == "mesh" {
			meshHint = &tmpl.OutputChannelsHint[i]
			break
		}
	}
	if meshHint == nil {
		t.Fatal("nightly-bulletproof output_channels_hint missing a mesh entry")
	}
	if meshHint.Priority != "low" {
		t.Errorf("mesh priority = %q, want %q", meshHint.Priority, "low")
	}
	if meshHint.PriorityOnFail != "high" {
		t.Errorf("mesh priority_on_fail = %q, want %q", meshHint.PriorityOnFail, "high")
	}
}

// TestConsolidatorEchoShape locks in the load-bearing fields of the
// echo-LLM-backed consolidator template so a future edit doesn't
// silently break the harness contract:
//
//   - model_provider_hint == "openai_compat" (the echo-llm speaks the
//     OpenAI chat-completions surface)
//   - tool_allowlist includes memory__save + memory__invalidate (the
//     two write actions the consolidator's domain-finalize hook
//     counts on)
//   - name == "consolidator-echo" (the harness queries by this exact
//     name; renaming requires a coordinated harness update)
func TestConsolidatorEchoShape(t *testing.T) {
	t.Setenv("MCPLEXER_TEST_SEEDS", "1") // harness-only seed is gated
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := workertemplates.New(db)
	if err := workertemplates.Seed(context.Background(), reg); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	entry, err := reg.Get(context.Background(),
		workertemplates.AdminScope(), "consolidator-echo",
		workertemplates.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	tmpl, err := workertemplates.Unmarshal(entry.Body)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if tmpl.Name != "consolidator-echo" {
		t.Errorf("name = %q, want consolidator-echo", tmpl.Name)
	}
	if tmpl.ModelProviderHint != "openai_compat" {
		t.Errorf("model_provider_hint = %q, want openai_compat (echo-llm path)",
			tmpl.ModelProviderHint)
	}
	requiredTools := map[string]bool{
		"memory__save":       false,
		"memory__invalidate": false,
		"memory__list":       false,
	}
	for _, name := range tmpl.ToolAllowlist {
		if _, ok := requiredTools[name]; ok {
			requiredTools[name] = true
		}
	}
	for name, ok := range requiredTools {
		if !ok {
			t.Errorf("tool_allowlist missing %q (harness depends on it)", name)
		}
	}
}
