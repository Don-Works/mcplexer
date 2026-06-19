package skillregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type Provenance struct {
	Source      string    `json:"source"`       // "local" or "hub:<peer_id>"
	PulledAt    time.Time `json:"pulled_at"`    // when pulled from hub
	PulledBy    string    `json:"pulled_by"`    // session ID of puller
	OriginalID  string    `json:"original_id"`  // original registry entry ID from hub
	OriginalVer int       `json:"original_ver"` // original version on hub
}

type PushMetadata struct {
	Name         string          `json:"name"`
	Version      int             `json:"version"`
	ContentHash  string          `json:"content_hash"`
	Description  string          `json:"description"`
	Author       string          `json:"author"`
	BundleSHA256 string          `json:"bundle_sha256,omitempty"`
	BundleSize   int             `json:"bundle_size,omitempty"`
	PublishedAt  time.Time       `json:"published_at"`
	SourceType   string          `json:"source_type"`
	SourcePath   string          `json:"source_path,omitempty"`
	MetadataJSON json.RawMessage `json:"metadata_json,omitempty"`
	TagsJSON     json.RawMessage `json:"tags_json,omitempty"`
	Provenance   *Provenance     `json:"provenance,omitempty"`
}

type PushResult struct {
	Name      string       `json:"name"`
	Version   int          `json:"version"`
	Metadata  PushMetadata `json:"metadata"`
	Bundle    []byte       `json:"bundle,omitempty"`
	BundleSHA string       `json:"bundle_sha,omitempty"`
}

type DryRunResult struct {
	Name        string          `json:"name"`
	LocalExists bool            `json:"local_exists"`
	LocalVer    int             `json:"local_ver,omitempty"`
	LocalHash   string          `json:"local_hash,omitempty"`
	HubVer      int             `json:"hub_ver"`
	HubHash     string          `json:"hub_hash"`
	Status      string          `json:"status"` // "new", "update", "conflict", "skip"
	BodyDiff    string          `json:"body_diff,omitempty"`
	TreeDiff    []FileDiffEntry `json:"tree_diff,omitempty"`
}

type PullOptions struct {
	Name    string
	Version int // 0 = latest
	DryRun  bool
	Scope   store.SkillScope
}

type PullResult struct {
	Name        string          `json:"name"`
	Version     int             `json:"version"`
	ContentHash string          `json:"content_hash"`
	Action      string          `json:"action"` // "created", "deduped", "skipped"
	BundleSHA   string          `json:"bundle_sha,omitempty"`
	BundleSize  int             `json:"bundle_size,omitempty"`
	BodyDiff    string          `json:"body_diff,omitempty"`
	TreeDiff    []FileDiffEntry `json:"tree_diff,omitempty"`
	DryRun      bool            `json:"dry_run"`
}

func (r *Registry) Push(ctx context.Context, scope store.SkillScope, name string, ref VersionRef) (*PushResult, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("skillregistry: not initialised")
	}
	entry, err := r.Get(ctx, scope, name, ref)
	if err != nil {
		return nil, fmt.Errorf("push get: %w", err)
	}
	return r.buildPushResult(ctx, entry)
}

func (r *Registry) buildPushResult(ctx context.Context, entry *store.SkillRegistryEntry) (*PushResult, error) {
	meta := PushMetadata{
		Name:         entry.Name,
		Version:      entry.Version,
		ContentHash:  entry.ContentHash,
		Description:  entry.Description,
		Author:       entry.Author,
		BundleSHA256: entry.BundleSHA256,
		BundleSize:   len(entry.Bundle),
		PublishedAt:  entry.PublishedAt,
		SourceType:   entry.SourceType,
		SourcePath:   entry.SourcePath,
		MetadataJSON: entry.MetadataJSON,
		TagsJSON:     entry.TagsJSON,
	}
	meta.Provenance = &Provenance{
		Source:      "local",
		PulledAt:    entry.PublishedAt,
		OriginalID:  entry.ID,
		OriginalVer: entry.Version,
	}
	res := &PushResult{
		Name:     entry.Name,
		Version:  entry.Version,
		Metadata: meta,
	}
	if entry.BundleSHA256 != "" {
		bundle, sha, err := r.store.GetSkillRegistryBundle(ctx, entry.WorkspaceID, entry.Name, entry.Version)
		if err == nil && len(bundle) > 0 {
			res.Bundle = bundle
			res.BundleSHA = sha
		}
	}
	return res, nil
}

func (r *Registry) DryRunPull(ctx context.Context, scope store.SkillScope, hubEntry *HubPullEntry) (*DryRunResult, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("skillregistry: not initialised")
	}
	res := &DryRunResult{
		Name:    hubEntry.Name,
		HubVer:  hubEntry.Version,
		HubHash: hubEntry.ContentHash,
	}
	local, err := r.store.GetSkillRegistryHead(ctx, scope, hubEntry.Name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("local head: %w", err)
	}
	if errors.Is(err, store.ErrNotFound) {
		res.Status = "new"
		return res, nil
	}
	res.LocalExists = true
	res.LocalVer = local.Version
	res.LocalHash = local.ContentHash
	if local.ContentHash == hubEntry.ContentHash {
		res.Status = "skip"
		return res, nil
	}
	if local.Version == hubEntry.Version {
		res.Status = "conflict"
		return res, nil
	}
	res.Status = "update"
	oldEntry, err := r.store.GetSkillRegistryEntry(ctx, local.WorkspaceID, hubEntry.Name, hubEntry.Version)
	if err == nil && oldEntry != nil && oldEntry.BundleSHA256 != "" && local.BundleSHA256 != "" {
		diff, err := r.DiffVersions(ctx, scope, hubEntry.Name,
			VersionRef{Version: oldEntry.Version},
			VersionRef{Version: local.Version})
		if err == nil {
			res.BodyDiff = diff.BodyDiff
			res.TreeDiff = diff.Tree
		}
	}
	return res, nil
}

type HubPullEntry struct {
	Name        string `json:"name"`
	Version     int    `json:"version"`
	ContentHash string `json:"content_hash"`
	Description string `json:"description"`
	Author      string `json:"author,omitempty"`
	BundleSHA   string `json:"bundle_sha,omitempty"`
	Body        string `json:"body"`
	Bundle      []byte `json:"bundle,omitempty"`
}

func (r *Registry) Pull(ctx context.Context, scope store.SkillScope, opts PullOptions) (*PullResult, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("skillregistry: not initialised")
	}
	ref := VersionRef{Latest: true}
	if opts.Version > 0 {
		ref = VersionRef{Version: opts.Version}
	}
	hubEntry := &HubPullEntry{
		Name:        opts.Name,
		Version:     ref.Version,
		ContentHash: "",
		Body:        "",
	}
	local, err := r.store.GetSkillRegistryHead(ctx, scope, opts.Name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("local head: %w", err)
	}
	if !errors.Is(err, store.ErrNotFound) {
		hubEntry.ContentHash = local.ContentHash
		hubEntry.Version = local.Version
	}
	dryRunRes := &DryRunResult{
		Name:        opts.Name,
		LocalExists: !errors.Is(err, store.ErrNotFound),
		LocalVer:    hubEntry.Version,
		LocalHash:   hubEntry.ContentHash,
		HubVer:      opts.Version,
		Status:      "new",
	}
	if !errors.Is(err, store.ErrNotFound) {
		dryRunRes.LocalHash = local.ContentHash
		dryRunRes.LocalVer = local.Version
		if local.ContentHash == "" {
			dryRunRes.Status = "new"
		} else {
			dryRunRes.Status = "skip"
		}
	}
	if opts.DryRun {
		return &PullResult{
			Name:    opts.Name,
			Version: opts.Version,
			DryRun:  true,
			Action:  dryRunRes.Status,
		}, nil
	}
	parsed, err := Parse(hubEntry.Body, opts.Name)
	if err != nil {
		return nil, fmt.Errorf("parse body: %w", err)
	}
	entry := &store.SkillRegistryEntry{
		Name:         parsed.Name,
		ContentHash:  parsed.ContentHash,
		Description:  parsed.Description,
		Body:         parsed.Body,
		MetadataJSON: parsed.MetadataJSON,
		TagsJSON:     parsed.TagsJSON,
		Author:       "hub-pull",
		WorkspaceID:  nil,
		SourceType:   "hub-pull",
	}
	dedup, err := r.store.PublishSkillRegistryEntry(ctx, entry)
	if err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}
	r.invalidate()
	action := "created"
	if dedup {
		action = "deduped"
	}
	return &PullResult{
		Name:        opts.Name,
		Version:     entry.Version,
		ContentHash: entry.ContentHash,
		Action:      action,
	}, nil
}

type IndexEntry struct {
	Name        string `json:"name"`
	Version     int    `json:"version"`
	ContentHash string `json:"content_hash"`
	Description string `json:"description"`
	Author      string `json:"author,omitempty"`
	BundleSHA   string `json:"bundle_sha,omitempty"`
}

func (r *Registry) ListIndexEntries(ctx context.Context, scope store.SkillScope) ([]IndexEntry, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("skillregistry: not initialised")
	}
	heads, err := r.store.ListSkillRegistryHeads(ctx, scope, 0)
	if err != nil {
		return nil, err
	}
	entries := make([]IndexEntry, 0, len(heads))
	for _, e := range heads {
		entries = append(entries, IndexEntry{
			Name:        e.Name,
			Version:     e.Version,
			ContentHash: e.ContentHash,
			Description: e.Description,
			Author:      e.Author,
			BundleSHA:   e.BundleSHA256,
		})
	}
	return entries, nil
}

func BuildPushMetadata(entry *store.SkillRegistryEntry) PushMetadata {
	meta := PushMetadata{
		Name:         entry.Name,
		Version:      entry.Version,
		ContentHash:  entry.ContentHash,
		Description:  entry.Description,
		Author:       entry.Author,
		BundleSHA256: entry.BundleSHA256,
		BundleSize:   len(entry.Bundle),
		PublishedAt:  entry.PublishedAt,
		SourceType:   entry.SourceType,
		SourcePath:   entry.SourcePath,
		MetadataJSON: entry.MetadataJSON,
		TagsJSON:     entry.TagsJSON,
	}
	return meta
}

func ComputeContentHash(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
}
