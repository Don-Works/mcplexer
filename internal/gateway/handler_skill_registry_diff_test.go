package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newSkillRegistryHandler(t *testing.T) (*handler, *skillregistry.Registry) {
	t.Helper()
	db, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := skillregistry.New(db)
	h := &handler{store: db, skillRegistry: reg}
	h.sessions = newSessionManager(db, nil, TransportInternal, nil)
	return h, reg
}

const diffSkillBodyV1 = `---
name: diff-gateway
description: Use when testing skill diff gateway tool.
---
# Diff gateway v1
`

const diffSkillBodyV2 = `---
name: diff-gateway
description: Use when testing skill diff gateway tool (revised).
---
# Diff gateway v2
`

func TestSkillDiffGatewayTool(t *testing.T) {
	ctx := context.Background()
	h, reg := newSkillRegistryHandler(t)

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "diff-gateway", Body: diffSkillBodyV1, Author: "alice",
	}); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "diff-gateway", Body: diffSkillBodyV2, Author: "bob",
	}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{
		"name":        "diff-gateway",
		"old_version": 1,
		"new_version": 2,
	})
	resp, rpcErr := h.handleSkillDiff(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("handleSkillDiff: %v", rpcErr)
	}
	text := toolResultText(t, resp)
	if !strings.Contains(text, "body_diff") {
		t.Fatalf("expected body_diff in response: %s", text)
	}
	if !strings.Contains(text, `"old_version":1`) {
		t.Errorf("expected old_version=1 in %s", text)
	}
	if !strings.Contains(text, `"new_version":2`) {
		t.Errorf("expected new_version=2 in %s", text)
	}
}

func TestSkillSyncGatewayToolsExportAndDryRunImport(t *testing.T) {
	ctx := context.Background()
	h, reg := newSkillRegistryHandler(t)

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "sync-gateway", Body: strings.Replace(diffSkillBodyV1, "diff-gateway", "sync-gateway", 1),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	exportRaw, _ := json.Marshal(map[string]any{"name": "sync-gateway"})
	resp, rpcErr := h.handleSkillExport(ctx, exportRaw)
	if rpcErr != nil {
		t.Fatalf("handleSkillExport: %v", rpcErr)
	}
	var pkg skillregistry.SyncPackage
	if err := json.Unmarshal([]byte(toolResultText(t, resp)), &pkg); err != nil {
		t.Fatalf("unmarshal package: %v", err)
	}
	if pkg.Signature == "" || pkg.Name != "sync-gateway" {
		t.Fatalf("bad package: %+v", pkg)
	}

	importRaw, _ := json.Marshal(map[string]any{
		"package": pkg,
		"dry_run": true,
	})
	resp, rpcErr = h.handleSkillImport(ctx, importRaw)
	if rpcErr != nil {
		t.Fatalf("handleSkillImport: %v", rpcErr)
	}
	var plan skillregistry.SyncPlan
	if err := json.Unmarshal([]byte(toolResultText(t, resp)), &plan); err != nil {
		t.Fatalf("unmarshal plan: %v", err)
	}
	if plan.Action != skillregistry.SyncSkipped || plan.WouldMutate {
		t.Fatalf("same package should be skipped, got %+v", plan)
	}
}

func TestSkillPullDryRunRejectsCompositionBeforePlanning(t *testing.T) {
	ctx := context.Background()
	h, reg := newSkillRegistryHandler(t)
	body := `---
name: gateway-composed-remote
description: Composed remote gateway fixture.
includes:
  - id: fragment
    skill: missing-fragment
    scope: global
    version: 1
    content_hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
---
# Root

<!-- mcpx:include fragment -->
`
	raw, _ := json.Marshal(map[string]any{
		"name":         "gateway-composed-remote",
		"body":         body,
		"content_hash": skillregistry.ComputeContentHash(body),
		"dry_run":      true,
	})
	resp, rpcErr := h.handleSkillPull(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("handleSkillPull RPC error: %v", rpcErr)
	}
	var envelope struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal tool result: %v", err)
	}
	if !envelope.IsError || len(envelope.Content) == 0 {
		t.Fatalf("expected error tool result: %s", resp)
	}
	text := envelope.Content[0].Text
	if !strings.Contains(text, "dependency-closure transfer unsupported in protocol v1") {
		t.Fatalf("missing actionable portability error: %s", text)
	}
	heads, err := reg.ListHeads(ctx, skillregistry.GlobalScope(), 0)
	if err != nil {
		t.Fatalf("list heads: %v", err)
	}
	if len(heads) != 0 {
		t.Fatalf("dry-run rejection mutated registry: %+v", heads)
	}
}

func TestSkillSearchReturnsStructuredJSON(t *testing.T) {
	ctx := context.Background()
	h, reg := newSkillRegistryHandler(t)

	body := `---
name: pdf-text
description: Extract text from PDF files for downstream parsing.
---
# PDF text
`
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "pdf-text", Body: body, Author: "alice",
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{"query": "extract pdf text", "limit": 3})
	resp, rpcErr := h.handleSkillSearch(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("handleSkillSearch: %v", rpcErr)
	}
	var parsed skillSearchResponse
	if err := json.Unmarshal([]byte(toolResultText(t, resp)), &parsed); err != nil {
		t.Fatalf("unmarshal search payload: %v", err)
	}
	if parsed.Count != 1 || len(parsed.Hits) != 1 {
		t.Fatalf("unexpected hits: %+v", parsed)
	}
	if parsed.Hits[0].Name != "pdf-text" {
		t.Fatalf("hit name = %q", parsed.Hits[0].Name)
	}
}

func TestSkillGetReturnsStructuredJSON(t *testing.T) {
	ctx := context.Background()
	h, reg := newSkillRegistryHandler(t)

	body := `---
name: using-mcplexer
description: Use when operating the mcplexer gateway.
---
# Using mcplexer
`
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "using-mcplexer", Body: body, Author: "alice",
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{"name": "using-mcplexer"})
	resp, rpcErr := h.handleSkillGet(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("handleSkillGet: %v", rpcErr)
	}
	var parsed skillGetResponse
	if err := json.Unmarshal([]byte(toolResultText(t, resp)), &parsed); err != nil {
		t.Fatalf("unmarshal get payload: %v", err)
	}
	if parsed.Name != "using-mcplexer" || parsed.Version != 1 {
		t.Fatalf("unexpected identity: %+v", parsed)
	}
	if !strings.Contains(parsed.Body, "# Using mcplexer") {
		t.Fatalf("body missing markdown: %q", parsed.Body)
	}
	if parsed.ContentHash != skillregistry.ComputeContentHash(body) {
		t.Fatalf("content hash missing from response: %+v", parsed)
	}
	if parsed.RawBody != "" || parsed.ExpandedSHA256 != "" || len(parsed.Provenance) != 0 {
		t.Fatalf("plain skill duplicated raw/expanded fields: %+v", parsed)
	}
	payload := toolResultText(t, resp)
	if strings.Contains(payload, `"raw_body"`) || strings.Contains(payload, `"expanded_sha256"`) || strings.Contains(payload, `"provenance"`) {
		t.Fatalf("plain skill response contains omitted composition fields: %s", payload)
	}
}

func TestSkillGetExpandsPinnedIncludesByDefaultAndCanReturnRaw(t *testing.T) {
	ctx := context.Background()
	h, reg := newSkillRegistryHandler(t)
	targetBody := "---\nname: gateway-fragment\ndescription: Neutral fragment.\n---\nFRAGMENT CONTENT\n"
	target, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "gateway-fragment", Body: targetBody})
	if err != nil {
		t.Fatalf("publish target: %v", err)
	}
	rootBody := fmt.Sprintf(`---
name: gateway-composed
description: Neutral composed fixture.
includes:
  - id: fragment
    skill: gateway-fragment
    scope: global
    version: %d
    content_hash: %q
---
ROOT BEFORE
<!-- mcpx:include fragment -->
ROOT AFTER
`, target.Version, target.ContentHash)
	root, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "gateway-composed", Body: rootBody})
	if err != nil {
		t.Fatalf("publish root: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{"name": "gateway-composed"})
	resp, rpcErr := h.handleSkillGet(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("handleSkillGet expanded: %v", rpcErr)
	}
	var expanded skillGetResponse
	if err := json.Unmarshal([]byte(toolResultText(t, resp)), &expanded); err != nil {
		t.Fatalf("unmarshal expanded response: %v", err)
	}
	if !strings.Contains(expanded.Body, "FRAGMENT CONTENT") || strings.Contains(expanded.Body, "mcpx:include") {
		t.Fatalf("body was not expanded: %s", expanded.Body)
	}
	if expanded.RawBody != rootBody || expanded.ContentHash != root.ContentHash {
		t.Fatalf("raw audit fields incorrect: %+v", expanded)
	}
	if expanded.ExpandedSHA256 != skillregistry.ComputeContentHash(expanded.Body) || len(expanded.Provenance) != 1 {
		t.Fatalf("expanded audit fields incorrect: %+v", expanded)
	}

	raw, _ = json.Marshal(map[string]any{"name": "gateway-composed", "expand_includes": false})
	resp, rpcErr = h.handleSkillGet(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("handleSkillGet raw: %v", rpcErr)
	}
	var stored skillGetResponse
	if err := json.Unmarshal([]byte(toolResultText(t, resp)), &stored); err != nil {
		t.Fatalf("unmarshal raw response: %v", err)
	}
	if stored.Body != rootBody || stored.RawBody != "" || stored.ExpandedSHA256 != "" || len(stored.Provenance) != 0 {
		t.Fatalf("expand_includes=false did not return raw-only body: %+v", stored)
	}
}

func TestSkillPublishAcceptsBodyB64(t *testing.T) {
	ctx := context.Background()
	h, reg := newSkillRegistryHandler(t)
	body := `---
name: b64-skill
description: Use when testing base64 publish input.
---
# Encoded body
`

	raw, _ := json.Marshal(map[string]any{
		"body_b64": base64.StdEncoding.EncodeToString([]byte(body)),
	})
	resp, rpcErr := h.handleSkillPublish(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("handleSkillPublish: %v", rpcErr)
	}
	var published skillregistry.PublishResult
	if err := json.Unmarshal([]byte(toolResultText(t, resp)), &published); err != nil {
		t.Fatalf("unmarshal publish result: %v", err)
	}
	if published.Name != "b64-skill" || published.Version != 1 {
		t.Fatalf("unexpected publish result: %+v", published)
	}
	entry, err := reg.Get(ctx, skillregistry.AdminScope(), "b64-skill", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("get published skill: %v", err)
	}
	if entry.Body != body {
		t.Fatalf("published body mismatch: %q", entry.Body)
	}
}

func TestSkillPublishFromRelativeSourcePathFile(t *testing.T) {
	ctx := context.Background()
	h, reg := newSkillRegistryHandler(t)
	root := t.TempDir()
	h.sessions.clientPath = root
	body := `---
name: source-file-skill
description: Use when testing source_path SKILL.md file publish.
---
# Source file body
`
	writeFile(t, filepath.Join(root, "SKILL.md"), body)

	raw, _ := json.Marshal(map[string]any{"source_path": "SKILL.md"})
	resp, rpcErr := h.handleSkillPublish(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("handleSkillPublish: %v", rpcErr)
	}
	var published skillregistry.PublishResult
	if err := json.Unmarshal([]byte(toolResultText(t, resp)), &published); err != nil {
		t.Fatalf("unmarshal publish result: %v", err)
	}
	if published.Name != "source-file-skill" || published.BundleSHA256 != "" {
		t.Fatalf("unexpected publish result: %+v", published)
	}
	entry, err := reg.Get(ctx, skillregistry.AdminScope(), "source-file-skill", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("get published skill: %v", err)
	}
	if entry.SourcePath != filepath.Join(root, "SKILL.md") || entry.SourceType != "path" {
		t.Fatalf("unexpected source metadata: type=%q path=%q", entry.SourceType, entry.SourcePath)
	}
}

func TestSkillPublishFromSourcePathDirectoryBundlesSidecars(t *testing.T) {
	ctx := context.Background()
	h, reg := newSkillRegistryHandler(t)
	root := t.TempDir()
	h.sessions.clientPath = root
	dir := filepath.Join(root, "dir-skill")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `---
name: source-dir-skill
description: Use when testing source_path directory bundle publish.
---
# Source dir body
`
	writeFile(t, filepath.Join(dir, "SKILL.md"), body)
	writeFile(t, filepath.Join(dir, "scripts", "run.sh"), "#!/bin/sh\n")

	raw, _ := json.Marshal(map[string]any{"source_path": "dir-skill"})
	resp, rpcErr := h.handleSkillPublish(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("handleSkillPublish: %v", rpcErr)
	}
	var published skillregistry.PublishResult
	if err := json.Unmarshal([]byte(toolResultText(t, resp)), &published); err != nil {
		t.Fatalf("unmarshal publish result: %v", err)
	}
	if published.Name != "source-dir-skill" || published.BundleSHA256 == "" || published.BundleSize == 0 {
		t.Fatalf("unexpected publish result: %+v", published)
	}
	entry, err := reg.Get(ctx, skillregistry.AdminScope(), "source-dir-skill", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("get published skill: %v", err)
	}
	if entry.SourceType != "bundle" || entry.SourcePath != dir {
		t.Fatalf("unexpected source metadata: type=%q path=%q", entry.SourceType, entry.SourcePath)
	}
	bundle, sha, err := reg.FetchBundle(ctx, skillregistry.AdminScope(), "source-dir-skill", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("fetch bundle: %v", err)
	}
	if len(bundle) == 0 || sha != published.BundleSHA256 {
		t.Fatalf("unexpected bundle len=%d sha=%q", len(bundle), sha)
	}
}

func TestSkillPublishSourcePathRejectsOutsideRoot(t *testing.T) {
	ctx := context.Background()
	h, _ := newSkillRegistryHandler(t)
	h.sessions.clientPath = t.TempDir()
	outside := t.TempDir()
	body := `---
name: outside-source-skill
description: Use when testing source path root rejection.
---
# Outside
`
	writeFile(t, filepath.Join(outside, "SKILL.md"), body)

	raw, _ := json.Marshal(map[string]any{"source_path": filepath.Join(outside, "SKILL.md")})
	_, rpcErr := h.handleSkillPublish(ctx, raw)
	if rpcErr == nil {
		t.Fatal("expected RPCError for source_path outside allowed roots")
	}
	if !strings.Contains(rpcErr.Message, "must be under the current client/workspace root") {
		t.Fatalf("unexpected error: %s", rpcErr.Message)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func toolResultText(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var env struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal tool result: %v", err)
	}
	if env.IsError {
		t.Fatalf("tool error: %s", env.Content[0].Text)
	}
	if len(env.Content) == 0 {
		t.Fatal("empty tool result")
	}
	return env.Content[0].Text
}
