package skillregistry

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type SyncProvenance struct {
	ExportedAt         time.Time `json:"exported_at"`
	ExportedBy         string    `json:"exported_by,omitempty"`
	SourceScope        string    `json:"source_scope"`
	SourceWorkspaceID  *string   `json:"source_workspace_id,omitempty"`
	SourceType         string    `json:"source_type,omitempty"`
	SourcePath         string    `json:"source_path,omitempty"`
	SourceBundleSHA256 string    `json:"source_bundle_sha256,omitempty"`
}

type SyncPackage struct {
	Name         string          `json:"name"`
	Version      int             `json:"version"`
	ContentHash  string          `json:"content_hash"`
	Description  string          `json:"description"`
	Body         string          `json:"body"`
	MetadataJSON json.RawMessage `json:"metadata_json,omitempty"`
	TagsJSON     json.RawMessage `json:"tags_json,omitempty"`
	Author       string          `json:"author,omitempty"`
	BundleSHA256 string          `json:"bundle_sha256,omitempty"`
	BundleB64    string          `json:"bundle_b64,omitempty"`
	Provenance   SyncProvenance  `json:"provenance"`
	Signature    string          `json:"signature"`
}

type ExportOptions struct {
	Name          string
	Version       VersionRef
	IncludeBundle bool
	ExportedBy    string
}

type ImportOptions struct {
	Package     SyncPackage
	WorkspaceID *string
	DryRun      bool
	Commit      bool
	Author      string
}

type SyncAction string

const (
	SyncImported SyncAction = "imported"
	SyncUpdated  SyncAction = "updated"
	SyncSkipped  SyncAction = "skipped"
)

type SyncPlan struct {
	Name             string         `json:"name"`
	Action           SyncAction     `json:"action"`
	DryRun           bool           `json:"dry_run"`
	WouldMutate      bool           `json:"would_mutate"`
	RequiresCommit   bool           `json:"requires_commit,omitempty"`
	ExistingVersion  int            `json:"existing_version,omitempty"`
	PublishedVersion int            `json:"published_version,omitempty"`
	BundleSHA256     string         `json:"bundle_sha256,omitempty"`
	BodyDiff         string         `json:"body_diff,omitempty"`
	FrontDiff        string         `json:"frontmatter_diff,omitempty"`
	Provenance       SyncProvenance `json:"provenance"`
}

func (r *Registry) ExportSkill(
	ctx context.Context, scope store.SkillScope, opts ExportOptions,
) (*SyncPackage, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("skillregistry: not initialised")
	}
	if opts.Name == "" {
		return nil, errors.New("name required")
	}
	ref := opts.Version
	if ref == (VersionRef{}) {
		ref = VersionRef{Latest: true}
	}
	entry, err := r.Get(ctx, scope, opts.Name, ref)
	if err != nil {
		return nil, err
	}
	pkg := &SyncPackage{
		Name:         entry.Name,
		Version:      entry.Version,
		ContentHash:  entry.ContentHash,
		Description:  entry.Description,
		Body:         entry.Body,
		MetadataJSON: entry.MetadataJSON,
		TagsJSON:     entry.TagsJSON,
		Author:       entry.Author,
		BundleSHA256: entry.BundleSHA256,
		Provenance: SyncProvenance{
			ExportedAt:         time.Now().UTC(),
			ExportedBy:         opts.ExportedBy,
			SourceScope:        scopeLabel(entry.WorkspaceID),
			SourceWorkspaceID:  entry.WorkspaceID,
			SourceType:         entry.SourceType,
			SourcePath:         entry.SourcePath,
			SourceBundleSHA256: entry.BundleSHA256,
		},
	}
	if opts.IncludeBundle && entry.BundleSHA256 != "" {
		bundle, sha, err := r.FetchBundle(ctx, scope, entry.Name, VersionRef{Version: entry.Version})
		if err != nil {
			return nil, err
		}
		pkg.BundleSHA256 = sha
		pkg.BundleB64 = base64.StdEncoding.EncodeToString(bundle)
	}
	pkg.Signature = pkgSignature(*pkg)
	return pkg, nil
}

func (r *Registry) ImportSkillPackage(ctx context.Context, opts ImportOptions) (*SyncPlan, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("skillregistry: not initialised")
	}
	pkg := opts.Package
	if err := validatePackage(pkg); err != nil {
		return nil, err
	}
	scope := importScope(opts.WorkspaceID)
	head, err := r.store.GetSkillRegistryHead(ctx, scope, pkg.Name)
	plan := &SyncPlan{
		Name:       pkg.Name,
		DryRun:     opts.DryRun || !opts.Commit,
		Provenance: pkg.Provenance,
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		plan.Action = SyncImported
		plan.WouldMutate = true
	case err != nil:
		return nil, fmt.Errorf("lookup head: %w", err)
	case head.ContentHash == pkg.ContentHash:
		plan.Action = SyncSkipped
		plan.ExistingVersion = head.Version
	default:
		plan.Action = SyncUpdated
		plan.WouldMutate = true
		plan.ExistingVersion = head.Version
		plan.BodyDiff = unifiedDiff(head.Body, pkg.Body, "body")
		oldFront, _, _ := splitFrontmatter(head.Body)
		newFront, _, _ := splitFrontmatter(pkg.Body)
		if oldFront != newFront {
			plan.FrontDiff = unifiedDiff(oldFront, newFront, "frontmatter")
		}
	}
	if !plan.WouldMutate {
		return plan, nil
	}
	if opts.DryRun || !opts.Commit {
		plan.RequiresCommit = true
		return plan, nil
	}
	pub, err := r.Publish(ctx, PublishOptions{
		Name:           pkg.Name,
		Body:           pkg.Body,
		Author:         importAuthor(opts.Author),
		WorkspaceID:    opts.WorkspaceID,
		Bundle:         decodeBundle(pkg),
		MetadataExtras: syncMetadata(pkg),
	})
	if err != nil {
		return nil, err
	}
	plan.PublishedVersion = pub.Version
	plan.BundleSHA256 = pub.BundleSHA256
	plan.DryRun = false
	plan.RequiresCommit = false
	return plan, nil
}

func validatePackage(pkg SyncPackage) error {
	if pkg.Name == "" || pkg.Body == "" || pkg.ContentHash == "" {
		return errors.New("sync package requires name, body, and content_hash")
	}
	parsed, err := Parse(pkg.Body, pkg.Name)
	if err != nil {
		return err
	}
	if parsed.ContentHash != pkg.ContentHash {
		return fmt.Errorf("content_hash mismatch: package=%s body=%s",
			pkg.ContentHash, parsed.ContentHash)
	}
	if pkg.Signature != "" && pkg.Signature != pkgSignature(pkg) {
		return errors.New("sync package signature mismatch")
	}
	if pkg.BundleB64 != "" {
		raw, err := base64.StdEncoding.DecodeString(pkg.BundleB64)
		if err != nil {
			return fmt.Errorf("bundle_b64: %w", err)
		}
		if sha, err := ValidateBundle(raw, pkg.Body); err != nil {
			return err
		} else if pkg.BundleSHA256 != "" && sha != pkg.BundleSHA256 {
			return errors.New("bundle sha mismatch")
		}
	}
	return nil
}

func pkgSignature(pkg SyncPackage) string {
	pkg.Signature = ""
	raw, _ := json.Marshal(pkg)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func importScope(workspaceID *string) store.SkillScope {
	if workspaceID == nil || *workspaceID == "" {
		return GlobalScope()
	}
	return store.SkillScope{WorkspaceIDs: []string{*workspaceID}}
}

func importAuthor(author string) string {
	if author == "" {
		return "skill-sync"
	}
	return author
}

func decodeBundle(pkg SyncPackage) []byte {
	if pkg.BundleB64 == "" {
		return nil
	}
	raw, _ := base64.StdEncoding.DecodeString(pkg.BundleB64)
	return raw
}

func syncMetadata(pkg SyncPackage) map[string]any {
	return map[string]any{
		"skill_sync": map[string]any{
			"source_scope":         pkg.Provenance.SourceScope,
			"source_workspace_id":  pkg.Provenance.SourceWorkspaceID,
			"source_type":          pkg.Provenance.SourceType,
			"source_path":          pkg.Provenance.SourcePath,
			"source_bundle_sha256": pkg.Provenance.SourceBundleSHA256,
			"exported_at":          pkg.Provenance.ExportedAt.Format(time.RFC3339Nano),
			"exported_by":          pkg.Provenance.ExportedBy,
			"signature":            pkg.Signature,
		},
	}
}
