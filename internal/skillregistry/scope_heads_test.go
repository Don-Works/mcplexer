package skillregistry_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

func TestWorkspacePrecedenceAndScopeHeads(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	const child, parent = "workspace-child", "workspace-parent"

	publishVersions(t, reg, nil, "layered", 9, "global")
	publishVersions(t, reg, ptr(parent), "layered", 5, "parent")
	publishVersions(t, reg, ptr(child), "layered", 1, "child")
	scope := store.SkillScope{WorkspaceIDs: []string{child, parent}}

	head, err := reg.Get(ctx, scope, "layered", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("get effective head: %v", err)
	}
	if head.WorkspaceID == nil || *head.WorkspaceID != child || head.Version != 1 {
		t.Fatalf("effective head = %+v, want child v1", head)
	}

	effective, err := reg.ListHeads(ctx, scope, 0)
	if err != nil {
		t.Fatalf("list effective heads: %v", err)
	}
	if len(effective) != 1 || effective[0].WorkspaceID == nil ||
		*effective[0].WorkspaceID != child || effective[0].Version != 1 {
		t.Fatalf("effective heads = %+v, want child v1", effective)
	}

	scopeHeads, err := reg.ListScopeHeads(ctx, scope, 0)
	if err != nil {
		t.Fatalf("list scope heads: %v", err)
	}
	if len(scopeHeads) != 3 {
		t.Fatalf("scope heads count = %d, want 3", len(scopeHeads))
	}
	want := map[string]int{"global": 9, child: 1, parent: 5}
	for _, entry := range scopeHeads {
		key := "global"
		if entry.WorkspaceID != nil {
			key = *entry.WorkspaceID
		}
		if entry.Version != want[key] {
			t.Errorf("%s head = v%d, want v%d", key, entry.Version, want[key])
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Errorf("missing scope heads: %v", want)
	}

	childHits, err := reg.Search(ctx, scope, "layered", 1)
	if err != nil || len(childHits) != 1 || childHits[0].Version != 1 {
		t.Fatalf("child-first search: hits=%+v err=%v", childHits, err)
	}
	parentFirst := store.SkillScope{WorkspaceIDs: []string{parent, child}}
	parentHits, err := reg.Search(ctx, parentFirst, "layered", 1)
	if err != nil || len(parentHits) != 1 || parentHits[0].Version != 5 {
		t.Fatalf("parent-first search reused wrong scope cache: hits=%+v err=%v", parentHits, err)
	}
}

func TestExplicitVersionPrecedenceIncludesStableAndBundle(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	const child, parent, name = "bundle-child", "bundle-parent", "layered-bundle"
	bundles := make(map[string][]byte)
	for _, fixture := range []struct {
		label string
		ws    *string
	}{
		{label: "global"}, {label: "parent", ws: ptr(parent)}, {label: "child", ws: ptr(child)},
	} {
		body := sampleBody(name, "Use when testing "+fixture.label+" explicit precedence.")
		bundle := buildBundle(t, name, map[string]string{"SKILL.md": body, "scope.txt": fixture.label})
		bundles[fixture.label] = bundle
		if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
			Name: name, Body: body, Bundle: bundle, WorkspaceID: fixture.ws,
		}); err != nil {
			t.Fatalf("publish %s: %v", fixture.label, err)
		}
	}
	scope := store.SkillScope{WorkspaceIDs: []string{child, parent}}
	if err := reg.SetTag(ctx, scope, name, "@stable", 1, "test"); err != nil {
		t.Fatalf("set stable: %v", err)
	}
	for label, ref := range map[string]skillregistry.VersionRef{
		"explicit": {Version: 1}, "stable": {Stable: true},
	} {
		entry, err := reg.Get(ctx, scope, name, ref)
		if err != nil || entry.WorkspaceID == nil || *entry.WorkspaceID != child {
			t.Fatalf("%s resolution: entry=%+v err=%v", label, entry, err)
		}
	}
	bundle, _, err := reg.FetchBundle(ctx, scope, name, skillregistry.VersionRef{Version: 1})
	if err != nil || !bytes.Equal(bundle, bundles["child"]) {
		t.Fatalf("child bundle resolution: size=%d err=%v", len(bundle), err)
	}
	parentFirst := store.SkillScope{WorkspaceIDs: []string{parent, child}}
	entry, err := reg.Get(ctx, parentFirst, name, skillregistry.VersionRef{Version: 1})
	if err != nil || entry.WorkspaceID == nil || *entry.WorkspaceID != parent {
		t.Fatalf("parent-first resolution: entry=%+v err=%v", entry, err)
	}
}

func TestScopeHeadsDeletedFallbackAndLimit(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	ws := ptr("workspace-one")
	publishVersions(t, reg, ws, "alpha", 2, "workspace")
	publishVersions(t, reg, nil, "alpha", 2, "global")
	publishVersions(t, reg, nil, "bravo", 1, "global")

	if err := reg.SoftDelete(ctx, ws, "alpha", 2); err != nil {
		t.Fatalf("delete workspace alpha v2: %v", err)
	}
	if err := reg.SoftDelete(ctx, nil, "alpha", 2); err != nil {
		t.Fatalf("delete global alpha v2: %v", err)
	}

	heads, err := reg.ListScopeHeads(ctx, skillregistry.AdminScope(), 0)
	if err != nil {
		t.Fatalf("list scope heads: %v", err)
	}
	if len(heads) != 3 {
		t.Fatalf("scope heads count = %d, want 3", len(heads))
	}
	for _, entry := range heads {
		if entry.Name == "alpha" && entry.Version != 1 {
			t.Errorf("deleted fallback = v%d, want v1", entry.Version)
		}
	}

	limited, err := reg.ListScopeHeads(ctx, skillregistry.AdminScope(), 2)
	if err != nil {
		t.Fatalf("list limited scope heads: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("limited scope heads count = %d, want 2", len(limited))
	}
}

func publishVersions(
	t *testing.T, reg *skillregistry.Registry, ws *string, name string, count int, label string,
) {
	t.Helper()
	for version := 1; version <= count; version++ {
		publish(t, reg, ws, name, "Use when testing "+label+" version "+string(rune('a'+version))+".")
	}
}
