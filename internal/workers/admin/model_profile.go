// model_profile.go — pure HTTP handlers for the ModelProfile admin
// surface (Layer 2). Mirrors the request-parsing + JSON response style
// of internal/api/auth_handler.go but lives in the workers/admin
// package since model profiles are a workers-only concept.
//
// Validation:
//   - Provider must be one of anthropic|openai|openai_compat|claude_cli|opencode_cli|grok_cli|mimo_cli|gemini_cli|codex_cli|pi_cli.
//   - Name is required, max 80 chars.
//   - EndpointURL is required for anthropic|openai|openai_compat. It
//     may be empty for claude_cli (the binary path can be discovered
//     from $PATH when omitted).
//   - SecretScopeID is required for anthropic|openai|openai_compat.
//     Optional for claude_cli (OAuth login owns auth there).
//   - Builtin rows are managed by the daemon and reject Update/Delete.
package admin

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// ModelProfileHandlers exposes the five CRUD endpoints. Wired into the
// router by the main package — this file deliberately does not import
// any routing/mux types so the package stays HTTP-framework neutral.
//
// All mutation + validation lives in ModelProfileCore (see
// model_profile_service.go); these handlers are pure transport — decode
// the request, call the core, map the typed result/error onto an HTTP
// status code. The CWD-gated MCP tools call the SAME core, so the two
// surfaces can never drift on the Builtin / secret-required rules.
type ModelProfileHandlers struct {
	Store store.ModelProfileStore
}

// core lazily wraps Store in a ModelProfileCore. Constructed per-call so
// the router wiring (which only sets Store) stays unchanged.
func (h *ModelProfileHandlers) core() *ModelProfileCore {
	return NewModelProfileCore(h.Store)
}

// validProviders is the closed set the validator accepts. Adding a new
// provider here is the only file an adapter author needs to touch on
// the admin side.
var validProviders = map[string]struct{}{
	"anthropic":     {},
	"openai":        {},
	"openai_compat": {},
	"claude_cli":    {},
	"opencode_cli":  {},
	"grok_cli":      {},
	"mimo_cli":      {},
	"gemini_cli":    {},
	"codex_cli":     {},
	"pi_cli":        {},
}

const maxProfileNameLen = 80

// List returns every ModelProfile ordered by name ASC.
func (h *ModelProfileHandlers) List(w http.ResponseWriter, r *http.Request) {
	profiles, err := h.core().List(r.Context())
	if err != nil {
		mpWriteError(w, http.StatusInternalServerError, "failed to list model profiles")
		return
	}
	mpWriteJSON(w, http.StatusOK, profiles)
}

// Get returns one ModelProfile by id (path value "id").
func (h *ModelProfileHandlers) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := h.core().Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			mpWriteError(w, http.StatusNotFound, "model profile not found")
			return
		}
		mpWriteError(w, http.StatusInternalServerError, "failed to get model profile")
		return
	}
	mpWriteJSON(w, http.StatusOK, p)
}

// Create inserts a new ModelProfile after validating the payload.
// Returns 409 on unique-name conflict, 400 on validation error.
func (h *ModelProfileHandlers) Create(w http.ResponseWriter, r *http.Request) {
	var p store.ModelProfile
	if err := mpDecodeJSON(r, &p); err != nil {
		mpWriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	created, err := h.core().Create(r.Context(), &p)
	if err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			mpWriteError(w, http.StatusConflict, "model profile name already exists")
			return
		}
		if isModelProfileValidationErr(err) {
			mpWriteErrorDetail(w, http.StatusBadRequest, "invalid model profile", err.Error())
			return
		}
		mpWriteErrorDetail(w, http.StatusInternalServerError,
			"failed to create model profile", err.Error())
		return
	}
	mpWriteJSON(w, http.StatusCreated, created)
}

// Update overwrites every mutable field on the profile (after
// validation). Refuses to mutate Builtin=true rows. Returns 404 when
// the row is missing, 409 on unique-name conflict.
//
// The REST contract is a full replace: every body field maps to a patch
// field (including the zero value), so omitted fields are cleared just as
// they were before the core refactor. The MCP update tool, by contrast,
// uses a sparse patch (omit = unchanged).
func (h *ModelProfileHandlers) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p store.ModelProfile
	if err := mpDecodeJSON(r, &p); err != nil {
		mpWriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	patch := ModelProfilePatch{
		Name:          &p.Name,
		Provider:      &p.Provider,
		EndpointURL:   &p.EndpointURL,
		SecretScopeID: &p.SecretScopeID,
		KnownModels:   &p.KnownModels,
	}
	updated, err := h.core().Update(r.Context(), id, patch)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			mpWriteError(w, http.StatusNotFound, "model profile not found")
		case errors.Is(err, ErrModelProfileBuiltin):
			mpWriteError(w, http.StatusForbidden, "cannot modify builtin model profile")
		case errors.Is(err, store.ErrAlreadyExists):
			mpWriteError(w, http.StatusConflict, "model profile name already exists")
		case isModelProfileValidationErr(err):
			mpWriteErrorDetail(w, http.StatusBadRequest, "invalid model profile", err.Error())
		default:
			mpWriteErrorDetail(w, http.StatusInternalServerError,
				"failed to update model profile", err.Error())
		}
		return
	}
	mpWriteJSON(w, http.StatusOK, updated)
}

// Delete hard-deletes the row. Refuses to delete Builtin rows.
// Workers referencing the profile have their model_profile_id set to
// NULL by the FK's ON DELETE SET NULL clause (migration 056).
func (h *ModelProfileHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.core().Delete(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			mpWriteError(w, http.StatusNotFound, "model profile not found")
		case errors.Is(err, ErrModelProfileBuiltin):
			mpWriteError(w, http.StatusForbidden, "cannot delete builtin model profile")
		default:
			mpWriteError(w, http.StatusInternalServerError, "failed to delete model profile")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateModelProfile enforces the rules documented at the top of the
// file. Returns nil on success.
func validateModelProfile(p *store.ModelProfile) error {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return errors.New("name is required")
	}
	if len(name) > maxProfileNameLen {
		return errors.New("name exceeds 80 characters")
	}
	p.Name = name
	if _, ok := validProviders[p.Provider]; !ok {
		return errors.New("provider must be one of: anthropic, openai, openai_compat, claude_cli, opencode_cli, grok_cli, mimo_cli, gemini_cli, codex_cli, pi_cli")
	}
	switch p.Provider {
	case "openai_compat":
		if strings.TrimSpace(p.EndpointURL) == "" {
			return errors.New("endpoint_url is required for openai_compat")
		}
		if strings.TrimSpace(p.SecretScopeID) == "" {
			return errors.New("secret_scope_id is required for openai_compat")
		}
	case "anthropic", "openai":
		// Endpoint defaults are baked into the runner adapters; the user
		// only needs to provide a secret scope with the api_key.
		if strings.TrimSpace(p.SecretScopeID) == "" {
			return errors.New("secret_scope_id is required for this provider")
		}
	case "claude_cli", "opencode_cli", "grok_cli", "mimo_cli", "gemini_cli", "codex_cli", "pi_cli":
		// EndpointURL holds the binary path; both fields are optional.
		// CLI providers own their own credentials, so no secret scope needed.
	}
	return nil
}

// --- HTTP helpers (package-local to keep workers/admin framework-neutral).

type mpErrorResponse struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

func mpWriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("failed to encode response", "error", err)
	}
}

func mpWriteError(w http.ResponseWriter, status int, msg string) {
	mpWriteJSON(w, status, mpErrorResponse{Error: msg})
}

func mpWriteErrorDetail(w http.ResponseWriter, status int, msg, detail string) {
	mpWriteJSON(w, status, mpErrorResponse{Error: msg, Details: detail})
}

// mpDecodeJSON mirrors api.decodeJSON: strict mode, single JSON value.
func mpDecodeJSON(r *http.Request, v any) error {
	defer func() { _ = r.Body.Close() }()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return errors.New("request body must contain only one JSON object")
}
