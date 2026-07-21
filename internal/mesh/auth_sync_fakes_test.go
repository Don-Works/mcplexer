package mesh

import (
	"context"

	"github.com/don-works/mcplexer/internal/store"
)

type fakeAuthSyncStore struct {
	scopes    map[string]*store.AuthScope
	providers map[string]*store.OAuthProvider
	servers   map[string]*store.DownstreamServer
	routes    map[string]*store.RouteRule
	bindings  map[string]*store.WorkspacePeerBinding
	peers     map[string]*store.P2PPeer
}

func newFakeAuthSyncStore() *fakeAuthSyncStore {
	return &fakeAuthSyncStore{
		scopes:    map[string]*store.AuthScope{},
		providers: map[string]*store.OAuthProvider{},
		servers:   map[string]*store.DownstreamServer{},
		routes:    map[string]*store.RouteRule{},
		bindings:  map[string]*store.WorkspacePeerBinding{},
		peers:     map[string]*store.P2PPeer{},
	}
}

func (f *fakeAuthSyncStore) CreateAuthScope(_ context.Context, a *store.AuthScope) error {
	if a.ID == "" {
		a.ID = "scope-generated"
	}
	if _, ok := f.scopes[a.ID]; ok {
		return store.ErrAlreadyExists
	}
	f.scopes[a.ID] = cloneScope(a)
	return nil
}

func (f *fakeAuthSyncStore) GetAuthScope(_ context.Context, id string) (*store.AuthScope, error) {
	a, ok := f.scopes[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return cloneScope(a), nil
}

func (f *fakeAuthSyncStore) GetAuthScopeByName(_ context.Context, name string) (*store.AuthScope, error) {
	for _, a := range f.scopes {
		if a.Name == name {
			return cloneScope(a), nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeAuthSyncStore) ListAuthScopes(_ context.Context) ([]store.AuthScope, error) {
	out := make([]store.AuthScope, 0, len(f.scopes))
	for _, a := range f.scopes {
		out = append(out, *cloneScope(a))
	}
	return out, nil
}

func (f *fakeAuthSyncStore) UpdateAuthScope(_ context.Context, a *store.AuthScope) error {
	if _, ok := f.scopes[a.ID]; !ok {
		return store.ErrNotFound
	}
	cp := cloneScope(a)
	cp.EncryptedData = f.scopes[a.ID].EncryptedData
	cp.OAuthTokenData = f.scopes[a.ID].OAuthTokenData
	f.scopes[a.ID] = cp
	return nil
}

func (f *fakeAuthSyncStore) UpdateAuthScopeTokenData(_ context.Context, id string, data []byte) error {
	if _, ok := f.scopes[id]; !ok {
		return store.ErrNotFound
	}
	f.scopes[id].OAuthTokenData = cloneBytes(data)
	return nil
}

func (f *fakeAuthSyncStore) UpdateAuthScopeEncryptedData(_ context.Context, id string, data []byte) error {
	if _, ok := f.scopes[id]; !ok {
		return store.ErrNotFound
	}
	f.scopes[id].EncryptedData = cloneBytes(data)
	return nil
}

func (f *fakeAuthSyncStore) CreateOAuthProvider(_ context.Context, p *store.OAuthProvider) error {
	if p.ID == "" {
		p.ID = "provider-generated"
	}
	f.providers[p.ID] = cloneProvider(p)
	return nil
}

func (f *fakeAuthSyncStore) GetOAuthProvider(_ context.Context, id string) (*store.OAuthProvider, error) {
	p, ok := f.providers[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return cloneProvider(p), nil
}

func (f *fakeAuthSyncStore) GetOAuthProviderByName(_ context.Context, name string) (*store.OAuthProvider, error) {
	for _, p := range f.providers {
		if p.Name == name {
			return cloneProvider(p), nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeAuthSyncStore) UpdateOAuthProvider(_ context.Context, p *store.OAuthProvider) error {
	if _, ok := f.providers[p.ID]; !ok {
		return store.ErrNotFound
	}
	f.providers[p.ID] = cloneProvider(p)
	return nil
}

func (f *fakeAuthSyncStore) CreateDownstreamServer(_ context.Context, s *store.DownstreamServer) error {
	if s.ID == "" {
		s.ID = "server-generated"
	}
	f.servers[s.ID] = cloneServer(s)
	return nil
}

func (f *fakeAuthSyncStore) GetDownstreamServer(_ context.Context, id string) (*store.DownstreamServer, error) {
	s, ok := f.servers[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return cloneServer(s), nil
}

func (f *fakeAuthSyncStore) GetDownstreamServerByName(
	_ context.Context,
	name string,
) (*store.DownstreamServer, error) {
	for _, s := range f.servers {
		if s.Name == name {
			return cloneServer(s), nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeAuthSyncStore) UpdateDownstreamServer(_ context.Context, s *store.DownstreamServer) error {
	if _, ok := f.servers[s.ID]; !ok {
		return store.ErrNotFound
	}
	f.servers[s.ID] = cloneServer(s)
	return nil
}

func (f *fakeAuthSyncStore) CreateRouteRule(_ context.Context, r *store.RouteRule) error {
	if r.ID == "" {
		r.ID = "route-generated"
	}
	f.routes[r.ID] = cloneRoute(r)
	return nil
}

func (f *fakeAuthSyncStore) GetRouteRule(_ context.Context, id string) (*store.RouteRule, error) {
	r, ok := f.routes[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return cloneRoute(r), nil
}

func (f *fakeAuthSyncStore) ListRouteRules(_ context.Context, workspaceID string) ([]store.RouteRule, error) {
	out := make([]store.RouteRule, 0, len(f.routes))
	for _, r := range f.routes {
		if workspaceID != "" && r.WorkspaceID != workspaceID {
			continue
		}
		out = append(out, *cloneRoute(r))
	}
	return out, nil
}

func (f *fakeAuthSyncStore) UpdateRouteRule(_ context.Context, r *store.RouteRule) error {
	if _, ok := f.routes[r.ID]; !ok {
		return store.ErrNotFound
	}
	f.routes[r.ID] = cloneRoute(r)
	return nil
}

func (f *fakeAuthSyncStore) GetWorkspacePeerBinding(
	_ context.Context,
	peerID, remoteWorkspaceID string,
) (*store.WorkspacePeerBinding, error) {
	b, ok := f.bindings[bindingKey(peerID, remoteWorkspaceID)]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *b
	return &cp, nil
}

func (f *fakeAuthSyncStore) GetPeer(_ context.Context, peerID string) (*store.P2PPeer, error) {
	p, ok := f.peers[peerID]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *p
	cp.Scopes = append([]string(nil), p.Scopes...)
	return &cp, nil
}

func (f *fakeAuthSyncStore) ListPeers(_ context.Context) ([]store.P2PPeer, error) {
	out := make([]store.P2PPeer, 0, len(f.peers))
	for _, p := range f.peers {
		cp := *p
		cp.Scopes = append([]string(nil), p.Scopes...)
		out = append(out, cp)
	}
	return out, nil
}

func (f *fakeAuthSyncStore) HasPeerScope(_ context.Context, peerID, scope string) (bool, error) {
	p, ok := f.peers[peerID]
	if !ok || p.RevokedAt != nil {
		return false, nil
	}
	for _, s := range p.Scopes {
		if s == scope {
			return true, nil
		}
	}
	return false, nil
}

func bindingKey(peerID, remoteWorkspaceID string) string {
	return peerID + "\x00" + remoteWorkspaceID
}

func cloneBytes(in []byte) []byte {
	if len(in) == 0 {
		return nil
	}
	out := make([]byte, len(in))
	copy(out, in)
	return out
}

func cloneServer(s *store.DownstreamServer) *store.DownstreamServer {
	cp := *s
	cp.Args = cloneRaw(s.Args)
	cp.URL = cloneStringPtr(s.URL)
	cp.CapabilitiesCache = cloneRaw(s.CapabilitiesCache)
	cp.CacheConfig = cloneRaw(s.CacheConfig)
	return &cp
}

func cloneRoute(r *store.RouteRule) *store.RouteRule {
	cp := *r
	cp.ToolMatch = cloneRaw(r.ToolMatch)
	cp.ScopePolicy = cloneRaw(r.ScopePolicy)
	return &cp
}
