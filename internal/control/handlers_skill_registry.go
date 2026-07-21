package control

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

// Skills-registry admin handlers. Visible only when the agent's CWD is
// at or under the data dir (CWD-gated by the gateway via the mcplexer__
// prefix). The universal mcpx__skill_search/get/publish/list surface
// lives in internal/gateway and is reachable from anywhere.

func handleListSkillRegistry(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	var p struct {
		Name           string `json:"name"`
		IncludeDeleted bool   `json:"include_deleted"`
		Limit          int    `json:"limit"`
		View           string `json:"view"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &p)
	}
	if p.Name != "" {
		versions, err := s.ListSkillRegistryVersions(ctx, store.SkillScope{IncludeAll: true}, p.Name, p.IncludeDeleted)
		if err != nil {
			return nil, fmt.Errorf("list versions: %w", err)
		}
		return jsonResult(versions)
	}
	view := strings.ToLower(strings.TrimSpace(p.View))
	if view == "" || view == "effective" {
		heads, err := s.ListSkillRegistryHeads(ctx, store.SkillScope{IncludeAll: true}, p.Limit)
		if err != nil {
			return nil, fmt.Errorf("list effective heads: %w", err)
		}
		return jsonResult(heads)
	}
	if view != "scope_heads" {
		return nil, fmt.Errorf("invalid view %q (expected effective or scope_heads)", p.View)
	}
	heads, err := s.ListSkillRegistryScopeHeads(ctx, store.SkillScope{IncludeAll: true}, p.Limit)
	if err != nil {
		return nil, fmt.Errorf("list scope heads: %w", err)
	}
	return jsonResult(heads)
}

func handleGetSkillRegistry(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	return runGetSkillRegistry(ctx, s, nil, args)
}

func handleGetSkillRegistryWithRegistry(reg *skillregistry.Registry) handlerFunc {
	return func(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
		return runGetSkillRegistry(ctx, s, reg, args)
	}
}

func runGetSkillRegistry(
	ctx context.Context,
	s store.Store,
	reg *skillregistry.Registry,
	args json.RawMessage,
) (json.RawMessage, error) {
	var p struct {
		Name        string  `json:"name"`
		Version     int     `json:"version"`
		WorkspaceID *string `json:"workspace_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	var v validator
	v.requireString("name", p.Name,
		"skill name as registered — call list_skill_registry to browse")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	var (
		entry *store.SkillRegistryEntry
		err   error
	)
	workspaceID, scopeErr := cleanOptionalWorkspaceID(p.WorkspaceID)
	if scopeErr != nil {
		return nil, scopeErr
	}
	if workspaceID == nil {
		if reg == nil {
			// Standalone control remains read-capable without a separately wired
			// registry. Registry.Get supplies the same deterministic AdminScope
			// precedence as the integrated backend.
			reg = skillregistry.New(s)
		}
		ref := skillregistry.VersionRef{Latest: true}
		if p.Version > 0 {
			ref = skillregistry.VersionRef{Version: p.Version}
		}
		entry, err = reg.Get(ctx, skillregistry.AdminScope(), p.Name, ref)
	} else if p.Version > 0 {
		entry, err = s.GetSkillRegistryEntry(ctx, workspaceID, p.Name, p.Version)
	} else {
		entry, err = getExactWorkspaceHead(ctx, s, *workspaceID, p.Name)
	}
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}
	return jsonResult(entry)
}

type skillRegistryDeleteFunc func(context.Context, *string, string, int) error

func runDeleteSkillRegistry(
	ctx context.Context, args json.RawMessage, deleteEntry skillRegistryDeleteFunc,
) (json.RawMessage, error) {
	var p struct {
		Name        string  `json:"name"`
		Version     int     `json:"version"`
		WorkspaceID *string `json:"workspace_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	var v validator
	v.requireString("name", p.Name,
		"skill name to delete — call list_skill_registry to browse")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	workspaceID, scopeErr := cleanOptionalWorkspaceID(p.WorkspaceID)
	if scopeErr != nil {
		return nil, scopeErr
	}
	delErr := deleteEntry(ctx, workspaceID, p.Name, p.Version)
	if delErr != nil {
		return nil, fmt.Errorf("delete: %w", delErr)
	}
	return textResult("deleted"), nil
}

func handleDeleteSkillRegistryWithRegistry(reg *skillregistry.Registry) handlerFunc {
	return func(ctx context.Context, _ store.Store, args json.RawMessage) (json.RawMessage, error) {
		return runDeleteSkillRegistry(ctx, args, reg.SoftDelete)
	}
}

func cleanOptionalWorkspaceID(workspaceID *string) (*string, error) {
	if workspaceID == nil {
		return nil, nil
	}
	clean := strings.TrimSpace(*workspaceID)
	if clean == "" {
		return nil, fmt.Errorf("workspace_id must be non-empty when provided")
	}
	return &clean, nil
}

func getExactWorkspaceHead(
	ctx context.Context, s store.Store, workspaceID, name string,
) (*store.SkillRegistryEntry, error) {
	entry, err := s.GetSkillRegistryHead(ctx, store.SkillScope{WorkspaceIDs: []string{workspaceID}}, name)
	if err != nil {
		return nil, err
	}
	if entry.WorkspaceID != nil && *entry.WorkspaceID == workspaceID {
		return entry, nil
	}
	return nil, store.ErrNotFound
}

func handleSetSkillRegistryTag(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	var p struct {
		Name    string `json:"name"`
		Tag     string `json:"tag"`
		Version int    `json:"version"`
		SetBy   string `json:"set_by"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	var v validator
	v.requireString("name", p.Name,
		"skill name to tag — call list_skill_registry to browse")
	v.requireString("tag", p.Tag,
		"tag to set (e.g. \"stable\", \"beta\") — NOT \"latest\" (that's derived)")
	if p.Version <= 0 {
		v.errs = append(v.errs, fieldError{
			Code:    "invalid_value",
			Field:   "version",
			Value:   fmt.Sprintf("%d", p.Version),
			Message: "version must be a positive integer",
			Hint:    "use the integer version returned by mcpx__skill_publish",
		})
	}
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	if p.Tag == "@latest" || p.Tag == "latest" {
		return nil, fmt.Errorf("@latest is derived, not stored")
	}
	if _, err := s.GetSkillRegistryEntry(ctx, nil, p.Name, p.Version); err != nil {
		return nil, fmt.Errorf("verify target: %w", err)
	}
	if err := s.SetSkillRegistryTag(ctx, &store.SkillRegistryTag{
		Name: p.Name, Tag: p.Tag, Version: p.Version, SetBy: p.SetBy,
	}); err != nil {
		return nil, fmt.Errorf("set tag: %w", err)
	}
	return textResult("ok"), nil
}

// handleImportSkillRegistryDir ingests a local directory as a "path"
// source skill — the SKILL.md text is read into the body for search,
// and source_path points at the on-disk bundle so agents can read
// scripts/, reference/ and other assets.
//
// Two shapes are accepted:
//
//  1. path = "/abs/path/to/skill"  — that directory contains SKILL.md
//     directly. Imports as one skill.
//
//  2. path = "/abs/path/to/skill/<name>.md"  — single-file skill, no
//     bundle. Imports as inline (source_type=inline).
//
// The agent can pass scope (auto/workspace/global; default global for
// admin imports), workspace_id (only meaningful with scope=workspace),
// and author. For directory imports the bundle path stays where it is —
// we don't copy. Files outside SKILL.md/scripts/reference/assets are
// untouched and remain accessible from source_path.
//
// Returns a JSON list of {name, version, action, source_type}.
func handleImportSkillRegistryDir(reg *skillregistry.Registry) handlerFunc {
	return func(ctx context.Context, _ store.Store, args json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Path        string `json:"path"`
			Author      string `json:"author"`
			Scope       string `json:"scope"`
			WorkspaceID string `json:"workspace_id"`
			Recursive   bool   `json:"recursive"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.Path == "" {
			return nil, fmt.Errorf("path is required")
		}
		abs, err := filepath.Abs(p.Path)
		if err != nil {
			return nil, fmt.Errorf("resolve path: %w", err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("stat path: %w", err)
		}
		author := strings.TrimSpace(p.Author)
		if author == "" {
			author = "import"
		}

		var workspaceID *string
		switch strings.ToLower(p.Scope) {
		case "", "global":
			workspaceID = nil
		case "workspace":
			if p.WorkspaceID == "" {
				return nil, fmt.Errorf("scope=\"workspace\" requires workspace_id")
			}
			ws := p.WorkspaceID
			workspaceID = &ws
		default:
			return nil, fmt.Errorf("invalid scope %q (expected workspace/global)", p.Scope)
		}

		type result struct {
			Name       string `json:"name"`
			Version    int    `json:"version"`
			Action     string `json:"action"`
			SourceType string `json:"source_type"`
			SourcePath string `json:"source_path,omitempty"`
		}

		var results []result

		importOne := func(skillMdPath, bundlePath string) error {
			body, err := os.ReadFile(skillMdPath)
			if err != nil {
				return fmt.Errorf("read %s: %w", skillMdPath, err)
			}
			parsed, err := skillregistry.Parse(string(body), "")
			if err != nil {
				return fmt.Errorf("parse %s: %w", skillMdPath, err)
			}
			res, err := reg.Publish(ctx, skillregistry.PublishOptions{
				Name:        parsed.Name,
				Body:        string(body),
				Author:      author,
				WorkspaceID: workspaceID,
				SourcePath:  bundlePath,
			})
			if err != nil {
				return err
			}
			st := "inline"
			if bundlePath != "" {
				st = "path"
			}
			results = append(results, result{
				Name: res.Name, Version: res.Version, Action: res.Action,
				SourceType: st, SourcePath: bundlePath,
			})
			return nil
		}

		// Single-file case.
		if !info.IsDir() {
			if !strings.HasSuffix(strings.ToLower(abs), ".md") {
				return nil, fmt.Errorf("file must be a .md skill")
			}
			if err := importOne(abs, ""); err != nil {
				return nil, err
			}
			return jsonResult(results)
		}

		// Directory contains a SKILL.md → bundle import (single skill).
		skillMd := filepath.Join(abs, "SKILL.md")
		if _, statErr := os.Stat(skillMd); statErr == nil {
			if err := importOne(skillMd, abs); err != nil {
				return nil, err
			}
			return jsonResult(results)
		}

		// Otherwise treat as a directory of skills. Walk and import
		// every *.md as inline, every nested SKILL.md as a bundle.
		var importErrs []string
		walk := filepath.Walk
		if !p.Recursive {
			// Walk only one level deep.
			walk = func(root string, fn filepath.WalkFunc) error {
				entries, err := os.ReadDir(root)
				if err != nil {
					return err
				}
				for _, e := range entries {
					path := filepath.Join(root, e.Name())
					info, _ := e.Info()
					if err := fn(path, info, nil); err != nil {
						return err
					}
					if e.IsDir() {
						subSkill := filepath.Join(path, "SKILL.md")
						if si, err := os.Stat(subSkill); err == nil {
							if err := fn(subSkill, si, nil); err != nil {
								return err
							}
						}
					}
				}
				return nil
			}
		}
		walkErr := walk(abs, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}
			if info.IsDir() {
				return nil
			}
			lower := strings.ToLower(filepath.Base(path))
			if lower == "skill.md" {
				if err := importOne(path, filepath.Dir(path)); err != nil {
					importErrs = append(importErrs, err.Error())
				}
				return nil
			}
			if strings.HasSuffix(lower, ".md") && filepath.Dir(path) == abs {
				if err := importOne(path, ""); err != nil {
					importErrs = append(importErrs, err.Error())
				}
			}
			return nil
		})
		if walkErr != nil {
			return nil, fmt.Errorf("walk: %w", walkErr)
		}
		if len(importErrs) > 0 && len(results) == 0 {
			return nil, fmt.Errorf("no skills imported; first error: %s", importErrs[0])
		}
		// Surface partial errors as a top-level field.
		out := map[string]any{"imported": results}
		if len(importErrs) > 0 {
			out["errors"] = importErrs
		}
		return jsonResult(out)
	}
}

// handleImportSkillRegistryGit clones a git URL into the daemon's
// managed cache, then runs the same path-import logic against the
// cloned tree. Each imported row records (git_url, git_ref, git_commit)
// in its metadata under the "source" key so callers can later refresh
// or trace provenance.
//
// Args (admin tool surface):
//
//	{
//	  "url":          "git@github.com:foo/bar.git" | "https://...",
//	  "ref":          "main"  | "v1.2.0" (optional; default branch if empty),
//	  "subpath":      "skills" (optional; defaults to repo root),
//	  "scope":        "global" | "workspace" (default global),
//	  "workspace_id": "..."  (required when scope=workspace),
//	  "author":       "anthropics" (optional; default = repo URL host/path)
//	}
//
// The git binary must be on PATH; auth uses the local agent's
// credentials (ssh-agent / system keychain). The daemon never sees
// the user's git password directly.
func handleImportSkillRegistryGit(reg *skillregistry.Registry, gitSrc *skillregistry.GitSource) handlerFunc {
	return func(ctx context.Context, _ store.Store, args json.RawMessage) (json.RawMessage, error) {
		if gitSrc == nil {
			return nil, fmt.Errorf("git source not configured")
		}
		var p struct {
			URL         string `json:"url"`
			Ref         string `json:"ref"`
			Subpath     string `json:"subpath"`
			Author      string `json:"author"`
			Scope       string `json:"scope"`
			WorkspaceID string `json:"workspace_id"`
			Recursive   bool   `json:"recursive"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if strings.TrimSpace(p.URL) == "" {
			return nil, fmt.Errorf("url is required")
		}
		// Reject any subpath that escapes the repo (defence-in-depth).
		clean := filepath.Clean(p.Subpath)
		if clean == "." {
			clean = ""
		}
		if strings.HasPrefix(clean, "..") || strings.HasPrefix(clean, "/") {
			return nil, fmt.Errorf("invalid subpath %q (must stay inside the repo)", p.Subpath)
		}

		// Resolve scope.
		var workspaceID *string
		switch strings.ToLower(p.Scope) {
		case "", "global":
			workspaceID = nil
		case "workspace":
			if p.WorkspaceID == "" {
				return nil, fmt.Errorf("scope=\"workspace\" requires workspace_id")
			}
			ws := p.WorkspaceID
			workspaceID = &ws
		default:
			return nil, fmt.Errorf("invalid scope %q (expected workspace/global)", p.Scope)
		}

		clone, err := gitSrc.Clone(ctx, p.URL, p.Ref)
		if err != nil {
			return nil, fmt.Errorf("clone: %w", err)
		}

		root := clone.LocalPath
		if clean != "" {
			root = filepath.Join(clone.LocalPath, clean)
		}
		info, err := os.Stat(root)
		if err != nil {
			return nil, fmt.Errorf("subpath not found in clone: %w", err)
		}

		author := strings.TrimSpace(p.Author)
		if author == "" {
			author = "git:" + shortRepoLabel(p.URL)
		}

		extras := map[string]any{
			"source": map[string]any{
				"type":       "git",
				"git_url":    clone.URL,
				"git_ref":    clone.Ref,
				"git_commit": clone.Commit,
				"subpath":    clean,
			},
		}

		type result struct {
			Name       string `json:"name"`
			Version    int    `json:"version"`
			Action     string `json:"action"`
			SourceType string `json:"source_type"`
			SourcePath string `json:"source_path,omitempty"`
		}
		var results []result
		var importErrs []string

		importOne := func(skillMdPath, bundlePath string) {
			body, err := os.ReadFile(skillMdPath)
			if err != nil {
				importErrs = append(importErrs, fmt.Sprintf("read %s: %v", skillMdPath, err))
				return
			}
			parsed, err := skillregistry.Parse(string(body), "")
			if err != nil {
				importErrs = append(importErrs, fmt.Sprintf("parse %s: %v", skillMdPath, err))
				return
			}
			res, err := reg.Publish(ctx, skillregistry.PublishOptions{
				Name:               parsed.Name,
				Body:               string(body),
				Author:             author,
				WorkspaceID:        workspaceID,
				SourcePath:         bundlePath,
				SourceTypeOverride: "git",
				MetadataExtras:     extras,
			})
			if err != nil {
				importErrs = append(importErrs, fmt.Sprintf("publish %s: %v", skillMdPath, err))
				return
			}
			results = append(results, result{
				Name: res.Name, Version: res.Version, Action: res.Action,
				SourceType: "git", SourcePath: bundlePath,
			})
		}

		// Single-skill folder shape.
		if info.IsDir() {
			skillMd := filepath.Join(root, "SKILL.md")
			if _, statErr := os.Stat(skillMd); statErr == nil {
				importOne(skillMd, root)
			} else {
				// Walk one level (or recursive) for a folder of skills.
				walk := walkOneLevel
				if p.Recursive {
					walk = filepath.Walk
				}
				walkErr := walk(root, func(path string, fi os.FileInfo, werr error) error {
					if werr != nil || fi == nil || fi.IsDir() {
						return nil
					}
					base := strings.ToLower(filepath.Base(path))
					if base == "skill.md" {
						importOne(path, filepath.Dir(path))
						return nil
					}
					if strings.HasSuffix(base, ".md") && filepath.Dir(path) == root {
						// inline .md at the top level — no bundle
						importOne(path, "")
					}
					return nil
				})
				if walkErr != nil {
					return nil, fmt.Errorf("walk: %w", walkErr)
				}
			}
		} else if strings.HasSuffix(strings.ToLower(root), ".md") {
			importOne(root, "")
		} else {
			return nil, fmt.Errorf("subpath %q is not a SKILL.md folder or .md file", p.Subpath)
		}

		out := map[string]any{
			"clone": map[string]any{
				"url":        clone.URL,
				"ref":        clone.Ref,
				"commit":     clone.Commit,
				"local_path": clone.LocalPath,
			},
			"imported": results,
		}
		if len(importErrs) > 0 {
			out["errors"] = importErrs
		}
		return jsonResult(out)
	}
}

// walkOneLevel mirrors filepath.Walk but only descends one directory
// deep — useful when subpath points at a directory of skills (each
// child folder is its own SKILL.md bundle).
func walkOneLevel(root string, fn filepath.WalkFunc) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		path := filepath.Join(root, e.Name())
		info, _ := e.Info()
		if err := fn(path, info, nil); err != nil {
			return err
		}
		if e.IsDir() {
			subSkill := filepath.Join(path, "SKILL.md")
			if si, err := os.Stat(subSkill); err == nil {
				if err := fn(subSkill, si, nil); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// shortRepoLabel reduces a URL to a human-readable "host/path" label.
// "git@github.com:org/repo.git" → "github.com/org/repo"
// "https://github.com/org/repo.git" → "github.com/org/repo"
// Used as the author when none was supplied — keeps provenance visible
// without exposing the full URL on every list row.
func shortRepoLabel(url string) string {
	s := strings.TrimSuffix(url, ".git")
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.Replace(s, ":", "/", 1)
	return s
}

func handleDeleteSkillRegistryTag(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	var p struct {
		Name string `json:"name"`
		Tag  string `json:"tag"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Name == "" || p.Tag == "" {
		return nil, fmt.Errorf("name and tag are required")
	}
	if err := s.DeleteSkillRegistryTag(ctx, p.Name, p.Tag); err != nil {
		return nil, fmt.Errorf("delete tag: %w", err)
	}
	return textResult("deleted"), nil
}
