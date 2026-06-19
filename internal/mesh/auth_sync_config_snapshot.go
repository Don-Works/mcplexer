package mesh

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/don-works/mcplexer/internal/store"
)

func (m *Manager) addLinkedConfigToSnapshot(
	ctx context.Context,
	snap *authSnapshotPlain,
	scopeID string,
) (authSnapshotPlain, error) {
	routes, err := m.authSyncStore.ListRouteRules(ctx, "")
	if err != nil {
		return *snap, fmt.Errorf("list route rules: %w", err)
	}
	servers := map[string]downstreamServerSnapshot{}
	for i := range routes {
		if routes[i].AuthScopeID != scopeID {
			continue
		}
		snap.Routes = append(snap.Routes, snapshotRouteRule(&routes[i]))
		if routes[i].DownstreamServerID == "" {
			continue
		}
		if _, ok := servers[routes[i].DownstreamServerID]; ok {
			continue
		}
		server, err := m.authSyncStore.GetDownstreamServer(ctx, routes[i].DownstreamServerID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return *snap, fmt.Errorf("get downstream server: %w", err)
		}
		servers[server.ID] = snapshotDownstreamServer(server)
	}
	for _, server := range servers {
		snap.Servers = append(snap.Servers, server)
	}
	sort.Slice(snap.Servers, func(i, j int) bool {
		return snap.Servers[i].ID < snap.Servers[j].ID
	})
	sort.Slice(snap.Routes, func(i, j int) bool {
		if snap.Routes[i].Priority == snap.Routes[j].Priority {
			return snap.Routes[i].ID < snap.Routes[j].ID
		}
		return snap.Routes[i].Priority > snap.Routes[j].Priority
	})
	return *snap, nil
}

func snapshotDownstreamServer(s *store.DownstreamServer) downstreamServerSnapshot {
	return downstreamServerSnapshot{
		ID:             s.ID,
		Name:           s.Name,
		Transport:      s.Transport,
		Command:        s.Command,
		Args:           cloneRaw(s.Args),
		URL:            cloneStringPtr(s.URL),
		ToolNamespace:  s.ToolNamespace,
		Discovery:      s.Discovery,
		CacheConfig:    cloneRaw(s.CacheConfig),
		IdleTimeoutSec: s.IdleTimeoutSec,
		CallTimeoutSec: s.CallTimeoutSec,
		MaxInstances:   s.MaxInstances,
		RestartPolicy:  s.RestartPolicy,
		Disabled:       s.Disabled,
		Source:         s.Source,
	}
}

func snapshotRouteRule(r *store.RouteRule) routeRuleSnapshot {
	return routeRuleSnapshot{
		ID:                 r.ID,
		Name:               r.Name,
		Priority:           r.Priority,
		WorkspaceID:        r.WorkspaceID,
		PathGlob:           r.PathGlob,
		ToolMatch:          cloneRaw(r.ToolMatch),
		ScopePolicy:        cloneRaw(r.ScopePolicy),
		DownstreamServerID: r.DownstreamServerID,
		AuthScopeID:        r.AuthScopeID,
		Policy:             r.Policy,
		LogLevel:           r.LogLevel,
		ApprovalMode:       r.ApprovalMode,
		ApprovalTimeout:    r.ApprovalTimeout,
		Source:             r.Source,
	}
}

func cloneStringPtr(in *string) *string {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
