package skillregistry_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

func includeBody(name, description, declarations, markdown string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n%s---\n%s", name, description, declarations, markdown)
}

func includeDeclaration(id, name, scope string, version int, hash, section string) string {
	sectionLine := ""
	if section != "" {
		sectionLine = "    section: " + section + "\n"
	}
	return fmt.Sprintf("includes:\n  - id: %s\n    skill: %s\n    scope: %s\n    version: %d\n    content_hash: %q\n%s",
		id, name, scope, version, hash, sectionLine)
}

func TestCompositionRendersNestedPinnedSectionsDeterministically(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	leafBody := includeBody("neutral-leaf", "Leaf fixture.", "",
		"# Leaf\n\nOutside.\n<!-- mcpx:section core -->\nLEAF CORE\n<!-- mcpx:endsection -->\n")
	leafResult, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "neutral-leaf", Body: leafBody})
	if err != nil {
		t.Fatalf("publish leaf: %v", err)
	}
	middleDecl := includeDeclaration("leaf-core", "neutral-leaf", "global", leafResult.Version, leafResult.ContentHash, "core")
	middleBody := includeBody("neutral-middle", "Middle fixture.", middleDecl,
		"# Middle\n\n<!-- mcpx:section instructions -->\nMIDDLE START\n<!-- mcpx:include leaf-core -->\nMIDDLE END\n<!-- mcpx:endsection -->\n")
	middleResult, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "neutral-middle", Body: middleBody})
	if err != nil {
		t.Fatalf("publish middle: %v", err)
	}
	rootDecl := includeDeclaration("middle-instructions", "neutral-middle", "global", middleResult.Version, middleResult.ContentHash, "instructions")
	rootBody := includeBody("neutral-root", "Root fixture.", rootDecl,
		"# Root\n\nBEFORE\n<!-- mcpx:include middle-instructions -->\nAFTER\n")
	rootResult, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "neutral-root", Body: rootBody})
	if err != nil {
		t.Fatalf("publish root: %v", err)
	}

	entry, err := reg.Get(ctx, skillregistry.GlobalScope(), "neutral-root", skillregistry.VersionRef{Version: rootResult.Version})
	if err != nil {
		t.Fatalf("get raw root: %v", err)
	}
	if entry.Body != rootBody {
		t.Fatal("Registry.Get changed the raw composition document")
	}
	if entry.ContentHash != skillregistry.ComputeContentHash(rootBody) {
		t.Fatalf("raw content hash changed: %s", entry.ContentHash)
	}
	if extra := skillregistry.ExtraFromEntry(entry); len(extra.Includes) != 1 || extra.Includes[0].ID != "middle-instructions" {
		t.Fatalf("typed include did not survive sqlite round-trip: %+v", extra)
	}

	first, err := reg.RenderEntry(ctx, entry)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	second, err := reg.RenderEntry(ctx, entry)
	if err != nil {
		t.Fatalf("render again: %v", err)
	}
	if first.Body != second.Body || first.SHA256 != second.SHA256 {
		t.Fatalf("render is not deterministic:\nfirst=%+v\nsecond=%+v", first, second)
	}
	for _, want := range []string{"BEFORE", "MIDDLE START", "LEAF CORE", "MIDDLE END", "AFTER"} {
		if !strings.Contains(first.Body, want) {
			t.Errorf("expanded body missing %q: %s", want, first.Body)
		}
	}
	for _, unwanted := range []string{"name: neutral-middle", "name: neutral-leaf", "mcpx:include", "mcpx:section", "Outside."} {
		if strings.Contains(first.Body, unwanted) {
			t.Errorf("expanded body unexpectedly contains %q: %s", unwanted, first.Body)
		}
	}
	if got := len(first.Provenance); got != 2 {
		t.Fatalf("provenance edges = %d, want 2: %+v", got, first.Provenance)
	}
	if first.Provenance[0].IncludeID != "middle-instructions" || first.Provenance[1].IncludeID != "leaf-core" {
		t.Fatalf("provenance order is not preorder: %+v", first.Provenance)
	}
	if first.SHA256 != skillregistry.ComputeContentHash(first.Body) {
		t.Fatalf("expanded sha mismatch: %s", first.SHA256)
	}
}

func TestCompositionExactScopeBypassesWorkspaceShadowing(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	workspace := "workspace-neutral"

	globalBody := strings.Replace(sampleBody("scope-target", "Global target body."), "Body content for scope-target.", "GLOBAL CONTENT", 1)
	globalResult, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "scope-target", Body: globalBody})
	if err != nil {
		t.Fatalf("publish global target: %v", err)
	}
	workspaceBody := strings.Replace(sampleBody("scope-target", "Workspace target body."), "Body content for scope-target.", "WORKSPACE CONTENT", 1)
	workspaceResult, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "scope-target", Body: workspaceBody, WorkspaceID: &workspace,
	})
	if err != nil {
		t.Fatalf("publish workspace target: %v", err)
	}
	declarations := includeDeclaration("global-target", "scope-target", "global", globalResult.Version, globalResult.ContentHash, "") +
		strings.TrimPrefix(includeDeclaration("same-target", "scope-target", "same", workspaceResult.Version, workspaceResult.ContentHash, ""), "includes:\n")
	rootBody := includeBody("scope-root", "Scope fixture.", declarations,
		"GLOBAL:\n<!-- mcpx:include global-target -->\nSAME:\n<!-- mcpx:include same-target -->\n")
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "scope-root", Body: rootBody, WorkspaceID: &workspace}); err != nil {
		t.Fatalf("publish workspace root: %v", err)
	}
	rendered, err := reg.Render(ctx, store.SkillScope{WorkspaceIDs: []string{workspace}}, "scope-root", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(rendered.Body, "GLOBAL CONTENT") || !strings.Contains(rendered.Body, "WORKSPACE CONTENT") {
		t.Fatalf("exact scope resolution returned the wrong targets: %s", rendered.Body)
	}
	if strings.Contains(rendered.Body, "name: scope-target") {
		t.Fatalf("included full skills leaked their frontmatter: %s", rendered.Body)
	}
}

func TestCompositionPublishFailsClosed(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	targetBody := sampleBody("validation-target", "Validation target.")
	target, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "validation-target", Body: targetBody})
	if err != nil {
		t.Fatalf("publish target: %v", err)
	}
	validDecl := includeDeclaration("target", "validation-target", "global", target.Version, target.ContentHash, "")

	cases := []struct {
		name    string
		decl    string
		body    string
		wantErr string
	}{
		{
			name: "hash mismatch",
			decl: includeDeclaration("target", "validation-target", "global", target.Version, strings.Repeat("0", 64), ""),
			body: "<!-- mcpx:include target -->\n", wantErr: "content hash mismatch",
		},
		{
			name: "undeclared marker", decl: validDecl,
			body: "<!-- mcpx:include missing -->\n<!-- mcpx:include target -->\n", wantErr: "undeclared include",
		},
		{
			name: "unused declaration", decl: validDecl,
			body: "No marker.\n", wantErr: "declared but has no marker",
		},
		{
			name: "duplicate marker", decl: validDecl,
			body: "<!-- mcpx:include target -->\n<!-- mcpx:include target -->\n", wantErr: "appears more than once",
		},
		{
			name: "marker in fence is inert", decl: validDecl,
			body: "```\n```go\n<!-- mcpx:include target -->\n```\n", wantErr: "declared but has no marker",
		},
		{
			name: "missing section",
			decl: includeDeclaration("target", "validation-target", "global", target.Version, target.ContentHash, "missing"),
			body: "<!-- mcpx:include target -->\n", wantErr: "section \"missing\" was not found",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := "invalid-" + strings.ReplaceAll(tc.name, " ", "-")
			body := includeBody(name, "Invalid composition fixture.", tc.decl, tc.body)
			_, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: name, Body: body})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("publish error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestCompositionRejectsAssetBearingIncludeTargets(t *testing.T) {
	cases := []struct {
		name string
		opts func(t *testing.T, name, body string) skillregistry.PublishOptions
	}{
		{
			name: "bundle",
			opts: func(t *testing.T, name, body string) skillregistry.PublishOptions {
				return skillregistry.PublishOptions{
					Name: name, Body: body,
					Bundle: buildBundle(t, name, map[string]string{
						"SKILL.md": body, "scripts/helper.sh": "echo neutral\n",
					}),
				}
			},
		},
		{
			name: "path",
			opts: func(t *testing.T, name, body string) skillregistry.PublishOptions {
				return skillregistry.PublishOptions{Name: name, Body: body, SourcePath: t.TempDir()}
			},
		},
		{
			name: "git-source-type",
			opts: func(_ *testing.T, name, body string) skillregistry.PublishOptions {
				return skillregistry.PublishOptions{Name: name, Body: body, SourceTypeOverride: "git"}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, _ := newTestRegistry(t)
			ctx := context.Background()
			targetName := "asset-target-" + tc.name
			targetBody := sampleBody(targetName, "Use when testing asset-bearing fragments.")
			target, err := reg.Publish(ctx, tc.opts(t, targetName, targetBody))
			if err != nil {
				t.Fatalf("publish target: %v", err)
			}
			rootName := "asset-root-" + tc.name
			rootBody := includeBody(rootName, "Use when rejecting asset-bearing fragments.",
				includeDeclaration("target", targetName, "global", target.Version, target.ContentHash, ""),
				"<!-- mcpx:include target -->\n")
			_, err = reg.Publish(ctx, skillregistry.PublishOptions{Name: rootName, Body: rootBody})
			for _, want := range []string{"merges prose only", "flatten/copy the prose", "text-only inline fragment"} {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("publish error = %v, want actionable %q guidance", err, want)
				}
			}
		})
	}
}

func TestCompositionRejectsExpandedBodyOverLimit(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	targetBody := includeBody("large-fragment", "Large neutral fragment.", "", strings.Repeat("x", 40*1024))
	target, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "large-fragment", Body: targetBody})
	if err != nil {
		t.Fatalf("publish target: %v", err)
	}
	declarations := includeDeclaration("first", "large-fragment", "global", target.Version, target.ContentHash, "") +
		strings.TrimPrefix(includeDeclaration("second", "large-fragment", "global", target.Version, target.ContentHash, ""), "includes:\n")
	rootBody := includeBody("large-root", "Expansion cap fixture.", declarations,
		"<!-- mcpx:include first -->\n<!-- mcpx:include second -->\n")
	_, err = reg.Publish(ctx, skillregistry.PublishOptions{Name: "large-root", Body: rootBody})
	if err == nil || !strings.Contains(err.Error(), "expands to") {
		t.Fatalf("publish error = %v, want expanded-size failure", err)
	}
}

func TestCompositionEnforcesDepthAndEdgeLimits(t *testing.T) {
	t.Run("depth", func(t *testing.T) {
		reg, _ := newTestRegistry(t)
		ctx := context.Background()
		name := "depth-leaf"
		result, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: name, Body: sampleBody(name, "Depth leaf.")})
		if err != nil {
			t.Fatalf("publish leaf: %v", err)
		}
		for level := 1; level <= skillregistry.MaxCompositionDepth+1; level++ {
			next := fmt.Sprintf("depth-level-%d", level)
			body := includeBody(next, "Depth fixture.",
				includeDeclaration("previous", name, "global", result.Version, result.ContentHash, ""),
				"<!-- mcpx:include previous -->\n")
			published, publishErr := reg.Publish(ctx, skillregistry.PublishOptions{Name: next, Body: body})
			if level == skillregistry.MaxCompositionDepth+1 {
				if publishErr == nil || !strings.Contains(publishErr.Error(), "depth exceeds") {
					t.Fatalf("level %d error = %v, want depth limit", level, publishErr)
				}
				break
			}
			if publishErr != nil {
				t.Fatalf("publish level %d: %v", level, publishErr)
			}
			name, result = next, published
		}
	})

	t.Run("edges", func(t *testing.T) {
		reg, _ := newTestRegistry(t)
		ctx := context.Background()
		leaf, err := reg.Publish(ctx, skillregistry.PublishOptions{
			Name: "edge-leaf", Body: sampleBody("edge-leaf", "Edge leaf."),
		})
		if err != nil {
			t.Fatalf("publish leaf: %v", err)
		}
		const branchCount = 11 // root edges + two per branch = 33
		branches := make([]*skillregistry.PublishResult, 0, branchCount)
		for i := 0; i < branchCount; i++ {
			name := fmt.Sprintf("edge-branch-%d", i)
			decl := includeDeclaration("first", "edge-leaf", "global", leaf.Version, leaf.ContentHash, "") +
				strings.TrimPrefix(includeDeclaration("second", "edge-leaf", "global", leaf.Version, leaf.ContentHash, ""), "includes:\n")
			body := includeBody(name, "Edge branch.", decl,
				"<!-- mcpx:include first -->\n<!-- mcpx:include second -->\n")
			branch, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: name, Body: body})
			if err != nil {
				t.Fatalf("publish branch %d: %v", i, err)
			}
			branches = append(branches, branch)
		}
		var declarations, markers strings.Builder
		declarations.WriteString("includes:\n")
		for i, branch := range branches {
			fmt.Fprintf(&declarations, "  - id: branch-%d\n    skill: edge-branch-%d\n    scope: global\n    version: %d\n    content_hash: %q\n",
				i, i, branch.Version, branch.ContentHash)
			fmt.Fprintf(&markers, "<!-- mcpx:include branch-%d -->\n", i)
		}
		body := includeBody("edge-root", "Edge limit root.", declarations.String(), markers.String())
		_, err = reg.Publish(ctx, skillregistry.PublishOptions{Name: "edge-root", Body: body})
		if err == nil || !strings.Contains(err.Error(), "edge count exceeds") {
			t.Fatalf("publish error = %v, want edge limit", err)
		}
	})
}

func TestRenderWithoutIncludesIsByteIdentical(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "byte-stable",
			body: " \n---\r\nname: byte-stable\r\ndescription: Byte compatibility fixture.\r\n---\r\n# Body\r\n\r\nExact bytes.\r\n",
		},
		{
			name: "literal-include",
			body: "---\nname: literal-include\ndescription: Use when documenting composition syntax.\n---\n# Literal example\n\n<!-- mcpx:include undeclared-example -->\n<!-- mcpx:include malformed example -->\n",
		},
		{
			name: "literal-section",
			body: "---\nname: literal-section\ndescription: Use when documenting section syntax.\n---\n# Literal example\n\n<!-- mcpx:section unmatched-example -->\nNo closing marker is intentional.\n",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			reg, _ := newTestRegistry(t)
			ctx := context.Background()
			result, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: test.name, Body: test.body})
			if err != nil {
				t.Fatalf("publish: %v", err)
			}
			rendered, err := reg.Render(ctx, skillregistry.GlobalScope(), test.name, skillregistry.VersionRef{Version: result.Version})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if rendered.Body != test.body || rendered.SHA256 != result.ContentHash {
				t.Fatalf("plain body changed:\nwant %q (%s)\n got %q (%s)",
					test.body, result.ContentHash, rendered.Body, rendered.SHA256)
			}
		})
	}
}
