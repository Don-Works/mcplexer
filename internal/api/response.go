package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/don-works/mcplexer/internal/scopes"
)

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("failed to encode response", "error", err)
	}
}

// errorResponse is the standard error response body.
type errorResponse struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

// writeErrorDetail writes a JSON error response with extra details.
func writeErrorDetail(w http.ResponseWriter, status int, msg, detail string) {
	writeJSON(w, status, errorResponse{Error: msg, Details: detail})
}

// denialResponse is the standard 403 body for typed scope denials.
// "error" stays "forbidden" for back-compat with clients that just
// switch on the top-level field; the structured "denial" object is
// the new vocabulary the bug fix (JTAC65) introduces.
type denialResponse struct {
	Error  string        `json:"error"`
	Denial scopes.Denial `json:"denial"`
}

// writeDenial writes a structured 403 response carrying a typed
// scopes.DenialCode. Use this at every cross-peer scope-check rejection
// site so the calling agent can distinguish "never granted" from
// "granted then revoked" from "doesn't apply" from "cross-org".
//
// Existing 403 sites that ship a generic message (cross-origin
// browser-request denials, path-traversal blocks) should keep using
// writeError — they aren't scope rejections and the vocabulary
// wouldn't fit.
func writeDenial(w http.ResponseWriter, d scopes.Denial) {
	writeJSON(w, http.StatusForbidden, denialResponse{
		Error:  "forbidden",
		Denial: d,
	})
}

// decodeJSON reads and decodes a JSON request body into v.
func decodeJSON(r *http.Request, v any) error {
	defer func() { _ = r.Body.Close() }()

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}

	// Enforce a single JSON value in the body.
	if err := dec.Decode(&struct{}{}); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return errors.New("request body must contain only one JSON object")
}
