package mesh

import (
	"context"
	"errors"
	"log/slog"

	"github.com/don-works/mcplexer/internal/store"
)

func (m *Manager) upsertAuthLinkedConfig(
	ctx context.Context,
	senderPeerID string,
	snap authSnapshotPlain,
	localScopeID string,
) error {
	if len(snap.Routes) == 0 {
		return nil
	}
	servers := make(map[string]downstreamServerSnapshot, len(snap.Servers))
	for _, server := range snap.Servers {
		servers[server.ID] = server
	}
	localServerIDs := map[string]string{}
	for _, route := range snap.Routes {
		workspaceID, ok, err := m.localWorkspaceForAuthRoute(ctx, senderPeerID, route.WorkspaceID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		serverID := ""
		if route.DownstreamServerID != "" {
			server, ok := servers[route.DownstreamServerID]
			if !ok {
				continue
			}
			serverID, err = m.localServerIDForRoute(ctx, senderPeerID, localServerIDs, server)
			if err != nil {
				return err
			}
		}
		if err := m.upsertRouteRule(ctx, senderPeerID, route, workspaceID, serverID, localScopeID); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) localServerIDForRoute(
	ctx context.Context,
	senderPeerID string,
	cache map[string]string,
	snap downstreamServerSnapshot,
) (string, error) {
	if got := cache[snap.ID]; got != "" {
		return got, nil
	}
	id, err := m.upsertDownstreamServer(ctx, senderPeerID, snap)
	if err != nil {
		return "", err
	}
	cache[snap.ID] = id
	return id, nil
}

func (m *Manager) localWorkspaceForAuthRoute(
	ctx context.Context,
	senderPeerID, remoteWorkspaceID string,
) (string, bool, error) {
	if remoteWorkspaceID == "" {
		return "", true, nil
	}
	if senderPeerID == "" {
		return "", false, nil
	}
	binding, err := m.authSyncStore.GetWorkspacePeerBinding(ctx, senderPeerID, remoteWorkspaceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	if binding.LocalWorkspaceID == "" {
		return "", false, nil
	}
	return binding.LocalWorkspaceID, true, nil
}

func (m *Manager) upsertDownstreamServer(ctx context.Context, senderPeerID string, snap downstreamServerSnapshot) (string, error) {
	server := &store.DownstreamServer{
		ID:             snap.ID,
		Name:           snap.Name,
		Transport:      snap.Transport,
		Command:        snap.Command,
		Args:           cloneRaw(snap.Args),
		URL:            cloneStringPtr(snap.URL),
		ToolNamespace:  snap.ToolNamespace,
		Discovery:      snap.Discovery,
		CacheConfig:    cloneRaw(snap.CacheConfig),
		IdleTimeoutSec: snap.IdleTimeoutSec,
		CallTimeoutSec: snap.CallTimeoutSec,
		MaxInstances:   snap.MaxInstances,
		RestartPolicy:  snap.RestartPolicy,
		Disabled:       snap.Disabled,
		Source:         meshImportSource(senderPeerID),
	}
	existing, err := m.findExistingServer(ctx, snap)
	if err != nil {
		return "", err
	}
	if existing != nil {
		if !importClobberOK(existing.Source, senderPeerID) {
			// Refuse to overwrite a locally-authored or other-peer server:
			// the Command/Args fields execute locally, so a silent clobber
			// would be a code-execution foothold.
			slog.Default().Warn("p2p: auth_sync preserving local downstream server, skipping import overwrite",
				"name", existing.Name, "peer", senderPeerID, "existing_source", existing.Source)
			return existing.ID, nil
		}
		server.ID = existing.ID
		server.CapabilitiesCache = cloneRaw(existing.CapabilitiesCache)
		return server.ID, m.authSyncStore.UpdateDownstreamServer(ctx, server)
	}
	if err := m.authSyncStore.CreateDownstreamServer(ctx, server); err != nil {
		return "", err
	}
	return server.ID, nil
}

func (m *Manager) findExistingServer(ctx context.Context, snap downstreamServerSnapshot) (*store.DownstreamServer, error) {
	existing, err := m.authSyncStore.GetDownstreamServerByName(ctx, snap.Name)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if snap.ID == "" {
		return nil, nil
	}
	existing, err = m.authSyncStore.GetDownstreamServer(ctx, snap.ID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return nil, nil
}

func (m *Manager) upsertRouteRule(
	ctx context.Context,
	senderPeerID string,
	snap routeRuleSnapshot,
	workspaceID, serverID, scopeID string,
) error {
	route := &store.RouteRule{
		ID:                 snap.ID,
		Name:               snap.Name,
		Priority:           snap.Priority,
		WorkspaceID:        workspaceID,
		PathGlob:           snap.PathGlob,
		ToolMatch:          cloneRaw(snap.ToolMatch),
		ScopePolicy:        cloneRaw(snap.ScopePolicy),
		DownstreamServerID: serverID,
		AuthScopeID:        scopeID,
		Policy:             snap.Policy,
		LogLevel:           snap.LogLevel,
		ApprovalMode:       snap.ApprovalMode,
		ApprovalTimeout:    snap.ApprovalTimeout,
		Source:             meshImportSource(senderPeerID),
	}
	if snap.ID != "" {
		if existing, err := m.authSyncStore.GetRouteRule(ctx, snap.ID); err == nil {
			if !importClobberOK(existing.Source, senderPeerID) {
				slog.Default().Warn("p2p: auth_sync preserving local route rule, skipping import overwrite",
					"name", existing.Name, "peer", senderPeerID, "existing_source", existing.Source)
				return nil
			}
			route.ID = existing.ID
			return m.authSyncStore.UpdateRouteRule(ctx, route)
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return m.authSyncStore.CreateRouteRule(ctx, route)
}
