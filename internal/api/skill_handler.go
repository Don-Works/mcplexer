// Package api — skill_handler exposes POST /api/skills/{id}/run.
//
// Loads the manifest + body from the on-disk skill directory, attaches a
// skill context (skill_id + namespace allowlist) and dispatches into the
// gateway's code-mode pipeline. See ADR 0004 for the capability model.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/don-works/mcplexer/internal/gateway"
	"github.com/don-works/mcplexer/internal/skills"
)

// SkillRunner is the subset of *gateway.Server the handler depends on.
// Defined as an interface so tests can stub it out without standing up
// a full gateway.
type SkillRunner interface {
	ExecuteSkill(ctx context.Context, id string, m *skills.Manifest, body string) gateway.SkillRunResult
}

// SkillsRoot resolves the directory that holds installed skill bundles.
// Each skill lives at <root>/<id>/manifest.toml + <root>/<id>/<entry_point>.
type SkillsRoot func() (string, error)

// defaultSkillsRoot returns ~/.mcplexer/skills, matching cmd/mcplexer/setup.go.
func defaultSkillsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".mcplexer", "skills"), nil
}

type skillHandler struct {
	runner SkillRunner
	root   SkillsRoot
}

// run handles POST /api/skills/{id}/run. The request body is currently
// ignored — future versions may pass per-invocation arguments.
func (h *skillHandler) run(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" || strings.ContainsAny(id, "/\\.") {
		writeError(w, http.StatusBadRequest, "invalid skill id")
		return
	}
	root, err := h.root()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	manifest, body, err := loadSkill(root, id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "skill not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	res := h.runner.ExecuteSkill(r.Context(), id, manifest, body)
	if res.Error != nil {
		writeJSON(w, http.StatusBadRequest, res)
		return
	}
	// res.Result is already a JSON-encoded MCP CallToolResult — pass it
	// through verbatim under the {"result": ...} envelope.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]json.RawMessage{
		"result": res.Result,
	})
}

// loadSkill reads <root>/<id>/manifest.toml and the body file referenced
// by manifest.EntryPoint (default skill.md). The body's leading TS/JS
// fenced code block, if present, is the JS entry point. For now we treat
// the whole body file as JS.
func loadSkill(root, id string) (*skills.Manifest, string, error) {
	manifestPath := filepath.Join(root, id, "manifest.toml")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, "", fmt.Errorf("read manifest: %w", err)
	}
	m, err := skills.Parse(manifestData)
	if err != nil {
		return nil, "", fmt.Errorf("parse manifest: %w", err)
	}
	if err := skills.Validate(m); err != nil {
		return nil, "", fmt.Errorf("validate manifest: %w", err)
	}

	entry := m.EntryPoint
	if entry == "" {
		entry = "skill.md"
	}
	bodyData, err := os.ReadFile(filepath.Join(root, id, entry))
	if err != nil {
		return nil, "", fmt.Errorf("read body: %w", err)
	}
	return m, string(bodyData), nil
}
