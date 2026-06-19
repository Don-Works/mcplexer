package skillregistry_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/skills"
)

// SKILL.md body that declares every W4 frontmatter field. Mirrors the
// brief's YAML example one-for-one.
const w4Body = `---
name: w4-demo
description: Use when verifying that the W4 frontmatter extensions land end-to-end.
requires:
  - { binary: "ffmpeg" }
  - { env: "ANTHROPIC_API_KEY" }
  - { scope: "linear:read" }
produces:
  - "markdown"
  - "json:reveal-deck-config"
consumes:
  - "markdown"
  - "screenshot"
phases:
  - "discover"
  - "draft"
  - "publish"
refinement: "enabled"
---
# W4 demo

Body content for the W4 round-trip test.
`

func TestParse_ExtractsManifestExtra(t *testing.T) {
	parsed, err := skillregistry.Parse(w4Body, "w4-demo")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := skills.ManifestExtra{
		Requires: []skills.Requirement{
			{Binary: "ffmpeg"},
			{Env: "ANTHROPIC_API_KEY"},
			{Scope: "linear:read"},
		},
		Produces:   []string{"markdown", "json:reveal-deck-config"},
		Consumes:   []string{"markdown", "screenshot"},
		Phases:     []string{"discover", "draft", "publish"},
		Refinement: "enabled",
	}
	if !reflect.DeepEqual(parsed.Extra, want) {
		t.Errorf("Parsed.Extra mismatch:\ngot  %#v\nwant %#v", parsed.Extra, want)
	}

	// The five W4 keys must be REMOVED from the freeform metadata blob
	// so they're not duplicated as both `metadata.requires` and the
	// dedicated `manifest_extra` column on persistence.
	var meta map[string]any
	if err := json.Unmarshal(parsed.MetadataJSON, &meta); err != nil {
		t.Fatalf("metadata json: %v", err)
	}
	for _, key := range []string{"requires", "produces", "consumes", "phases", "refinement"} {
		if _, dup := meta[key]; dup {
			t.Errorf("metadata still carries %q after extras extraction: %v", key, meta)
		}
	}
}

func TestParse_RejectsInvalidExtra(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "requires entry with two kinds set",
			body: strings.Replace(w4Body, `- { binary: "ffmpeg" }`,
				`- { binary: "ffmpeg", env: "FOO" }`, 1),
		},
		{
			name: "refinement bad value",
			body: strings.Replace(w4Body, `refinement: "enabled"`,
				`refinement: "maybe"`, 1),
		},
		{
			name: "phase with uppercase",
			body: strings.Replace(w4Body, `- "discover"`, `- "Discover"`, 1),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := skillregistry.Parse(tc.body, "w4-demo")
			if err == nil {
				t.Fatal("Parse: expected error, got nil")
			}
		})
	}
}

func TestPublishGet_ManifestExtraRoundTrip(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	res, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "w4-demo", Body: w4Body})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if res.Action != "created" || res.Version != 1 {
		t.Fatalf("unexpected publish result: %+v", res)
	}

	got, err := reg.Get(ctx, skillregistry.GlobalScope(), "w4-demo", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	extra := skillregistry.ExtraFromEntry(got)
	want := skills.ManifestExtra{
		Requires: []skills.Requirement{
			{Binary: "ffmpeg"},
			{Env: "ANTHROPIC_API_KEY"},
			{Scope: "linear:read"},
		},
		Produces:   []string{"markdown", "json:reveal-deck-config"},
		Consumes:   []string{"markdown", "screenshot"},
		Phases:     []string{"discover", "draft", "publish"},
		Refinement: "enabled",
	}
	if !reflect.DeepEqual(extra, want) {
		t.Errorf("round-trip mismatch:\ngot  %#v\nwant %#v", extra, want)
	}
}

func TestExtraFromEntry_AbsentIsZero(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body := "---\nname: noextra\ndescription: skill with no W4 fields\n---\n# body\n"
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "noextra", Body: body}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	got, err := reg.Get(ctx, skillregistry.GlobalScope(), "noextra", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	extra := skillregistry.ExtraFromEntry(got)
	if !extra.IsZero() {
		t.Errorf("ExtraFromEntry on a no-W4 skill = %#v, want zero", extra)
	}
}

func TestListHeads_ExtraPersistsAcrossList(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "w4-demo", Body: w4Body}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	heads, err := reg.ListHeads(ctx, skillregistry.AdminScope(), 0)
	if err != nil {
		t.Fatalf("ListHeads: %v", err)
	}
	if len(heads) == 0 {
		t.Fatal("expected at least one head")
	}
	found := false
	for i := range heads {
		entry := heads[i]
		if entry.Name != "w4-demo" {
			continue
		}
		found = true
		extra := skillregistry.ExtraFromEntry(&entry)
		if extra.IsZero() {
			t.Errorf("listed head missing extras: %#v", entry)
		}
		if len(extra.Phases) != 3 {
			t.Errorf("expected 3 phases, got %d", len(extra.Phases))
		}
	}
	if !found {
		t.Fatal("w4-demo not present in heads")
	}
}
