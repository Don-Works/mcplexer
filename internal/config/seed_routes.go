package config

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// defaultRouteRules defines the built-in route rules seeded on first run.
// The builtin-allow rule at high priority ensures MCPlexer built-in tools are
// accessible by default. A global deny-all at low priority ensures deny-first routing.
var defaultRouteRules = []store.RouteRule{
	{
		ID:                 "builtin-allow",
		Name:               "Allow MCPlexer built-in tools",
		Priority:           100,
		WorkspaceID:        "global",
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["mcpx__*"]`),
		DownstreamServerID: "mcpx-builtin",
		Policy:             "allow",
		Source:             "default",
	},
	{
		ID:                 "mesh-allow",
		Name:               "Allow agent mesh tools",
		Priority:           99,
		WorkspaceID:        "global",
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["mesh__*"]`),
		DownstreamServerID: "mesh-builtin",
		Policy:             "allow",
		Source:             "default",
	},
	{
		ID:                 "telegram-allow",
		Name:               "Allow Telegram tools",
		Priority:           98,
		WorkspaceID:        "global",
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["telegram__*"]`),
		DownstreamServerID: "telegram",
		Policy:             "allow",
		Source:             "default",
	},
	{
		ID:                 "lmstudio-allow",
		Name:               "Allow LM Studio tools",
		Priority:           98,
		WorkspaceID:        "global",
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["lmstudio__*"]`),
		DownstreamServerID: "lmstudio",
		Policy:             "allow",
		Source:             "default",
	},
	{
		ID:                 "secret-allow",
		Name:               "Allow secret prompt tools",
		Priority:           97,
		WorkspaceID:        "global",
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["secret__*"]`),
		DownstreamServerID: "secret-builtin",
		Policy:             "allow",
		Source:             "default",
	},
	{
		// mcplexer__* admin tools route to the in-process control backend.
		// Routing is scoped to the global workspace path glob and admitted
		// at high priority so it sits above the global-deny. Visibility is
		// further constrained by AdminCWDGate at the gateway, so admin
		// tools only LIST inside ~/.mcplexer — but the route must exist
		// so that legitimate admin sessions can dispatch them.
		ID:                 "mcplexer-admin-allow",
		Name:               "Allow MCPlexer self-CRUD admin tools",
		Priority:           97,
		WorkspaceID:        "global",
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["mcplexer__*"]`),
		DownstreamServerID: "mcplexer",
		Policy:             "allow",
		Source:             "default",
	},
	{
		ID:                 "email-allow",
		Name:               "Allow agent email tools",
		Priority:           96,
		WorkspaceID:        "global",
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["email__*"]`),
		DownstreamServerID: "email-builtin",
		Policy:             "allow",
		Source:             "default",
	},
	{
		ID:                 "memory-allow",
		Name:               "Allow universal memory tools",
		Priority:           95,
		WorkspaceID:        "global",
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["memory__*"]`),
		DownstreamServerID: "memory-builtin",
		Policy:             "allow",
		Source:             "default",
	},
	{
		ID:                 "task-allow",
		Name:               "Allow universal task tools",
		Priority:           95,
		WorkspaceID:        "global",
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["task__*"]`),
		DownstreamServerID: "task-builtin",
		Policy:             "allow",
		Source:             "default",
	},
	{
		ID:                 "skill-allow",
		Name:               "Allow skill telemetry tools (W2)",
		Priority:           95,
		WorkspaceID:        "global",
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["skill__*"]`),
		DownstreamServerID: "skill-builtin",
		Policy:             "allow",
		Source:             "default",
	},
	{
		ID:                 hammerspoonRouteID,
		Name:               "Allow Hammerspoon tools",
		Priority:           94,
		WorkspaceID:        "global",
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["hammerspoon__*"]`),
		DownstreamServerID: hammerspoonServerID,
		AuthScopeID:        hammerspoonAuthScopeID,
		Policy:             "allow",
		Source:             "default",
	},
	{
		ID:                 "brain-allow",
		Name:               "Allow universal brain tools",
		Priority:           95,
		WorkspaceID:        "global",
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["brain__*"]`),
		DownstreamServerID: "brain-builtin",
		Policy:             "allow",
		Source:             "default",
	},
	{
		// data__* (workbench scratch) and kv__* (code-mode key/value) are
		// code-mode-only built-ins dispatched in-process by handleBuiltinCall.
		// Each routes to its own internal server whose ToolNamespace matches
		// the tool prefix — the routing namespace guard (engine.matchRoute)
		// rejects a rule whose downstream namespace doesn't prefix the tool,
		// so they cannot share mcpx-builtin (ns "mcpx"). Without these rules
		// the tools fall to global-deny → "no matching route".
		ID:                 "data-allow",
		Name:               "Allow data workbench tools",
		Priority:           95,
		WorkspaceID:        "global",
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["data__*"]`),
		DownstreamServerID: "data-builtin",
		Policy:             "allow",
		Source:             "default",
	},
	{
		ID:                 "kv-allow",
		Name:               "Allow code-mode kv tools",
		Priority:           95,
		WorkspaceID:        "global",
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["kv__*"]`),
		DownstreamServerID: "kv-builtin",
		Policy:             "allow",
		Source:             "default",
	},
	{
		ID:                 "index-allow",
		Name:               "Allow code-index tools",
		Priority:           95,
		WorkspaceID:        "global",
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["index__*"]`),
		DownstreamServerID: "index-builtin",
		Policy:             "allow",
		Source:             "default",
	},
	{
		ID:                 "monitoring-allow",
		Name:               "Allow Monitoring tools",
		Priority:           95,
		WorkspaceID:        "global",
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["monitoring__*"]`),
		DownstreamServerID: "monitoring-builtin",
		Policy:             "allow",
		Source:             "default",
	},
	{
		ID:        "global-deny",
		Priority:  0,
		PathGlob:  "**",
		ToolMatch: json.RawMessage(`["*"]`),
		Policy:    "deny",
		LogLevel:  "info",
		Source:    "default",
	},
}

// defaultWorkspaces defines the built-in workspaces seeded on first run.
var defaultWorkspaces = []store.Workspace{
	{
		ID:            "global",
		Name:          "Global",
		RootPath:      "/",
		DefaultPolicy: "deny",
		Source:        "default",
	},
}

// SeedDefaultRouteRules creates route rules if none exist.
// Seeds a builtin-allow at high priority and global deny-all at lowest priority.
// For existing databases, ensures required default rules exist.
func SeedDefaultRouteRules(ctx context.Context, s store.Store) error {
	existing, err := s.ListRouteRules(ctx, "")
	if err != nil {
		return err
	}

	if len(existing) > 0 {
		return ensureRequiredDefaultRouteRules(ctx, s, existing)
	}

	slog.Info("seeding default route rules", "count", len(defaultRouteRules))

	now := time.Now().UTC()
	for _, r := range defaultRouteRules {
		r.CreatedAt = now
		r.UpdatedAt = now
		if err := s.CreateRouteRule(ctx, &r); err != nil {
			return err
		}
		slog.Info("seeded route rule",
			"id", r.ID, "priority", r.Priority, "policy", r.Policy,
			"path_glob", r.PathGlob)
	}
	return nil
}

// ensureRequiredDefaultRouteRules creates critical default route rules if missing.
func ensureRequiredDefaultRouteRules(ctx context.Context, s store.Store, existing []store.RouteRule) error {
	requiredIDs := []string{
		"builtin-allow",
		"mesh-allow",
		"telegram-allow",
		"lmstudio-allow",
		"secret-allow",
		"mcplexer-admin-allow",
		"email-allow",
		"memory-allow",
		"task-allow",
		"brain-allow",
		"data-allow",
		"kv-allow",
		"index-allow",
		"monitoring-allow",
		hammerspoonRouteID,
	}

	existingByID := make(map[string]struct{}, len(existing))
	for _, r := range existing {
		existingByID[r.ID] = struct{}{}
	}

	now := time.Now().UTC()
	for _, id := range requiredIDs {
		if _, ok := existingByID[id]; ok {
			continue
		}

		seed, ok := defaultRouteRuleByID(id)
		if !ok {
			continue
		}

		// Skip seeding if dependencies are missing. They may be added later.
		if seed.WorkspaceID != "" {
			if _, err := s.GetWorkspace(ctx, seed.WorkspaceID); err != nil {
				slog.Warn("skipping default route seed (missing workspace)", "id", seed.ID, "workspace_id", seed.WorkspaceID)
				continue
			}
		}
		if seed.DownstreamServerID != "" {
			if _, err := s.GetDownstreamServer(ctx, seed.DownstreamServerID); err != nil {
				slog.Warn("skipping default route seed (missing downstream server)", "id", seed.ID, "downstream_server_id", seed.DownstreamServerID)
				continue
			}
		}
		if seed.AuthScopeID != "" {
			if _, err := s.GetAuthScope(ctx, seed.AuthScopeID); err != nil {
				slog.Warn("skipping default route seed (missing auth scope)", "id", seed.ID, "auth_scope_id", seed.AuthScopeID)
				continue
			}
		}

		seed.CreatedAt = now
		seed.UpdatedAt = now
		if err := s.CreateRouteRule(ctx, &seed); err != nil {
			return err
		}
		slog.Info("migrated: seeded default route rule", "id", seed.ID, "name", seed.Name)
	}
	return nil
}

func defaultRouteRuleByID(id string) (store.RouteRule, bool) {
	for _, r := range defaultRouteRules {
		if r.ID == id {
			return r, true
		}
	}
	return store.RouteRule{}, false
}

// SeedDefaultWorkspaces creates workspace records if none exist.
func SeedDefaultWorkspaces(ctx context.Context, s store.Store) error {
	existing, err := s.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return ensureGlobalWorkspace(ctx, s, existing)
	}

	slog.Info("seeding default workspaces", "count", len(defaultWorkspaces))

	now := time.Now().UTC()
	for _, w := range defaultWorkspaces {
		w.CreatedAt = now
		w.UpdatedAt = now
		if err := s.CreateWorkspace(ctx, &w); err != nil {
			return err
		}
		slog.Info("seeded workspace", "id", w.ID, "name", w.Name)
	}
	return nil
}

// ensureGlobalWorkspace creates the global workspace if missing.
func ensureGlobalWorkspace(ctx context.Context, s store.Store, existing []store.Workspace) error {
	for _, w := range existing {
		if w.ID == "global" {
			return nil
		}
	}

	now := time.Now().UTC()
	w := store.Workspace{
		ID:            "global",
		Name:          "Global",
		RootPath:      "/",
		DefaultPolicy: "deny",
		Source:        "default",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.CreateWorkspace(ctx, &w); err != nil {
		return err
	}
	slog.Info("migrated: seeded global workspace")
	return nil
}
