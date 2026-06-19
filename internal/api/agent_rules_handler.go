package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/don-works/mcplexer/internal/agentrules"
)

// agentRulesHandler backs /api/v1/agent-rules/*. Exposes the marker-bounded
// block sync surface to the dashboard so users see "out of date" + can
// click Sync without dropping to the CLI.
//
// The path is resolved per-request (default ~/.claude/CLAUDE.md, override
// via ?path=); we don't capture it in the struct because the handler
// process may run as a different user than the dashboard caller in
// container setups. Always reads HOME at the moment of the call.
type agentRulesHandler struct{}

type agentRulesStatusResponse struct {
	Present        bool   `json:"present"`
	CurrentVersion int    `json:"current_version"`
	LatestVersion  int    `json:"latest_version"`
	UpToDate       bool   `json:"up_to_date"`
	Path           string `json:"path"`
}

type agentRulesSyncResponse struct {
	Changed bool   `json:"changed"`
	Version int    `json:"version"`
	Path    string `json:"path"`
}

// RegisterAgentRulesRoutes registers the GET /status + POST /sync
// endpoints. Wired from router.go unconditionally — no store / no
// service dependency; the handler is pure file I/O.
func RegisterAgentRulesRoutes(mux *http.ServeMux) {
	h := &agentRulesHandler{}
	mux.HandleFunc("GET /api/v1/agent-rules/status", h.status)
	mux.HandleFunc("POST /api/v1/agent-rules/sync", h.sync)
}

func (h *agentRulesHandler) status(w http.ResponseWriter, r *http.Request) {
	path, err := resolveRulesPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	present, current, up, err := agentrules.Status(path, agentrules.CurrentVersion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, agentRulesStatusResponse{
		Present:        present,
		CurrentVersion: current,
		LatestVersion:  agentrules.CurrentVersion,
		UpToDate:       up,
		Path:           path,
	})
}

func (h *agentRulesHandler) sync(w http.ResponseWriter, r *http.Request) {
	path, err := resolveRulesPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	changed, err := agentrules.Sync(path, agentrules.CurrentVersion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, agentRulesSyncResponse{
		Changed: changed,
		Version: agentrules.CurrentVersion,
		Path:    path,
	})
}

// resolveRulesPath returns the path to operate on. If override is non-empty it
// must stay inside an approved agent config directory. The dashboard sync
// surface is for agent rules files, not arbitrary host file writes.
func resolveRulesPath(override string) (string, error) {
	if override != "" {
		clean := filepath.Clean(override)
		if !filepath.IsAbs(clean) {
			return "", &pathError{msg: "path override must be absolute"}
		}
		roots, err := allowedRulesRoots()
		if err != nil {
			return "", err
		}
		for _, root := range roots {
			if pathInside(root, clean) {
				return clean, nil
			}
		}
		return "", &pathError{msg: fmt.Sprintf("path override must reside under ~/.claude/ or ~/.mcplexer/, got %q", clean)}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "CLAUDE.md"), nil
}

func allowedRulesRoots() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return []string{
		filepath.Join(home, ".claude"),
		filepath.Join(home, ".mcplexer"),
	}, nil
}

func pathInside(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

type pathError struct{ msg string }

func (e *pathError) Error() string { return e.msg }
