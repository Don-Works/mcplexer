package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/don-works/mcplexer/internal/store"
	"gopkg.in/yaml.v3"
)

// SourceBrain marks config rows whose canonical truth is a brain config
// YAML file. ApplyBrain upserts these and prunes stale ones, mirroring the
// source=yaml discipline in Apply — never touching api/yaml/db/default rows.
const SourceBrain = "brain"

// brainRouteRule is the per-workspace routes.yaml entry shape. It is a
// deliberate subset of store.RouteRule (the human-editable fields); derived
// columns (created_at/updated_at/source) are owned by the apply path.
type brainRouteRule struct {
	ID                 string   `yaml:"id"`
	Name               string   `yaml:"name,omitempty"`
	Priority           int      `yaml:"priority"`
	PathGlob           string   `yaml:"path_glob,omitempty"`
	ToolMatch          []string `yaml:"tool_match,omitempty"`
	DownstreamServerID string   `yaml:"downstream_server_id"`
	AuthScopeID        string   `yaml:"auth_scope_id,omitempty"`
	Policy             string   `yaml:"policy,omitempty"`
	LogLevel           string   `yaml:"log_level,omitempty"`
}

// brainRoutesFile is the top-level shape of a workspaces/<ws>/config/
// routes.yaml document.
type brainRoutesFile struct {
	Routes []brainRouteRule `yaml:"routes"`
}

// ApplyBrain loads each workspace's config/routes.yaml from the brain repo,
// upserts the rules tagged source="brain", and prunes stale source="brain"
// rules that no longer appear in any file. It reuses the existing
// upsert+prune discipline (mirrors applyDownstreamServers) inside a single
// transaction so the route table never observes a partial apply. Rows with
// any other source (api/yaml/db/default) are never touched.
//
// A missing brain dir or a missing routes.yaml is not an error — the apply
// simply prunes any orphaned source="brain" rows and returns.
func ApplyBrain(ctx context.Context, s store.Store, brainDir string) error {
	wantByID, err := collectBrainRoutes(brainDir)
	if err != nil {
		return err
	}
	return s.Tx(ctx, func(tx store.Store) error {
		return applyBrainRoutes(ctx, tx, wantByID)
	})
}

// collectBrainRoutes walks every workspaces/<ws>/config/routes.yaml under
// brainDir and returns the parsed rules keyed by id (with the workspace
// slug filled in from the folder). Duplicate ids across files are an error
// (a route id is globally unique in the route table).
func collectBrainRoutes(brainDir string) (map[string]store.RouteRule, error) {
	out := make(map[string]store.RouteRule)
	root := filepath.Join(brainDir, "workspaces")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return nil, fmt.Errorf("brain config: read workspaces: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ws := e.Name()
		path := filepath.Join(root, ws, "config", "routes.yaml")
		data, rErr := os.ReadFile(path)
		if rErr != nil {
			if errors.Is(rErr, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("brain config: read %s: %w", path, rErr)
		}
		if err := parseBrainRoutes(ws, data, out); err != nil {
			return nil, fmt.Errorf("brain config: %s: %w", path, err)
		}
	}
	return out, nil
}

// parseBrainRoutes decodes one routes.yaml into the accumulator, stamping
// the workspace slug + source onto each rule.
func parseBrainRoutes(ws string, data []byte, out map[string]store.RouteRule) error {
	var f brainRoutesFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse yaml: %w", err)
	}
	for _, r := range f.Routes {
		if r.ID == "" {
			return errors.New("route rule missing id")
		}
		if _, dup := out[r.ID]; dup {
			return fmt.Errorf("duplicate route id %q", r.ID)
		}
		// Validate the human-editable fields here so a typo in a brain
		// routes.yaml (e.g. `policy: dney`) surfaces as an error rather
		// than silently producing an ALLOW rule downstream. The full
		// referential check (workspace/downstream/auth-scope existence)
		// runs in applyBrainRoutes via config.Service, sharing the API
		// path's validation surface.
		if err := validatePolicy(orDefault(r.Policy, "allow")); err != nil {
			return fmt.Errorf("route %s: %w", r.ID, err)
		}
		if err := validateGlob(orDefault(r.PathGlob, "**")); err != nil {
			return fmt.Errorf("route %s: %w", r.ID, err)
		}
		tm, err := json.Marshal(orDefaultToolMatch(r.ToolMatch))
		if err != nil {
			return fmt.Errorf("marshal tool_match for %s: %w", r.ID, err)
		}
		out[r.ID] = store.RouteRule{
			ID:                 r.ID,
			Name:               r.Name,
			Priority:           r.Priority,
			WorkspaceID:        ws,
			PathGlob:           orDefault(r.PathGlob, "**"),
			ToolMatch:          tm,
			DownstreamServerID: r.DownstreamServerID,
			AuthScopeID:        r.AuthScopeID,
			Policy:             orDefault(r.Policy, "allow"),
			LogLevel:           orDefault(r.LogLevel, "info"),
			Source:             SourceBrain,
		}
	}
	return nil
}

// applyBrainRoutes upserts the wanted rules and prunes stale source="brain"
// rows. Runs inside the caller's transaction. Upserts go through
// config.Service so the brain path shares the API path's validation surface:
// validateRouteRefs enforces workspace/downstream/auth-scope existence (the
// FKs that migration 004 dropped), glob/tool_match/scope-policy shape, and
// policy. A brain route referencing a non-existent workspace or server now
// fails the apply instead of inserting a dangling row.
func applyBrainRoutes(ctx context.Context, tx store.Store, want map[string]store.RouteRule) error {
	svc := NewService(tx)
	for id := range want {
		r := want[id]
		_, err := tx.GetRouteRule(ctx, id)
		if err != nil {
			if cErr := svc.CreateRouteRule(ctx, &r); cErr != nil {
				return fmt.Errorf("create brain route %s: %w", id, cErr)
			}
			continue
		}
		if uErr := svc.UpdateRouteRule(ctx, &r); uErr != nil {
			return fmt.Errorf("update brain route %s: %w", id, uErr)
		}
	}
	return pruneStaleBrainRoutes(ctx, tx, want)
}

// pruneStaleBrainRoutes deletes source="brain" route rows that no longer
// appear in any routes.yaml. Other sources are never touched.
func pruneStaleBrainRoutes(ctx context.Context, tx store.Store, want map[string]store.RouteRule) error {
	all, err := tx.ListRouteRules(ctx, "")
	if err != nil {
		return fmt.Errorf("list routes for prune: %w", err)
	}
	for _, r := range all {
		if r.Source != SourceBrain {
			continue
		}
		if _, keep := want[r.ID]; keep {
			continue
		}
		slog.Info("pruning stale brain route", "id", r.ID)
		if dErr := tx.DeleteRouteRule(ctx, r.ID); dErr != nil {
			return fmt.Errorf("delete stale brain route %s: %w", r.ID, dErr)
		}
	}
	return nil
}

// orDefault returns v when non-empty, else def.
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// orDefaultToolMatch returns the slice when non-empty, else the wildcard.
func orDefaultToolMatch(tm []string) []string {
	if len(tm) == 0 {
		return []string{"*"}
	}
	return tm
}
