package config

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// Service provides config CRUD with validation, wrapping the store.
type Service struct {
	store store.Store
}

// NewService creates a config Service.
func NewService(s store.Store) *Service {
	return &Service{store: s}
}

// CreateWorkspace validates and creates a workspace.
func (s *Service) CreateWorkspace(ctx context.Context, w *store.Workspace) error {
	if w.DefaultPolicy == "" {
		w.DefaultPolicy = "deny"
	}
	if err := validatePolicy(w.DefaultPolicy); err != nil {
		return err
	}
	now := time.Now().UTC()
	w.CreatedAt = now
	w.UpdatedAt = now
	return s.store.CreateWorkspace(ctx, w)
}

// UpdateWorkspace validates and updates a workspace.
func (s *Service) UpdateWorkspace(ctx context.Context, w *store.Workspace) error {
	if err := validatePolicy(w.DefaultPolicy); err != nil {
		return err
	}
	w.UpdatedAt = time.Now().UTC()
	return s.store.UpdateWorkspace(ctx, w)
}

// CreateDownstreamServer validates and creates a downstream server.
func (s *Service) CreateDownstreamServer(ctx context.Context, d *store.DownstreamServer) error {
	if err := validateTransport(d.Transport); err != nil {
		return err
	}
	if err := s.checkNamespaceUnique(ctx, d.ToolNamespace, d.ID); err != nil {
		return err
	}
	if d.Discovery == "" {
		if d.Transport == "internal" {
			d.Discovery = "static"
		} else {
			d.Discovery = "dynamic"
		}
	}
	now := time.Now().UTC()
	d.CreatedAt = now
	d.UpdatedAt = now
	return s.store.CreateDownstreamServer(ctx, d)
}

// UpdateDownstreamServer validates and updates a downstream server.
func (s *Service) UpdateDownstreamServer(ctx context.Context, d *store.DownstreamServer) error {
	if err := validateTransport(d.Transport); err != nil {
		return err
	}
	if err := s.checkNamespaceUnique(ctx, d.ToolNamespace, d.ID); err != nil {
		return err
	}
	d.UpdatedAt = time.Now().UTC()
	return s.store.UpdateDownstreamServer(ctx, d)
}

// CreateRouteRule validates references and creates a route rule.
func (s *Service) CreateRouteRule(ctx context.Context, r *store.RouteRule) error {
	if err := s.validateRouteRefs(ctx, r); err != nil {
		return err
	}
	now := time.Now().UTC()
	r.CreatedAt = now
	r.UpdatedAt = now
	return s.store.CreateRouteRule(ctx, r)
}

// UpdateRouteRule validates references and updates a route rule.
func (s *Service) UpdateRouteRule(ctx context.Context, r *store.RouteRule) error {
	if err := s.validateRouteRefs(ctx, r); err != nil {
		return err
	}
	r.UpdatedAt = time.Now().UTC()
	return s.store.UpdateRouteRule(ctx, r)
}

// BulkCreateRouteRules validates and creates multiple route rules atomically.
func (s *Service) BulkCreateRouteRules(ctx context.Context, rules []store.RouteRule) error {
	return s.store.Tx(ctx, func(txStore store.Store) error {
		svc := NewService(txStore)
		for i := range rules {
			if err := svc.CreateRouteRule(ctx, &rules[i]); err != nil {
				return fmt.Errorf("rule[%d] %q: %w", i, rules[i].Name, err)
			}
		}
		return nil
	})
}

// CreateOAuthProvider validates and creates an OAuth provider.
func (s *Service) CreateOAuthProvider(ctx context.Context, p *store.OAuthProvider) error {
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	return s.store.CreateOAuthProvider(ctx, p)
}

// UpdateOAuthProvider validates and updates an OAuth provider.
func (s *Service) UpdateOAuthProvider(ctx context.Context, p *store.OAuthProvider) error {
	p.UpdatedAt = time.Now().UTC()
	return s.store.UpdateOAuthProvider(ctx, p)
}

// CreateAuthScope validates and creates an auth scope.
func (s *Service) CreateAuthScope(ctx context.Context, a *store.AuthScope) error {
	now := time.Now().UTC()
	a.CreatedAt = now
	a.UpdatedAt = now
	return s.store.CreateAuthScope(ctx, a)
}

// UpdateAuthScope validates and updates an auth scope.
func (s *Service) UpdateAuthScope(ctx context.Context, a *store.AuthScope) error {
	a.UpdatedAt = time.Now().UTC()
	return s.store.UpdateAuthScope(ctx, a)
}

// Export serializes the current downstream servers to a FileConfig for YAML export.
func (s *Service) Export(ctx context.Context) (*FileConfig, error) {
	downstreams, err := s.store.ListDownstreamServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list downstreams: %w", err)
	}

	cfg := &FileConfig{}
	for _, d := range downstreams {
		var args []string
		if len(d.Args) > 0 {
			_ = json.Unmarshal(d.Args, &args)
		}
		dc := downstreamServerConfig{
			ID: d.ID, Name: d.Name, Transport: d.Transport,
			Command: d.Command, Args: args, ToolNamespace: d.ToolNamespace,
			IdleTimeoutSec: d.IdleTimeoutSec, MaxInstances: d.MaxInstances,
			RestartPolicy: d.RestartPolicy,
		}
		if d.URL != nil {
			dc.URL = *d.URL
		}
		cfg.DownstreamServers = append(cfg.DownstreamServers, dc)
	}

	return cfg, nil
}

func (s *Service) checkNamespaceUnique(ctx context.Context, ns, excludeID string) error {
	servers, err := s.store.ListDownstreamServers(ctx)
	if err != nil {
		return fmt.Errorf("check namespace: %w", err)
	}
	for _, srv := range servers {
		if srv.ToolNamespace == ns && srv.ID != excludeID {
			return fmt.Errorf("namespace %q already used by server %q", ns, srv.ID)
		}
	}
	return nil
}

func (s *Service) validateRouteRefs(ctx context.Context, r *store.RouteRule) error {
	if r.WorkspaceID != "" {
		if _, err := s.store.GetWorkspace(ctx, r.WorkspaceID); err != nil {
			return fmt.Errorf("workspace %q: %w", r.WorkspaceID, err)
		}
	}
	if r.DownstreamServerID != "" {
		if _, err := s.store.GetDownstreamServer(ctx, r.DownstreamServerID); err != nil {
			return fmt.Errorf("downstream %q: %w", r.DownstreamServerID, err)
		}
	}
	if r.AuthScopeID != "" {
		if _, err := s.store.GetAuthScope(ctx, r.AuthScopeID); err != nil {
			return fmt.Errorf("auth scope %q: %w", r.AuthScopeID, err)
		}
	}
	if err := validateGlob(r.PathGlob); err != nil {
		return err
	}
	if err := validateToolMatch(r.ToolMatch); err != nil {
		return err
	}
	if err := validateScopePolicy(r.ScopePolicy); err != nil {
		return err
	}
	return validatePolicy(r.Policy)
}

// validateToolMatch ensures tool_match is a JSON string array (or empty/nil).
func validateToolMatch(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return fmt.Errorf("tool_match must be a JSON array of strings")
	}
	for i, s := range arr {
		if s == "" {
			return fmt.Errorf("tool_match[%d]: empty string not allowed", i)
		}
	}
	return nil
}

func validateScopePolicy(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var parsed map[string][]string
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("scope_policy: must be a JSON object with string array values")
	}
	for key, values := range parsed {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("scope_policy: empty resource type key not allowed")
		}
		for i, v := range values {
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("scope_policy.%s[%d]: empty string not allowed", key, i)
			}
		}
	}
	return nil
}
