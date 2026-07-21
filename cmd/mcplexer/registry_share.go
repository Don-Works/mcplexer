package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/consent"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// buildRegistryShareService wires the agent-facing SKILL.md registry share
// service. It is separate from buildSkillShareService, which handles legacy
// signed .mcskill bundles from installed_skills.
func buildRegistryShareService(
	host *p2p.Host,
	db *sqlite.DB,
	reg *skillregistry.Registry,
	auditor *audit.Logger,
	resolver consent.Resolver,
	selfUser *store.User,
) *p2p.RegistryShareService {
	if host == nil || reg == nil {
		return nil
	}
	if resolver == nil {
		resolver = consent.NopResolver{}
	}
	lookup := &storePairedLookup{db: db}
	adapter := &registryShareAdapter{reg: reg}
	auditAdapter := &skillShareAuditAdapter{
		auditor:  auditor,
		resolver: resolver,
		selfUser: selfUser,
	}
	return p2p.NewRegistryShareService(
		host, lookup, adapter, adapter, adapter, adapter, auditAdapter, slog.Default(),
	)
}

type registryShareAdapter struct {
	reg *skillregistry.Registry
}

func (a *registryShareAdapter) GetRegistryEntry(
	ctx context.Context, name string, version int,
) (string, []byte, string, error) {
	ref := skillregistry.VersionRef{Latest: true}
	if version > 0 {
		ref = skillregistry.VersionRef{Version: version}
	}
	entry, err := a.reg.Get(ctx, skillregistry.GlobalScope(), name, ref)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", nil, "", fmt.Errorf("%w: %s", p2p.ErrRegistryEntryNotFound, name)
		}
		return "", nil, "", err
	}
	if err := skillregistry.CheckSyncPortableBody(entry.Name, entry.Body); err != nil {
		return "", nil, "", fmt.Errorf("serve registry entry %s: %w", entry.Name, err)
	}
	if entry.BundleSHA256 == "" {
		return entry.Body, nil, "", nil
	}
	bundle, sha, err := a.reg.FetchBundle(ctx, skillregistry.GlobalScope(),
		entry.Name, skillregistry.VersionRef{Version: entry.Version})
	if err != nil {
		return "", nil, "", err
	}
	return entry.Body, bundle, sha, nil
}

func (a *registryShareAdapter) HandleIncomingRegistryEntry(
	ctx context.Context, peerID, name, body string, bundle []byte,
) error {
	if err := skillregistry.CheckSyncPortableBody(name, body); err != nil {
		return fmt.Errorf("receive registry entry %s: %w", name, err)
	}
	_, err := a.reg.Publish(ctx, skillregistry.PublishOptions{
		Name:   name,
		Body:   body,
		Author: "p2p:" + peerID,
		Bundle: bundle,
		MetadataExtras: map[string]any{
			"imported_via":   "mesh.registry_request",
			"origin_peer_id": peerID,
		},
	})
	return err
}

func (a *registryShareAdapter) ListIndexEntries(
	ctx context.Context,
) ([]p2p.HubIndexEntry, error) {
	heads, err := a.reg.ListHeads(ctx, skillregistry.GlobalScope(), 0)
	if err != nil {
		return nil, err
	}
	out := make([]p2p.HubIndexEntry, 0, len(heads))
	for _, entry := range heads {
		if err := skillregistry.CheckSyncPortableBody(entry.Name, entry.Body); err != nil {
			if errors.Is(err, skillregistry.ErrCompositionNotPortable) {
				continue
			}
			return nil, fmt.Errorf("index registry entry %s: %w", entry.Name, err)
		}
		out = append(out, p2p.HubIndexEntry{
			Name:        entry.Name,
			Version:     entry.Version,
			ContentHash: entry.ContentHash,
			Description: entry.Description,
			Author:      entry.Author,
			BundleSHA:   entry.BundleSHA256,
		})
	}
	return out, nil
}

func (a *registryShareAdapter) SearchIndexEntries(
	ctx context.Context, q string, limit int,
) ([]p2p.HubSearchHit, error) {
	hits, err := a.reg.Search(ctx, skillregistry.GlobalScope(), q, limit)
	if err != nil {
		return nil, err
	}
	out := make([]p2p.HubSearchHit, 0, len(hits))
	for _, hit := range hits {
		entry, err := a.reg.Get(ctx, skillregistry.GlobalScope(),
			hit.Name, skillregistry.VersionRef{Version: hit.Version})
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := skillregistry.CheckSyncPortableBody(entry.Name, entry.Body); err != nil {
			if errors.Is(err, skillregistry.ErrCompositionNotPortable) {
				continue
			}
			return nil, fmt.Errorf("search registry entry %s: %w", entry.Name, err)
		}
		out = append(out, p2p.HubSearchHit{
			Name:        hit.Name,
			Version:     hit.Version,
			Score:       hit.Score,
			ContentHash: entry.ContentHash,
			Description: hit.Description,
			Author:      entry.Author,
			BundleSHA:   entry.BundleSHA256,
			Scope:       hit.Scope,
		})
	}
	return out, nil
}
