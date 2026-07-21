// scopes_handler.go — exposes the canonical peer-scope registry over
// HTTP so the dashboard's grant picker can render known scopes with
// descriptions + severity. Read-only.
package api

import (
	"net/http"

	"github.com/don-works/mcplexer/internal/peerscope"
)

// scopesHandler serves GET /api/v1/scopes.
type scopesHandler struct{}

// scopeView is the JSON shape returned to the UI. Mirrors peerscope.ScopeDef
// but spells out the boolean wildcard flag so the UI doesn't have to derive it.
type scopeView struct {
	Prefix          string `json:"prefix"`
	ResourceKind    string `json:"resource_kind"`
	WildcardAllowed bool   `json:"wildcard_allowed"`
	Description     string `json:"description"`
	Severity        string `json:"severity"`
}

// handleList serves GET /api/v1/scopes.
func (h *scopesHandler) handleList(w http.ResponseWriter, _ *http.Request) {
	out := make([]scopeView, 0, len(peerscope.Known))
	for _, d := range peerscope.Known {
		out = append(out, scopeView{
			Prefix:          d.Prefix,
			ResourceKind:    d.ResourceKind,
			WildcardAllowed: d.WildcardAllowed,
			Description:     d.Description,
			Severity:        d.Severity,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
