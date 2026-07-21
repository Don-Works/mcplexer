package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/skills"
	"github.com/don-works/mcplexer/internal/store"
)

// sessionScope returns a SkillScope spanning every workspace the session
// is currently rooted under (most-specific first) plus global skills.
// An ungated session (no workspace) gets a global-only scope.
func (h *handler) sessionScope(ctx context.Context) store.SkillScope {
	return store.SkillScope{WorkspaceIDs: h.readableWorkspaceIDs(ctx)}
}

// resolvePublishScope translates the agent-supplied scope arg into a
// concrete *string workspace_id. "auto" picks workspace if the session
// has one, else global. "global" forces nil. "workspace" forces the
// most-specific workspace. Empty falls back to "auto".
func (h *handler) resolvePublishScope(ctx context.Context, scope string) (*string, error) {
	return normalizeSkillPublishWorkspace(scope, h.currentWorkspaceID(ctx))
}

func normalizeSkillPublishWorkspace(scope, workspaceID string) (*string, error) {
	mode := strings.ToLower(strings.TrimSpace(scope))
	if mode == "" {
		mode = "auto"
	}
	switch mode {
	case "global":
		return nil, nil
	case "workspace":
		if workspaceID == "" || workspaceID == globalWorkspaceID {
			return nil, errors.New("scope=\"workspace\" but session is not rooted in a workspace")
		}
		return &workspaceID, nil
	case "auto":
		if workspaceID == "" || workspaceID == globalWorkspaceID {
			return nil, nil
		}
		return &workspaceID, nil
	default:
		return nil, fmt.Errorf("invalid scope %q: expected \"auto\", \"workspace\", or \"global\"", mode)
	}
}

// Compile-time check the routing import isn't dead — used by sessionScope.
var _ = routing.WorkspaceAncestor{}

// handleSkillSearch implements mcpx__skill_search.
func (h *handler) handleSkillSearch(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	if h.skillRegistry == nil {
		return marshalErrorResult("Skills registry is not enabled."), nil
	}
	var args struct {
		Query string `json:"query"`
		Q     string `json:"q"` // ergonomic alias — task__list and memory__recall use q
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.Query) == "" {
		args.Query = args.Q
	}
	v := newValidator()
	v.requireStringWithHint("query", args.Query,
		"pass a natural-language description of what you want to do (e.g. \"extract text from a PDF\"). To browse the catalog without searching, call mcpx__skill_list.")
	if env, ok := v.envelope(); ok {
		return env, nil
	}

	hits, err := h.skillRegistry.Search(ctx, h.sessionScope(ctx), args.Query, args.Limit)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}

	resp := skillSearchResponse{
		Query: args.Query,
		Count: len(hits),
		Hits:  make([]skillSearchHit, 0, len(hits)),
	}
	if len(hits) == 0 {
		resp.Hint = "No skills matched. The catalog may be small or your query may be too narrow — " +
			"call mcpx__skill_list to browse what's available."
	} else {
		resp.Hint = "Fetch a full body with mcpx__skill_get({name})."
		for _, hit := range hits {
			scope := hit.Scope
			if scope == "" {
				scope = "global"
			}
			resp.Hits = append(resp.Hits, skillSearchHit{
				Name:        hit.Name,
				Version:     hit.Version,
				Description: hit.Description,
				Score:       hit.Score,
				Scope:       scope,
				Reputation:  h.skillReputationLine(ctx, hit.Name),
			})
		}
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	return marshalToolResult(string(data)), nil
}

// handleSkillGet implements mcpx__skill_get.
func (h *handler) handleSkillGet(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	if h.skillRegistry == nil {
		return marshalErrorResult("Skills registry is not enabled."), nil
	}
	var args struct {
		Name           string          `json:"name"`
		Version        json.RawMessage `json:"version"`
		IncludeBundle  bool            `json:"include_bundle"`
		ExpandIncludes *bool           `json:"expand_includes"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	v := newValidator()
	v.requireStringWithHint("name", args.Name,
		"skill name as registered — call mcpx__skill_list to browse")

	var refVal any
	if len(args.Version) > 0 && string(args.Version) != "null" {
		if err := json.Unmarshal(args.Version, &refVal); err != nil {
			v.addFieldErr("invalid_value", "version", string(args.Version),
				fmt.Sprintf("invalid version: %v", err),
				"pass an integer version number or omit for latest")
		}
	}
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	ref, err := skillregistry.ParseVersionRef(refVal)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}

	entry, err := h.skillRegistry.Get(ctx, h.sessionScope(ctx), args.Name, ref)
	if errors.Is(err, store.ErrNotFound) {
		return marshalErrorResult(fmt.Sprintf("Skill %q not found.", args.Name)), nil
	}
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}

	resp := skillGetResponse{
		Name:        entry.Name,
		Version:     entry.Version,
		Author:      entry.Author,
		Body:        entry.Body,
		ContentHash: entry.ContentHash,
	}
	expandIncludes := args.ExpandIncludes == nil || *args.ExpandIncludes
	if expandIncludes {
		rendered, renderErr := h.skillRegistry.RenderEntry(ctx, entry)
		if renderErr != nil {
			return marshalErrorResult(fmt.Sprintf(
				"Skill composition failed: %v. Retry with expand_includes=false to inspect the raw body.",
				renderErr)), nil
		}
		resp.Body = rendered.Body
		if len(rendered.Provenance) > 0 {
			resp.RawBody = entry.Body
			resp.ExpandedSHA256 = rendered.SHA256
			resp.Provenance = rendered.Provenance
		}
	}
	if entry.BundleSHA256 != "" {
		resp.BundleSHA256 = entry.BundleSHA256
	}
	if extra := skillregistry.ExtraFromEntry(entry); !extra.IsZero() {
		resp.ManifestExtra = formatManifestExtra(extra)
	}

	if args.IncludeBundle && entry.BundleSHA256 != "" {
		bundle, sha, fetchErr := h.skillRegistry.FetchBundleForEntry(ctx, entry)
		if fetchErr != nil && !errors.Is(fetchErr, skillregistry.ErrBundleNotPresent) {
			return nil, &RPCError{Code: CodeInternalError, Message: fetchErr.Error()}
		}
		if len(bundle) > 0 {
			resp.BundleSHA256 = sha
			resp.BundleSize = len(bundle)
			resp.BundleB64 = base64.StdEncoding.EncodeToString(bundle)
		}
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	return marshalToolResult(string(data)), nil
}

// handleSkillExport implements mcpx__skill_export.
func (h *handler) handleSkillExport(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	if h.skillRegistry == nil {
		return marshalErrorResult("Skills registry is not enabled."), nil
	}
	var args struct {
		Name          string          `json:"name"`
		Version       json.RawMessage `json:"version"`
		IncludeBundle bool            `json:"include_bundle"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	v := newValidator()
	v.requireStringWithHint("name", args.Name,
		"skill name as registered — call mcpx__skill_list to browse")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	ref, err := parseSkillDiffVersionRef(args.Version, skillregistry.VersionRef{Latest: true})
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "version: " + err.Error()}
	}
	pkg, err := h.skillRegistry.ExportSkill(ctx, h.sessionScope(ctx), skillregistry.ExportOptions{
		Name:          args.Name,
		Version:       ref,
		IncludeBundle: args.IncludeBundle,
		ExportedBy:    h.clientTypeHint(),
	})
	if errors.Is(err, store.ErrNotFound) {
		return marshalErrorResult(fmt.Sprintf("Skill %q not found.", args.Name)), nil
	}
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	data, err := json.Marshal(pkg)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	return marshalToolResult(string(data)), nil
}

// handleSkillImport implements mcpx__skill_import.
func (h *handler) handleSkillImport(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	if h.skillRegistry == nil {
		return marshalErrorResult("Skills registry is not enabled."), nil
	}
	var args struct {
		Package     json.RawMessage `json:"package"`
		PackageJSON string          `json:"package_json"`
		DryRun      bool            `json:"dry_run"`
		Commit      bool            `json:"commit"`
		Scope       string          `json:"scope"`
		AuthorHint  string          `json:"author_hint"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	pkg, err := parseSkillSyncPackage(args.Package, args.PackageJSON)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	wsID, err := h.resolvePublishScope(ctx, args.Scope)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	author := strings.TrimSpace(args.AuthorHint)
	if author == "" {
		author = h.clientTypeHint()
	}
	plan, err := h.skillRegistry.ImportSkillPackage(ctx, skillregistry.ImportOptions{
		Package:     pkg,
		WorkspaceID: wsID,
		DryRun:      args.DryRun,
		Commit:      args.Commit,
		Author:      author,
	})
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Import failed: %v", err)), nil
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	return marshalToolResult(string(data)), nil
}

func parseSkillSyncPackage(raw json.RawMessage, rawString string) (skillregistry.SyncPackage, error) {
	var pkg skillregistry.SyncPackage
	switch {
	case len(raw) > 0 && string(raw) != "null":
		if err := json.Unmarshal(raw, &pkg); err != nil {
			return pkg, fmt.Errorf("package: %w", err)
		}
	case strings.TrimSpace(rawString) != "":
		if err := json.Unmarshal([]byte(rawString), &pkg); err != nil {
			return pkg, fmt.Errorf("package_json: %w", err)
		}
	default:
		return pkg, errors.New("package or package_json is required")
	}
	return pkg, nil
}

func (h *handler) clientTypeHint() string {
	if h == nil || h.sessions == nil {
		return ""
	}
	return h.sessions.clientType()
}

// handleSkillPublish implements mcpx__skill_publish.
func (h *handler) handleSkillPublish(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	if h.skillRegistry == nil {
		return marshalErrorResult("Skills registry is not enabled."), nil
	}
	var args struct {
		Name          string `json:"name"`
		Body          string `json:"body"`
		BodyB64       string `json:"body_b64"`
		ParentVersion *int   `json:"parent_version,omitempty"`
		AuthorHint    string `json:"author_hint"`
		Scope         string `json:"scope"`
		BundleB64     string `json:"bundle_b64"`
		SourcePath    string `json:"source_path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}

	var bundle []byte
	sourcePath := ""
	if strings.TrimSpace(args.SourcePath) != "" {
		if strings.TrimSpace(args.Body) != "" || strings.TrimSpace(args.BodyB64) != "" || strings.TrimSpace(args.BundleB64) != "" {
			return nil, &RPCError{
				Code:    CodeInvalidParams,
				Message: "source_path is mutually exclusive with body, body_b64, and bundle_b64",
			}
		}
		payload, err := h.loadSkillPublishSource(ctx, args.SourcePath)
		if err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("source_path: %v", err)}
		}
		if args.Name != "" && args.Name != payload.Name {
			return nil, &RPCError{
				Code:    CodeInvalidParams,
				Message: fmt.Sprintf("source_path frontmatter name %q does not match argument %q", payload.Name, args.Name),
			}
		}
		args.Name = payload.Name
		args.Body = payload.Body
		bundle = payload.Bundle
		sourcePath = payload.SourcePath
	}
	if strings.TrimSpace(args.BodyB64) != "" {
		if strings.TrimSpace(args.Body) != "" {
			return nil, &RPCError{Code: CodeInvalidParams, Message: "body and body_b64 are mutually exclusive"}
		}
		decoded, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(args.BodyB64))
		if decErr != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("body_b64: %v", decErr)}
		}
		args.Body = string(decoded)
	}

	v := newValidator()
	v.requireStringWithHint("body", args.Body,
		"the SKILL.md markdown body, body_b64, or source_path")
	if env, ok := v.envelope(); ok {
		return env, nil
	}

	author := strings.TrimSpace(args.AuthorHint)
	if author == "" {
		author = h.sessions.clientType()
	}

	wsID, err := h.resolvePublishScope(ctx, args.Scope)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}

	if args.BundleB64 != "" {
		decoded, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(args.BundleB64))
		if decErr != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("bundle_b64: %v", decErr)}
		}
		bundle = decoded
	}

	res, err := h.skillRegistry.Publish(ctx, skillregistry.PublishOptions{
		Name:             args.Name,
		Body:             args.Body,
		ParentVersion:    args.ParentVersion,
		Author:           author,
		CreatedByAgentID: h.sessions.sessionID(),
		WorkspaceID:      wsID,
		SourcePath:       sourcePath,
		Bundle:           bundle,
	})
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Publish failed: %v", err)), nil
	}

	data, err := json.Marshal(res)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	return marshalToolResult(string(data)), nil
}

// handleSkillList implements mcpx__skill_list.
func (h *handler) handleSkillList(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	if h.skillRegistry == nil {
		return marshalErrorResult("Skills registry is not enabled."), nil
	}
	var args struct {
		Name  string `json:"name"`
		Limit int    `json:"limit"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &args)
	}

	if strings.TrimSpace(args.Name) != "" {
		versions, err := h.skillRegistry.ListVersions(ctx, h.sessionScope(ctx), args.Name)
		if err != nil {
			return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
		}
		if len(versions) == 0 {
			return marshalErrorResult(fmt.Sprintf("Skill %q not found.", args.Name)), nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s — %d version(s):\n\n", args.Name, len(versions))
		for _, v := range versions {
			fmt.Fprintf(&b, "- v%d  (author: %s, %s)  %s\n",
				v.Version, displayAuthor(v.Author), v.PublishedAt.Format("2006-01-02"),
				truncate(v.Description, 80))
		}
		return marshalToolResult(b.String()), nil
	}

	heads, err := h.skillRegistry.ListHeads(ctx, h.sessionScope(ctx), args.Limit)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	if len(heads) == 0 {
		return marshalToolResult("Skills registry is empty. Publish your first skill with mcpx__skill_publish."), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d skill(s):\n\n", len(heads))
	for i := range heads {
		e := heads[i]
		scope := "global"
		if e.WorkspaceID != nil {
			scope = "ws:" + *e.WorkspaceID
		}
		fmt.Fprintf(&b, "- %s@%d  (%s · author: %s)  %s\n",
			e.Name, e.Version, scope, displayAuthor(e.Author), truncate(e.Description, 80))
		if extra := skillregistry.ExtraFromEntry(&e); !extra.IsZero() {
			fmt.Fprintf(&b, "    %s\n", formatManifestExtra(extra))
		}
		if rep := h.skillReputationLine(ctx, e.Name); rep != "" {
			fmt.Fprintf(&b, "    %s\n", rep)
		}
	}
	return marshalToolResult(b.String()), nil
}

// handleSkillDiff implements mcpx__skill_diff.
func (h *handler) handleSkillDiff(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	if h.skillRegistry == nil {
		return marshalErrorResult("Skills registry is not enabled."), nil
	}
	var args struct {
		Name       string          `json:"name"`
		OldVersion json.RawMessage `json:"old_version"`
		NewVersion json.RawMessage `json:"new_version"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	v := newValidator()
	v.requireStringWithHint("name", args.Name,
		"skill name as registered — call mcpx__skill_list to browse")
	if env, ok := v.envelope(); ok {
		return env, nil
	}

	oldRef, err := parseSkillDiffVersionRef(args.OldVersion, skillregistry.VersionRef{Version: 1})
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "old_version: " + err.Error()}
	}
	newRef, err := parseSkillDiffVersionRef(args.NewVersion, skillregistry.VersionRef{Latest: true})
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "new_version: " + err.Error()}
	}

	diff, err := h.skillRegistry.DiffVersions(ctx, h.sessionScope(ctx), args.Name, oldRef, newRef)
	if errors.Is(err, store.ErrNotFound) {
		return marshalErrorResult(fmt.Sprintf("Skill %q version not found.", args.Name)), nil
	}
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}

	data, err := json.Marshal(diff)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	return marshalToolResult(string(data)), nil
}

func parseSkillDiffVersionRef(raw json.RawMessage, fallback skillregistry.VersionRef) (skillregistry.VersionRef, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return fallback, nil
	}
	var val any
	if err := json.Unmarshal(raw, &val); err != nil {
		return skillregistry.VersionRef{}, err
	}
	return skillregistry.ParseVersionRef(val)
}

// skillReputationLine returns a compact "30d:N runs · X% success" tag
// the agent can scan to prefer high-reputation skills. Returns "" when
// the skill has zero runs in the window — silence is better than
// noise for new skills (W6 spec: "don't bloat the response").
//
// Uses the same 30d rolling window as the dashboard so the two surfaces
// agree at a glance. Failures upstream (e.g. store error) silently
// degrade to "" rather than failing the search/list response.
func (h *handler) skillReputationLine(ctx context.Context, name string) string {
	if h == nil || h.store == nil {
		return ""
	}
	since := time.Now().Add(-skillregistry.DefaultStatsWindow)
	runs, err := h.store.ListSkillRuns(ctx, store.SkillRunFilter{
		SkillName: name,
		Since:     &since,
		Limit:     1000,
	})
	if err != nil || len(runs) == 0 {
		return ""
	}
	stats := skillregistry.AggregateSkillRuns(runs, skillregistry.StatsOptions{
		Window: skillregistry.DefaultStatsWindow,
		Now:    time.Now(),
	})
	if stats.Invocations == 0 {
		return ""
	}
	pct := int(stats.SuccessRate*100 + 0.5)
	return fmt.Sprintf("rep: 30d=%d runs · %d%% success", stats.Invocations, pct)
}

func displayAuthor(a string) string {
	if a == "" {
		return "anonymous"
	}
	return a
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// handleSkillInventory implements mcpx__skill_inventory.
func (h *handler) handleSkillInventory(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	if h.skillRegistry == nil {
		return marshalErrorResult("Skills registry is not enabled."), nil
	}
	var args struct {
		Q          string   `json:"q"`
		Limit      int      `json:"limit"`
		SourceDirs []string `json:"source_dirs"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &args)
	}

	entries, err := h.skillRegistry.Inventory(ctx, skillregistry.InventoryOptions{
		Scope:      h.sessionScope(ctx),
		SourceDirs: args.SourceDirs,
		Query:      args.Q,
		Limit:      args.Limit,
	})
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	if entries == nil {
		entries = []skillregistry.InventoryEntry{}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d skill(s) in inventory:\n\n", len(entries))
	for i, e := range entries {
		fmt.Fprintf(&b, "%d. %s", i+1, e.Name)
		if e.Version > 0 {
			fmt.Fprintf(&b, "@v%d", e.Version)
		}
		fmt.Fprintf(&b, "  [%s", e.SourceKind)
		if e.Scope != "" {
			fmt.Fprintf(&b, " · %s", e.Scope)
		}
		if !e.Managed {
			fmt.Fprintf(&b, " · unmanaged")
		}
		if e.HasBundle {
			fmt.Fprintf(&b, " · bundle")
		}
		fmt.Fprintf(&b, "]")
		if e.Description != "" {
			fmt.Fprintf(&b, "  %s", truncate(e.Description, 80))
		}
		fmt.Fprintln(&b)
		if e.ParseError != "" {
			fmt.Fprintf(&b, "   parse_error: %s\n", e.ParseError)
		}
	}
	return marshalToolResult(b.String()), nil
}

// skillSearchHit is the code-mode-friendly projection of one search match.
type skillSearchHit struct {
	Name        string  `json:"name"`
	Version     int     `json:"version"`
	Description string  `json:"description"`
	Score       float64 `json:"score"`
	Scope       string  `json:"scope"`
	Reputation  string  `json:"reputation,omitempty"`
}

// skillSearchResponse is the structured mcpx__skill_search payload.
type skillSearchResponse struct {
	Query string           `json:"query"`
	Count int              `json:"count"`
	Hits  []skillSearchHit `json:"hits"`
	Hint  string           `json:"hint,omitempty"`
}

// skillGetResponse is the structured mcpx__skill_get payload. Body holds the
// rendered SKILL.md by default; RawBody preserves the verbatim registry source.
type skillGetResponse struct {
	Name           string                          `json:"name"`
	Version        int                             `json:"version"`
	Author         string                          `json:"author,omitempty"`
	Body           string                          `json:"body"`
	RawBody        string                          `json:"raw_body,omitempty"`
	ContentHash    string                          `json:"content_hash"`
	ExpandedSHA256 string                          `json:"expanded_sha256,omitempty"`
	Provenance     []skillregistry.CompositionEdge `json:"provenance,omitempty"`
	BundleSHA256   string                          `json:"bundle_sha256,omitempty"`
	BundleSize     int                             `json:"bundle_size,omitempty"`
	BundleB64      string                          `json:"bundle_b64,omitempty"`
	ManifestExtra  string                          `json:"manifest_extra,omitempty"`
}

// formatManifestExtra renders the W4 typed fields as a compact
// one-liner agents can scan in the mcpx__skill_get / mcpx__skill_search
// tool result. Mirrors `manifest_extra` JSON but trades brackets for
// shorthand (`req:binary=ffmpeg,env=API_KEY phases:discover,draft`).
func formatManifestExtra(e skills.ManifestExtra) string {
	parts := make([]string, 0, 6)
	if len(e.Requires) > 0 {
		reqs := make([]string, 0, len(e.Requires))
		for _, r := range e.Requires {
			switch r.Kind() {
			case "binary":
				reqs = append(reqs, "binary="+r.Binary)
			case "env":
				reqs = append(reqs, "env="+r.Env)
			case "scope":
				reqs = append(reqs, "scope="+r.Scope)
			}
		}
		if len(reqs) > 0 {
			parts = append(parts, "req:"+strings.Join(reqs, ","))
		}
	}
	if len(e.Produces) > 0 {
		parts = append(parts, "produces:"+strings.Join(e.Produces, ","))
	}
	if len(e.Consumes) > 0 {
		parts = append(parts, "consumes:"+strings.Join(e.Consumes, ","))
	}
	if len(e.Phases) > 0 {
		parts = append(parts, "phases:"+strings.Join(e.Phases, ","))
	}
	if r := e.EffectiveRefinement(); r != skills.RefinementEnabled {
		parts = append(parts, "refinement="+r)
	}
	if len(e.Includes) > 0 {
		includes := make([]string, 0, len(e.Includes))
		for _, include := range e.Includes {
			label := include.ID + "=" + include.Skill + fmt.Sprintf("@v%d", include.Version)
			if include.Section != "" {
				label += "#" + include.Section
			}
			includes = append(includes, label)
		}
		parts = append(parts, "includes:"+strings.Join(includes, ","))
	}
	return strings.Join(parts, " ")
}

func (h *handler) handleSkillPush(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	if h.skillRegistry == nil {
		return marshalErrorResult("Skills registry is not enabled."), nil
	}
	var args struct {
		Name          string          `json:"name"`
		Version       json.RawMessage `json:"version"`
		IncludeBundle bool            `json:"include_bundle"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	v := newValidator()
	v.requireStringWithHint("name", args.Name, "skill name as registered")

	var refVal any
	if len(args.Version) > 0 && string(args.Version) != "null" {
		if err := json.Unmarshal(args.Version, &refVal); err != nil {
			v.addFieldErr("invalid_value", "version", string(args.Version), fmt.Sprintf("invalid version: %v", err), "pass an integer or omit for latest")
		}
	}
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	ref, err := skillregistry.ParseVersionRef(refVal)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}

	pushRes, err := h.skillRegistry.Push(ctx, h.sessionScope(ctx), args.Name, ref)
	if errors.Is(err, store.ErrNotFound) {
		return marshalErrorResult(fmt.Sprintf("Skill %q not found.", args.Name)), nil
	}
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}

	data, err := json.Marshal(pushRes)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	return marshalToolResult(string(data)), nil
}

func (h *handler) handleSkillPull(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	if h.skillRegistry == nil {
		return marshalErrorResult("Skills registry is not enabled."), nil
	}
	var args struct {
		Name        string `json:"name"`
		Version     int    `json:"version"`
		DryRun      bool   `json:"dry_run"`
		Body        string `json:"body"`
		BundleB64   string `json:"bundle_b64"`
		ContentHash string `json:"content_hash"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	v := newValidator()
	v.requireStringWithHint("name", args.Name, "skill name to import")
	v.requireStringWithHint("body", args.Body, "SKILL.md body from hub peer")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	if err := skillregistry.CheckSyncPortableBody(args.Name, args.Body); err != nil {
		return marshalErrorResult(fmt.Sprintf("Pull failed: %v", err)), nil
	}

	scope := h.sessionScope(ctx)
	head, err := h.skillRegistry.Get(ctx, scope, args.Name, skillregistry.VersionRef{Latest: true})
	localExists := err == nil
	action := "new"
	if localExists {
		if head.ContentHash == args.ContentHash {
			action = "skip"
		} else {
			action = "update"
		}
	}

	if args.DryRun || action == "skip" {
		res := skillregistry.PullResult{
			Name:   args.Name,
			Action: action,
			DryRun: true,
		}
		data, err := json.Marshal(res)
		if err != nil {
			return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
		}
		return marshalToolResult(string(data)), nil
	}

	var bundle []byte
	if args.BundleB64 != "" {
		decoded, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(args.BundleB64))
		if decErr != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("bundle_b64: %v", decErr)}
		}
		bundle = decoded
	}

	wsID, _ := h.resolvePublishScope(ctx, "global")
	res, err := h.skillRegistry.Publish(ctx, skillregistry.PublishOptions{
		Name:               args.Name,
		Body:               args.Body,
		Author:             "hub-pull",
		CreatedByAgentID:   h.sessions.sessionID(),
		WorkspaceID:        wsID,
		SourceTypeOverride: "hub-pull",
		Bundle:             bundle,
	})
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Pull failed: %v", err)), nil
	}

	data, err := json.Marshal(res)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	return marshalToolResult(string(data)), nil
}
