package control

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/gateway"
	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

func TestSkillRegistryAdminScopeAwarePublishListGetDelete(t *testing.T) {
	db := newTestDB(t)
	workspace := seedWorkspace(t, db)
	backend := NewInternalBackend(db, nil)
	backend.SetSkillRegistry(skillregistry.New(db))

	callAdminPublish(t, backend, "admin-scope", "global copy", nil)
	callAdminPublish(t, backend, "admin-scope", "workspace copy", &workspace.ID)

	listText := callAdminTool(t, backend, "list_skill_registry", map[string]any{"view": "scope_heads"})
	var heads []store.SkillRegistryEntry
	if err := json.Unmarshal([]byte(listText), &heads); err != nil {
		t.Fatalf("decode scope heads: %v", err)
	}
	if len(heads) != 2 {
		t.Fatalf("scope head count = %d, want 2", len(heads))
	}

	getText := callAdminTool(t, backend, "get_skill_registry", map[string]any{
		"name": "admin-scope", "workspace_id": workspace.ID,
	})
	var got store.SkillRegistryEntry
	if err := json.Unmarshal([]byte(getText), &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if got.WorkspaceID == nil || *got.WorkspaceID != workspace.ID || !strings.Contains(got.Body, "workspace copy") {
		t.Fatalf("workspace get = %+v", got)
	}

	callAdminTool(t, backend, "delete_skill_registry", map[string]any{
		"name": "admin-scope", "workspace_id": workspace.ID,
	})
	globalText := callAdminTool(t, backend, "get_skill_registry", map[string]any{"name": "admin-scope"})
	if !strings.Contains(globalText, "global copy") {
		t.Fatalf("global skill did not survive workspace delete: %s", globalText)
	}
}

func TestSkillRegistryAdminExplicitGetUsesDeterministicAdminScope(t *testing.T) {
	db := newTestDB(t)
	reg := skillregistry.New(db)
	backend := NewInternalBackend(db, nil)
	backend.SetSkillRegistry(reg)
	ctx := context.Background()
	workspaceZ := "workspace-z"
	workspaceA := "workspace-a"
	name := "admin-explicit-scope"
	for _, fixture := range []struct {
		workspace *string
		content   string
	}{
		{content: "global copy"},
		{workspace: &workspaceZ, content: "workspace z copy"},
		{workspace: &workspaceA, content: "workspace a copy"},
	} {
		if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
			Name: name, Body: adminSkillBody(name, fixture.content), WorkspaceID: fixture.workspace,
		}); err != nil {
			t.Fatalf("publish %s: %v", fixture.content, err)
		}
	}

	text := callAdminTool(t, backend, "get_skill_registry", map[string]any{"name": name, "version": 1})
	var got store.SkillRegistryEntry
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("decode unscoped explicit get: %v", err)
	}
	if got.WorkspaceID == nil || *got.WorkspaceID != workspaceA || !strings.Contains(got.Body, "workspace a copy") {
		t.Fatalf("admin tie precedence = %+v, want lexicographically first workspace", got)
	}
	text = callAdminTool(t, backend, "get_skill_registry", map[string]any{"name": name})
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("decode unscoped latest get: %v", err)
	}
	if got.WorkspaceID == nil || *got.WorkspaceID != workspaceA {
		t.Fatalf("admin latest tie precedence = %+v, want lexicographically first workspace", got)
	}

	text = callAdminTool(t, backend, "get_skill_registry", map[string]any{
		"name": name, "version": 1, "workspace_id": workspaceZ,
	})
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("decode exact workspace get: %v", err)
	}
	if got.WorkspaceID == nil || *got.WorkspaceID != workspaceZ || !strings.Contains(got.Body, "workspace z copy") {
		t.Fatalf("exact workspace get = %+v", got)
	}

	workspaceOnly := "admin-workspace-only"
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: workspaceOnly, Body: adminSkillBody(workspaceOnly, "workspace-only copy"), WorkspaceID: &workspaceZ,
	}); err != nil {
		t.Fatalf("publish workspace-only: %v", err)
	}
	text = callAdminTool(t, backend, "get_skill_registry", map[string]any{"name": workspaceOnly, "version": 1})
	if !strings.Contains(text, "workspace-only copy") {
		t.Fatalf("unscoped explicit get missed workspace-only row: %s", text)
	}
}

func TestSkillRegistryDeleteHasNoDirectStoreFallback(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	body := adminSkillBody("guarded-admin-delete", "must survive")
	if _, err := db.PublishSkillRegistryEntry(ctx, &store.SkillRegistryEntry{
		Name: "guarded-admin-delete", ContentHash: skillregistry.ComputeContentHash(body),
		Description: "Use when testing admin registry tools.", Body: body,
		MetadataJSON: json.RawMessage(`{}`), TagsJSON: json.RawMessage(`[]`),
	}); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	backend := NewInternalBackend(db, nil) // registry intentionally not wired
	raw, _ := json.Marshal(map[string]any{"name": "guarded-admin-delete", "version": 1})
	result, err := backend.Call(ctx, "delete_skill_registry", raw)
	if err != nil {
		t.Fatalf("delete call: %v", err)
	}
	text, isError := parseToolResult(t, result)
	if !isError || !strings.Contains(text, "skill registry not initialised") {
		t.Fatalf("nil-registry delete result: isError=%v text=%s", isError, text)
	}
	if _, err := db.GetSkillRegistryEntry(ctx, nil, "guarded-admin-delete", 1); err != nil {
		t.Fatalf("nil-registry fallback mutated row: %v", err)
	}

	for _, tool := range dispatchableTools(allTools()) {
		if tool.Name == "delete_skill_registry" {
			t.Fatal("standalone control server still advertises direct-store registry delete")
		}
	}
	server := New(db, false)
	callArgs, _ := json.Marshal(gateway.CallToolRequest{Name: "delete_skill_registry", Arguments: raw})
	if _, rpcErr := server.handleToolsCall(ctx, callArgs); rpcErr == nil || rpcErr.Code != gateway.CodeMethodNotFound {
		t.Fatalf("standalone direct delete remained dispatchable: %+v", rpcErr)
	}
}

func TestSkillRegistryAdminRejectsLiteralGlobalPublishAndAudits(t *testing.T) {
	db := newTestDB(t)
	backend := NewInternalBackend(db, nil)
	backend.SetSkillRegistry(skillregistry.New(db))
	body := adminSkillBody("literal-sentinel", "content")
	raw, _ := json.Marshal(map[string]any{"body": body, "workspace_id": "global"})
	result, err := backend.Call(context.Background(), "publish_skill_registry", raw)
	if err != nil {
		t.Fatalf("publish call: %v", err)
	}
	text, isError := parseToolResult(t, result)
	if !isError || !strings.Contains(text, "reserved") {
		t.Fatalf("literal global result: isError=%v text=%s", isError, text)
	}

	callAdminPublish(t, backend, "audit-admin", "content", nil)
	auditText := callAdminTool(t, backend, "audit_skill_registry", map[string]any{"include_info": true})
	var report skillregistry.AuditReport
	if err := json.Unmarshal([]byte(auditText), &report); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if report.ScopeHeadCount != 1 || report.IssueCount == 0 {
		t.Fatalf("unexpected audit report: %+v", report)
	}
}

func TestSkillRegistryAdminRejectsReservedManifestMetadataExtra(t *testing.T) {
	db := newTestDB(t)
	backend := NewInternalBackend(db, nil)
	backend.SetSkillRegistry(skillregistry.New(db))
	name := "reserved-manifest-extra"
	raw, _ := json.Marshal(map[string]any{
		"body": adminSkillBody(name, "must not publish"),
		"metadata_extras": map[string]any{
			skillregistry.ManifestExtraStashKey: map[string]any{
				"includes": []any{map[string]any{"id": "unvalidated"}},
			},
		},
	})
	result, err := backend.Call(context.Background(), "publish_skill_registry", raw)
	if err != nil {
		t.Fatalf("publish call: %v", err)
	}
	text, isError := parseToolResult(t, result)
	if !isError || !strings.Contains(text, "metadata_extras.__manifest_extra is reserved") ||
		!strings.Contains(text, "validated SKILL.md frontmatter") {
		t.Fatalf("reserved metadata result: isError=%v text=%s", isError, text)
	}
	if _, err := db.GetSkillRegistryEntry(context.Background(), nil, name, 1); err == nil {
		t.Fatal("admin publish accepted reserved manifest metadata")
	}
}

func TestSkillRegistryAdminPublishPreservesBundleAndProvenance(t *testing.T) {
	db := newTestDB(t)
	backend := NewInternalBackend(db, nil)
	backend.SetSkillRegistry(skillregistry.New(db))
	body := adminSkillBody("bundled-admin", "bundle fixture content")
	bundle := makeAdminBundle(t, body, "asset fixture content")
	resultText := callAdminTool(t, backend, "publish_skill_registry", map[string]any{
		"body": body, "bundle_b64": base64.StdEncoding.EncodeToString(bundle),
		"source_type": "bundle", "source_path": "/tmp/bundled-admin-source",
		"metadata_extras": map[string]any{"provenance": map[string]any{"origin": "fixture"}},
	})
	if strings.Contains(resultText, "asset fixture content") || strings.Contains(resultText, body) {
		t.Fatalf("publish result exposed body or bundle content: %s", resultText)
	}
	var result skillregistry.PublishResult
	if err := json.Unmarshal([]byte(resultText), &result); err != nil {
		t.Fatalf("decode publish result: %v", err)
	}
	if result.BundleSize != len(bundle) || result.BundleSHA256 == "" {
		t.Fatalf("bundle result = %+v", result)
	}
	entry, err := db.GetSkillRegistryEntry(context.Background(), nil, "bundled-admin", 1)
	if err != nil {
		t.Fatalf("get published entry: %v", err)
	}
	if entry.SourceType != "bundle" || entry.SourcePath != "/tmp/bundled-admin-source" ||
		!strings.Contains(string(entry.MetadataJSON), `"origin":"fixture"`) {
		t.Fatalf("provenance not preserved: %+v", entry)
	}
	stored, sha, err := db.GetSkillRegistryBundle(context.Background(), nil, "bundled-admin", 1)
	if err != nil || !bytes.Equal(stored, bundle) || sha != result.BundleSHA256 {
		t.Fatalf("stored bundle mismatch: size=%d sha=%q err=%v", len(stored), sha, err)
	}
}

func TestAdminSkillBodyBase64SizeBound(t *testing.T) {
	oversized := strings.Repeat("A", base64.StdEncoding.EncodedLen(skillregistry.MaxBodyBytes)+1)
	if _, err := decodeAdminSkillBody("", oversized); err == nil || !strings.Contains(err.Error(), "decoded limit") {
		t.Fatalf("oversized body_b64 error = %v", err)
	}
}

func TestSkillRegistryAdminDeleteInvalidatesCacheAndRepairsStable(t *testing.T) {
	t.Run("global", func(t *testing.T) {
		exerciseAdminDeleteInvalidation(t, nil)
	})
	t.Run("exact workspace", func(t *testing.T) {
		db := newTestDB(t)
		workspace := seedWorkspace(t, db)
		exerciseAdminDeleteInvalidationWithDB(t, db, &workspace.ID)
	})
}

func exerciseAdminDeleteInvalidation(t *testing.T, workspaceID *string) {
	t.Helper()
	exerciseAdminDeleteInvalidationWithDB(t, newTestDB(t), workspaceID)
}

func exerciseAdminDeleteInvalidationWithDB(
	t *testing.T, db store.Store, workspaceID *string,
) {
	t.Helper()
	reg := skillregistry.New(db)
	backend := NewInternalBackend(db, nil)
	backend.SetSkillRegistry(reg)
	ctx := context.Background()
	name := "delete-cache-global"
	scope := skillregistry.GlobalScope()
	if workspaceID != nil {
		name = "delete-cache-workspace"
		scope = store.SkillScope{WorkspaceIDs: []string{*workspaceID}}
	}
	for _, content := range []string{"version one", "version two"} {
		if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
			Body: adminSkillBody(name, content), WorkspaceID: workspaceID,
		}); err != nil {
			t.Fatalf("publish %s: %v", content, err)
		}
	}
	if err := reg.SetTag(ctx, scope, name, "@stable", 2, "test"); err != nil {
		t.Fatalf("set stable: %v", err)
	}
	assertRegistryVersion(t, reg, scope, name, 2)
	args := map[string]any{"name": name, "version": 2}
	if workspaceID != nil {
		args["workspace_id"] = *workspaceID
	}
	callAdminTool(t, backend, "delete_skill_registry", args)
	assertRegistryVersion(t, reg, scope, name, 1)
}

func assertRegistryVersion(
	t *testing.T, reg *skillregistry.Registry, scope store.SkillScope, name string, want int,
) {
	t.Helper()
	ctx := context.Background()
	hits, err := reg.Search(ctx, scope, name, 5)
	if err != nil || len(hits) == 0 || hits[0].Version != want {
		t.Fatalf("search head: hits=%+v err=%v, want v%d", hits, err, want)
	}
	for label, ref := range map[string]skillregistry.VersionRef{
		"latest": {Latest: true}, "stable": {Stable: true},
	} {
		entry, getErr := reg.Get(ctx, scope, name, ref)
		if getErr != nil || entry.Version != want {
			t.Fatalf("%s: entry=%+v err=%v, want v%d", label, entry, getErr, want)
		}
	}
}

func callAdminPublish(
	t *testing.T, backend *InternalBackend, name, content string, workspaceID *string,
) {
	t.Helper()
	args := map[string]any{"body": adminSkillBody(name, content)}
	if workspaceID != nil {
		args["workspace_id"] = *workspaceID
	}
	callAdminTool(t, backend, "publish_skill_registry", args)
}

func adminSkillBody(name, content string) string {
	return "---\nname: " + name + "\ndescription: Use when testing admin registry tools.\n---\n# Skill\n\n" + content + "\n"
}

func callAdminTool(t *testing.T, backend *InternalBackend, name string, args any) string {
	t.Helper()
	raw, _ := json.Marshal(args)
	result, err := backend.Call(context.Background(), name, raw)
	if err != nil {
		t.Fatalf("%s call: %v", name, err)
	}
	text, isError := parseToolResult(t, result)
	if isError {
		t.Fatalf("%s error: %s", name, text)
	}
	return text
}

func makeAdminBundle(t *testing.T, skillBody, assetBody string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzipWriter := gzip.NewWriter(&buf)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range map[string]string{
		"bundled-admin/SKILL.md":           skillBody,
		"bundled-admin/assets/fixture.txt": assetBody,
	} {
		header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatalf("write tar content: %v", err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}
