// Package api — skill_migration_handler exposes the local-skill discovery
// + per-skill import endpoints that back the dashboard's "Local skills not
// in registry" tile (W5 of the skills-first epic).
//
// Endpoints:
//
//	GET  /api/v1/skills/local-unpublished[?source=DIR]
//	POST /api/v1/skills/import {name, source_dir, overwrite?}
//
// Defaults source to ~/.claude/skills and resolves a leading "~" so the
// caller doesn't need to know the operator's home dir.
package api

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/don-works/mcplexer/internal/skillregistry"
)

// skillMigrationHandler wraps a Registry to back the W5 dashboard surface.
type skillMigrationHandler struct {
	registry *skillregistry.Registry
}

// localUnpublishedResponse is the shape returned by GET
// /api/v1/skills/local-unpublished — path is the resolved source dir,
// skills is one row per directory under it.
type localUnpublishedResponse struct {
	Path   string                     `json:"path"`
	Skills []skillregistry.LocalSkill `json:"skills"`
}

// listLocalUnpublished discovers SKILL.md directories under ?source= (or
// the default ~/.claude/skills) and classifies each against the registry.
func (h *skillMigrationHandler) listLocalUnpublished(w http.ResponseWriter, r *http.Request) {
	rawSource := r.URL.Query().Get("source")
	src := resolveMigrationSource(rawSource)
	if rawSource == "" {
		if info, err := os.Stat(src); os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, localUnpublishedResponse{
				Path:   src,
				Skills: []skillregistry.LocalSkill{},
			})
			return
		} else if err == nil && !info.IsDir() {
			writeErrorDetail(w, http.StatusBadRequest, "discover failed", "source path is not a directory: "+src)
			return
		}
	}
	rows, err := h.registry.DiscoverLocalSkills(r.Context(), src)
	if err != nil {
		writeErrorDetail(w, http.StatusBadRequest, "discover failed", err.Error())
		return
	}
	if rows == nil {
		rows = []skillregistry.LocalSkill{}
	}
	writeJSON(w, http.StatusOK, localUnpublishedResponse{
		Path:   src,
		Skills: rows,
	})
}

// importLocalSkillRequest is the POST body for /api/v1/skills/import.
type importLocalSkillRequest struct {
	Name      string `json:"name"`
	SourceDir string `json:"source_dir"`
	Overwrite bool   `json:"overwrite,omitempty"`
}

// importLocalSkill publishes one local skill into the registry. The body
// must include name (used as the safety check on the parsed
// frontmatter) and source_dir (absolute path to the skill folder). When
// `overwrite` is true, a version-conflict publishes as a new version
// rather than failing.
func (h *skillMigrationHandler) importLocalSkill(w http.ResponseWriter, r *http.Request) {
	var req importLocalSkillRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.SourceDir == "" {
		writeError(w, http.StatusBadRequest, "name and source_dir are required")
		return
	}
	srcDir := skillregistry.ExpandUserHome(req.SourceDir)
	if !filepath.IsAbs(srcDir) {
		writeError(w, http.StatusBadRequest, "source_dir must be absolute")
		return
	}
	res := h.registry.ImportLocalSkill(r.Context(), skillregistry.MigrateOptions{
		Path:       srcDir,
		ArchiveDir: pickArchiveDir(srcDir),
		Overwrite:  req.Overwrite,
		Author:     "dashboard-migrate",
	})
	// Surface the parsed name so the dashboard can match the result to
	// the row it was rendering — even on failure we usually have it.
	if res.Name == "" {
		res.Name = req.Name
	}
	if res.Action == skillregistry.ActionFailed {
		// Best-effort status mapping: registry-side validation problems
		// surface as 422 so the dashboard can show the message in-line
		// without falling into the generic 500 path.
		writeJSON(w, http.StatusUnprocessableEntity, res)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// resolveMigrationSource expands `~` and returns a default when blank.
// Relative paths pass through verbatim; DiscoverLocalSkills then fails
// with a clear "read source dir" error rather than walking the daemon's
// cwd.
func resolveMigrationSource(src string) string {
	if src == "" {
		return skillregistry.ExpandUserHome("~/.claude/skills")
	}
	return skillregistry.ExpandUserHome(src)
}

// pickArchiveDir returns a default archive root co-located with the
// skill's parent dir, so a skill at ~/.claude/skills/foo archives to
// ~/.claude/skills/.migrated/<stamp>/foo (audit-trail next to originals).
func pickArchiveDir(src string) string {
	parent := filepath.Dir(src)
	if parent == "" || parent == "/" {
		return skillregistry.DefaultArchiveDir(time.Now())
	}
	return filepath.Join(parent, ".migrated", filepath.Base(skillregistry.DefaultArchiveDir(time.Now())))
}
