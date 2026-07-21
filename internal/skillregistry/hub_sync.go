package skillregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const HubSyncManifestVersion = 1

type HubManifest struct {
	Version     int                `json:"version"`
	GeneratedAt time.Time          `json:"generated_at"`
	SourcePeer  string             `json:"source_peer,omitempty"`
	Entries     []HubManifestEntry `json:"entries"`
	ManifestSHA string             `json:"manifest_sha,omitempty"`
}

type HubManifestEntry struct {
	Name         string `json:"name"`
	Version      int    `json:"version"`
	ContentHash  string `json:"content_hash"`
	Description  string `json:"description"`
	Author       string `json:"author,omitempty"`
	BundleSHA256 string `json:"bundle_sha256,omitempty"`
	SourceType   string `json:"source_type,omitempty"`
}

type HubPackage struct {
	Manifest HubManifestEntry `json:"manifest"`
	Body     string           `json:"body"`
	Bundle   []byte           `json:"bundle,omitempty"`
}

type HubPackageEnvelope struct {
	Package    HubPackage `json:"package"`
	SHA256     string     `json:"sha256"`
	SignedBy   string     `json:"signed_by,omitempty"`
	SignedAt   time.Time  `json:"signed_at,omitempty"`
	Provenance string     `json:"provenance,omitempty"`
}

type ProvenanceInfo struct {
	Source      string    `json:"source"`
	SourcePeer  string    `json:"source_peer,omitempty"`
	PulledAt    time.Time `json:"pulled_at"`
	OriginalSHA string    `json:"original_sha"`
	LocalAction string    `json:"local_action"`
}

type HubSyncPushOptions struct {
	Scope      store.SkillScope
	Names      []string
	Author     string
	SourcePeer string
}

type HubSyncPushResult struct {
	Packaged []HubPackageEnvelope `json:"packaged"`
	Skipped  []string             `json:"skipped,omitempty"`
	Errors   []string             `json:"errors,omitempty"`
}

type PullPlan struct {
	ToAdd    []PullPlanEntry `json:"to_add"`
	ToUpdate []PullPlanEntry `json:"to_update"`
	ToSkip   []PullPlanEntry `json:"to_skip"`
	Conflict []PullPlanEntry `json:"conflict"`
}

type PullPlanEntry struct {
	Name          string `json:"name"`
	RemoteVersion int    `json:"remote_version"`
	RemoteHash    string `json:"remote_hash"`
	LocalVersion  int    `json:"local_version,omitempty"`
	LocalHash     string `json:"local_hash,omitempty"`
	Change        string `json:"change"`
}

type HubSyncPullOptions struct {
	Scope      store.SkillScope
	Packages   []HubPackageEnvelope
	DryRun     bool
	Commit     bool
	SourcePeer string
	Author     string
}

type HubSyncPullResult struct {
	Plan      PullPlan           `json:"plan"`
	Applied   []PullAppliedEntry `json:"applied,omitempty"`
	Skipped   []string           `json:"skipped,omitempty"`
	Conflicts []string           `json:"conflicts,omitempty"`
	Errors    []string           `json:"errors,omitempty"`
	DryRun    bool               `json:"dry_run"`
}

type PullAppliedEntry struct {
	Name         string         `json:"name"`
	Version      int            `json:"version"`
	ContentHash  string         `json:"content_hash"`
	Action       string         `json:"action"`
	Provenance   ProvenanceInfo `json:"provenance"`
	BundleSize   int            `json:"bundle_size,omitempty"`
	BundleSHA256 string         `json:"bundle_sha256,omitempty"`
}

type HubSyncService struct {
	reg *Registry
}

func NewHubSyncService(reg *Registry) *HubSyncService {
	return &HubSyncService{reg: reg}
}

func (s *HubSyncService) BuildManifest(ctx context.Context, opts HubSyncPushOptions) (*HubManifest, error) {
	if s.reg == nil || s.reg.store == nil {
		return nil, errors.New("hub_sync: registry not initialised")
	}

	heads, err := s.reg.ListHeads(ctx, opts.Scope, 0)
	if err != nil {
		return nil, fmt.Errorf("list heads: %w", err)
	}

	nameSet := make(map[string]bool, len(opts.Names))
	for _, n := range opts.Names {
		nameSet[strings.ToLower(n)] = true
	}

	var entries []HubManifestEntry
	for _, h := range heads {
		if len(nameSet) > 0 && !nameSet[strings.ToLower(h.Name)] {
			continue
		}
		if err := checkSyncPortableEntry(&h); err != nil {
			if errors.Is(err, ErrCompositionNotPortable) {
				if len(nameSet) > 0 {
					return nil, fmt.Errorf("hub_sync: requested manifest entry %s: %w", h.Name, err)
				}
				continue
			}
			return nil, fmt.Errorf("manifest %s: %w", h.Name, err)
		}
		entries = append(entries, HubManifestEntry{
			Name:         h.Name,
			Version:      h.Version,
			ContentHash:  h.ContentHash,
			Description:  h.Description,
			Author:       h.Author,
			BundleSHA256: h.BundleSHA256,
			SourceType:   h.SourceType,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	manifest := &HubManifest{
		Version:     HubSyncManifestVersion,
		GeneratedAt: time.Now().UTC(),
		SourcePeer:  opts.SourcePeer,
		Entries:     entries,
	}
	manifest.ManifestSHA = computeManifestSHA(manifest)
	return manifest, nil
}

func (s *HubSyncService) Push(ctx context.Context, opts HubSyncPushOptions) (*HubSyncPushResult, error) {
	if s.reg == nil || s.reg.store == nil {
		return nil, errors.New("hub_sync: registry not initialised")
	}

	result := &HubSyncPushResult{}
	for _, name := range opts.Names {
		entry, err := s.reg.Get(ctx, opts.Scope, name, VersionRef{Latest: true})
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				result.Skipped = append(result.Skipped, name)
				continue
			}
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		if err := checkSyncPortableEntry(entry); err != nil {
			return nil, fmt.Errorf("hub_sync: push %s: %w", name, err)
		}

		pkg := HubPackage{
			Manifest: HubManifestEntry{
				Name:         entry.Name,
				Version:      entry.Version,
				ContentHash:  entry.ContentHash,
				Description:  entry.Description,
				Author:       entry.Author,
				BundleSHA256: entry.BundleSHA256,
				SourceType:   entry.SourceType,
			},
			Body: entry.Body,
		}

		if entry.BundleSHA256 != "" {
			bundle, _, fetchErr := s.reg.FetchBundle(ctx, opts.Scope, name, VersionRef{Latest: true})
			if fetchErr != nil && !errors.Is(fetchErr, ErrBundleNotPresent) {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: bundle fetch: %v", name, fetchErr))
				continue
			}
			if len(bundle) > 0 {
				pkg.Bundle = bundle
			}
		}

		envelope, sealErr := sealPackage(pkg, opts.Author, opts.SourcePeer)
		if sealErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: seal: %v", name, sealErr))
			continue
		}

		result.Packaged = append(result.Packaged, *envelope)
	}
	return result, nil
}

func (s *HubSyncService) PlanPull(ctx context.Context, packages []HubPackageEnvelope) (*PullPlan, error) {
	return s.PlanPullInScope(ctx, GlobalScope(), packages)
}

func (s *HubSyncService) PlanPullInScope(ctx context.Context, scope store.SkillScope, packages []HubPackageEnvelope) (*PullPlan, error) {
	if s.reg == nil || s.reg.store == nil {
		return nil, errors.New("hub_sync: registry not initialised")
	}
	if err := checkHubPackagesPortable(packages); err != nil {
		return nil, err
	}

	plan := &PullPlan{}
	for _, env := range packages {
		pkg := env.Package
		remote := pkg.Manifest

		local, err := s.reg.Get(ctx, scope, remote.Name, VersionRef{Latest: true})
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				plan.ToAdd = append(plan.ToAdd, PullPlanEntry{
					Name:          remote.Name,
					RemoteVersion: remote.Version,
					RemoteHash:    remote.ContentHash,
					Change:        "new",
				})
				continue
			}
			return nil, fmt.Errorf("plan pull %s: %w", remote.Name, err)
		}

		if local.ContentHash == remote.ContentHash {
			plan.ToSkip = append(plan.ToSkip, PullPlanEntry{
				Name:          remote.Name,
				RemoteVersion: remote.Version,
				RemoteHash:    remote.ContentHash,
				LocalVersion:  local.Version,
				LocalHash:     local.ContentHash,
				Change:        "identical",
			})
			continue
		}

		if local.Version == remote.Version && local.ContentHash != remote.ContentHash {
			plan.Conflict = append(plan.Conflict, PullPlanEntry{
				Name:          remote.Name,
				RemoteVersion: remote.Version,
				RemoteHash:    remote.ContentHash,
				LocalVersion:  local.Version,
				LocalHash:     local.ContentHash,
				Change:        "conflict",
			})
			continue
		}

		plan.ToUpdate = append(plan.ToUpdate, PullPlanEntry{
			Name:          remote.Name,
			RemoteVersion: remote.Version,
			RemoteHash:    remote.ContentHash,
			LocalVersion:  local.Version,
			LocalHash:     local.ContentHash,
			Change:        "update",
		})
	}
	return plan, nil
}

func (s *HubSyncService) Pull(ctx context.Context, opts HubSyncPullOptions) (*HubSyncPullResult, error) {
	if s.reg == nil || s.reg.store == nil {
		return nil, errors.New("hub_sync: registry not initialised")
	}

	plan, err := s.PlanPullInScope(ctx, opts.Scope, opts.Packages)
	if err != nil {
		return nil, err
	}

	result := &HubSyncPullResult{
		Plan:      *plan,
		DryRun:    opts.DryRun,
		Conflicts: entryNames(plan.Conflict),
	}

	if opts.DryRun {
		return result, nil
	}

	if !opts.Commit {
		return nil, errors.New("hub_sync: pull requires commit=true to apply changes (set dry_run=true for preview)")
	}

	if len(plan.Conflict) > 0 {
		return nil, fmt.Errorf("hub_sync: %d conflict(s) detected — resolve before pulling", len(plan.Conflict))
	}

	workspaceID, err := pullWorkspaceID(opts.Scope)
	if err != nil {
		return nil, err
	}

	toApply := append([]PullPlanEntry{}, plan.ToAdd...)
	toApply = append(toApply, plan.ToUpdate...)

	for _, pe := range toApply {
		var env *HubPackageEnvelope
		for i := range opts.Packages {
			if opts.Packages[i].Package.Manifest.Name == pe.Name {
				env = &opts.Packages[i]
				break
			}
		}
		if env == nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: package not found in input", pe.Name))
			continue
		}

		pkg := env.Package
		prov := ProvenanceInfo{
			Source:      "hub_pull",
			SourcePeer:  opts.SourcePeer,
			PulledAt:    time.Now().UTC(),
			OriginalSHA: env.SHA256,
			LocalAction: pe.Change,
		}

		pubOpts := PublishOptions{
			Name:               pkg.Manifest.Name,
			Body:               pkg.Body,
			Author:             authorOrDefault(opts.Author, pkg.Manifest.Author),
			WorkspaceID:        workspaceID,
			SourceTypeOverride: "hub",
			MetadataExtras: map[string]any{
				"hub_provenance":   prov,
				"hub_source_peer":  opts.SourcePeer,
				"hub_original_sha": env.SHA256,
			},
		}

		if len(pkg.Bundle) > 0 {
			pubOpts.Bundle = pkg.Bundle
		}

		res, pubErr := s.reg.Publish(ctx, pubOpts)
		if pubErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: publish: %v", pe.Name, pubErr))
			continue
		}

		prov.LocalAction = res.Action
		result.Applied = append(result.Applied, PullAppliedEntry{
			Name:         res.Name,
			Version:      res.Version,
			ContentHash:  res.ContentHash,
			Action:       res.Action,
			Provenance:   prov,
			BundleSize:   res.BundleSize,
			BundleSHA256: res.BundleSHA256,
		})
	}

	return result, nil
}

func pullWorkspaceID(scope store.SkillScope) (*string, error) {
	if scope.IncludeAll || len(scope.WorkspaceIDs) == 0 {
		return nil, nil
	}
	if len(scope.WorkspaceIDs) > 1 {
		return nil, errors.New("hub_sync: pull commit requires zero or one workspace in scope")
	}
	ws := strings.TrimSpace(scope.WorkspaceIDs[0])
	if ws == "" {
		return nil, errors.New("hub_sync: pull commit workspace id is empty")
	}
	return &ws, nil
}

func (s *HubSyncService) DiffPull(ctx context.Context, packages []HubPackageEnvelope) ([]*VersionDiff, error) {
	if s.reg == nil || s.reg.store == nil {
		return nil, errors.New("hub_sync: registry not initialised")
	}
	if err := checkHubPackagesPortable(packages); err != nil {
		return nil, err
	}

	var diffs []*VersionDiff
	for _, env := range packages {
		pkg := env.Package
		remote := pkg.Manifest

		local, err := s.reg.Get(ctx, GlobalScope(), remote.Name, VersionRef{Latest: true})
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				diffs = append(diffs, &VersionDiff{
					Name:         remote.Name,
					OldVersion:   0,
					NewVersion:   remote.Version,
					BodyDiff:     "--- (not present locally)\n+++ " + remote.Name + "\n" + truncateBody(pkg.Body, 512),
					NewHasBundle: remote.BundleSHA256 != "",
				})
				continue
			}
			return nil, fmt.Errorf("diff pull %s: %w", remote.Name, err)
		}

		diff := &VersionDiff{
			Name:         remote.Name,
			OldVersion:   local.Version,
			NewVersion:   remote.Version,
			OldHasBundle: local.BundleSHA256 != "",
			NewHasBundle: remote.BundleSHA256 != "",
		}

		if local.Body != pkg.Body {
			diff.BodyDiff = unifiedDiff(local.Body, pkg.Body, "body")
		}

		oldFront := extractFrontmatter(local.Body)
		newFront := extractFrontmatter(pkg.Body)
		if oldFront != newFront {
			diff.FrontDiff = unifiedDiff(oldFront, newFront, "frontmatter")
		}

		diffs = append(diffs, diff)
	}
	return diffs, nil
}

func checkHubPackagesPortable(packages []HubPackageEnvelope) error {
	for i := range packages {
		pkg := &packages[i].Package
		if err := CheckSyncPortableBody(pkg.Manifest.Name, pkg.Body); err != nil {
			return fmt.Errorf("hub_sync: package[%d] %q: %w", i, pkg.Manifest.Name, err)
		}
	}
	return nil
}

func sealPackage(pkg HubPackage, signedBy, sourcePeer string) (*HubPackageEnvelope, error) {
	data, err := json.Marshal(pkg)
	if err != nil {
		return nil, fmt.Errorf("marshal package: %w", err)
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])

	return &HubPackageEnvelope{
		Package:    pkg,
		SHA256:     sha,
		SignedBy:   signedBy,
		SignedAt:   time.Now().UTC(),
		Provenance: sourcePeer,
	}, nil
}

func VerifyEnvelope(env *HubPackageEnvelope) error {
	if env == nil {
		return errors.New("hub_sync: nil envelope")
	}
	data, err := json.Marshal(env.Package)
	if err != nil {
		return fmt.Errorf("marshal for verify: %w", err)
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if sha != env.SHA256 {
		return fmt.Errorf("hub_sync: sha256 mismatch: computed=%s envelope=%s", sha, env.SHA256)
	}
	return nil
}

func computeManifestSHA(m *HubManifest) string {
	clean := *m
	clean.ManifestSHA = ""
	data, _ := json.Marshal(clean)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func authorOrDefault(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

func entryNames(entries []PullPlanEntry) []string {
	if len(entries) == 0 {
		return nil
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	return names
}

func truncateBody(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
