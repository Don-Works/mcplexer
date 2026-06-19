// registry.go — thin service-layer over store.WorkerTemplateStore.
// Mirrors skillregistry.Registry's Publish/Get/ListHeads shape so
// callers feel at home, but the storage is the worker_templates table
// (migration 057) and there's no markdown-frontmatter parsing — Body is
// a JSON-encoded WorkerTemplate.
package workertemplates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

// Registry is the service-layer entry point for worker templates. Wrap
// the store and expose the publish / fetch verbs that callers need.
type Registry struct {
	store store.WorkerTemplateStore
}

// New constructs a Registry around a store. Returns nil if s is nil so
// the wiring code can guard with `if reg == nil` rather than panic.
func New(s store.WorkerTemplateStore) *Registry {
	if s == nil {
		return nil
	}
	return &Registry{store: s}
}

// AdminScope is the unrestricted scope — bypasses workspace filtering.
// Used by REST + control admin tools that need to see every template.
// Re-exported here so callers don't have to import skillregistry for it.
func AdminScope() store.SkillScope { return skillregistry.AdminScope() }

// VersionRef is "latest", "stable", or an explicit int. Shared with
// skillregistry — we just re-export the type so callers can pass either
// to Get without juggling two version-ref types.
type VersionRef = skillregistry.VersionRef

// ParseVersionRef accepts an int (>0), the literal "latest" / "stable",
// or empty (= latest). Re-exported from skillregistry.
func ParseVersionRef(v any) (VersionRef, error) {
	return skillregistry.ParseVersionRef(v)
}

// PublishOptions is the arg payload for Publish.
type PublishOptions struct {
	// Body is the JSON-encoded WorkerTemplate. Marshal() produces this.
	Body string

	// Author is a free-form identity hint (e.g. session id, agent
	// client_type, or "worker_publish" for the dashboard-driven publish).
	Author string

	// CreatedByAgentID is the session id that triggered the publish.
	// Optional — used for audit linkage only.
	CreatedByAgentID string

	// ParentVersion records lineage when the publish is a fork of an
	// existing version. Optional.
	ParentVersion *int

	// WorkspaceID nil = global; non-nil = pinned to one workspace.
	WorkspaceID *string

	// Description overrides the template's own Description field on
	// the row's description column. Optional.
	Description string
}

// PublishResult is what Publish returns.
type PublishResult struct {
	Name        string `json:"name"`
	Version     int    `json:"version"`
	ContentHash string `json:"content_hash"`
	Action      string `json:"action"` // "created" | "deduped"
}

// Publish validates the body + writes a new version. Dedup-on-content-
// hash is enforced by the store: re-publishing identical body returns
// the existing version with Action="deduped".
func (r *Registry) Publish(
	ctx context.Context, opts PublishOptions,
) (*PublishResult, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("workertemplates: registry not initialised")
	}
	tmpl, err := Unmarshal(opts.Body)
	if err != nil {
		return nil, err
	}
	desc := tmpl.Description
	if strings.TrimSpace(opts.Description) != "" {
		desc = opts.Description
	}
	tagsJSON, _ := json.Marshal([]string{"worker-template"})

	entry := &store.WorkerTemplateEntry{
		Name:             tmpl.Name,
		ContentHash:      ContentHash([]byte(opts.Body)),
		Description:      desc,
		Body:             opts.Body,
		TagsJSON:         tagsJSON,
		Author:           opts.Author,
		ParentVersion:    opts.ParentVersion,
		CreatedByAgentID: opts.CreatedByAgentID,
		WorkspaceID:      opts.WorkspaceID,
	}
	dedup, err := r.store.PublishWorkerTemplate(ctx, entry)
	if err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}
	action := "created"
	if dedup {
		action = "deduped"
	}
	return &PublishResult{
		Name:        entry.Name,
		Version:     entry.Version,
		ContentHash: entry.ContentHash,
		Action:      action,
	}, nil
}

// Get resolves (scope, name, ref) to one entry. Workspace templates
// shadow globals of the same name when scope.WorkspaceIDs lists them.
func (r *Registry) Get(
	ctx context.Context, scope store.SkillScope, name string, ref VersionRef,
) (*store.WorkerTemplateEntry, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("workertemplates: registry not initialised")
	}
	switch {
	case ref.Latest:
		return r.store.GetWorkerTemplateHead(ctx, scope, name)
	case ref.Stable:
		// Worker templates don't yet support @stable tagging — fall
		// back to head until the tagging surface lands.
		return r.store.GetWorkerTemplateHead(ctx, scope, name)
	case ref.Version > 0:
		for _, wsID := range scope.WorkspaceIDs {
			ws := wsID
			if e, err := r.store.GetWorkerTemplate(ctx, &ws, name, ref.Version); err == nil {
				return e, nil
			}
		}
		return r.store.GetWorkerTemplate(ctx, nil, name, ref.Version)
	default:
		return r.store.GetWorkerTemplateHead(ctx, scope, name)
	}
}

// ListHeads returns one row per template name visible in scope.
func (r *Registry) ListHeads(
	ctx context.Context, scope store.SkillScope, limit int,
) ([]store.WorkerTemplateEntry, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("workertemplates: registry not initialised")
	}
	return r.store.ListWorkerTemplateHeads(ctx, scope, limit)
}

// ListVersions returns every version of name in scope, descending.
func (r *Registry) ListVersions(
	ctx context.Context, scope store.SkillScope, name string,
) ([]store.WorkerTemplateEntry, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("workertemplates: registry not initialised")
	}
	return r.store.ListWorkerTemplateVersions(ctx, scope, name, false)
}

// SoftDelete soft-deletes one (or all) version(s) of (workspace, name).
func (r *Registry) SoftDelete(
	ctx context.Context, workspaceID *string, name string, version int,
) error {
	if r == nil || r.store == nil {
		return errors.New("workertemplates: registry not initialised")
	}
	return r.store.SoftDeleteWorkerTemplate(ctx, workspaceID, name, version)
}
