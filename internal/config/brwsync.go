package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// DefaultBrwdPath is the live brwd binary location used when SyncOptions
// leaves BrwdPath empty. It mirrors the reference brw_chromium server.
// Resolved under the current user's home dir, not a hardcoded username.
var DefaultBrwdPath = homeRelPath("Library", "Application Support", "brw", "bin", "brwd")

// DefaultBrwPolicyPath is the browser-profiles policy passed via
// --profile-policy when SyncOptions leaves PolicyPath empty.
var DefaultBrwPolicyPath = homeRelPath(".config", "brw", "browser-profiles.json")

// homeRelPath joins path segments under the current user's home directory.
// Falls back to a relative path when the home dir can't be resolved.
func homeRelPath(seg ...string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(seg...)
	}
	return filepath.Join(append([]string{home}, seg...)...)
}

// brwSource tags every downstream server + route this sync owns. Only rows
// with this source are ever updated or pruned by the sync.
const brwSource = "brw"

// Sync action verbs surfaced in a SyncPlan.
const (
	ActionCreated   = "created"
	ActionUpdated   = "updated"
	ActionAdopted   = "adopted"
	ActionUnchanged = "unchanged"
	ActionSkipped   = "skipped"
	ActionPruned    = "pruned"
)

// BrwIdentity mirrors the "identity" object of a `brwctl daemons` entry.
type BrwIdentity struct {
	Workspace        string `json:"workspace"`
	Profile          string `json:"profile"`
	UserDataDir      string `json:"user_data_dir"`
	ProfileDirectory string `json:"profile_directory"`
	Mode             string `json:"mode"`
}

// BrwDaemon mirrors one entry of the `brwctl daemons` JSON array — a
// configured browser-profile daemon exposing an HTTP bridge that mcplexer
// proxies to via a stdio `brwd --mcp` child.
type BrwDaemon struct {
	Name        string      `json:"name"`
	Kind        string      `json:"kind"`
	Workspace   string      `json:"workspace"`
	Profile     string      `json:"profile"`
	HTTPAddr    string      `json:"http_addr"`
	WSAddr      string      `json:"ws_addr"`
	ExtensionID string      `json:"extension_id"`
	Reachable   bool        `json:"reachable"`
	Identity    BrwIdentity `json:"identity"`
}

// SyncOptions controls SyncBrwProfiles.
type SyncOptions struct {
	// DryRun computes the plan without writing anything. The CLI defaults
	// to this until --apply is passed.
	DryRun bool
	// Workspaces are the target workspace IDs that should get a brw route
	// per daemon. Empty = sync servers only, no routes.
	Workspaces []string
	// BrwdPath overrides the brwd binary path (default DefaultBrwdPath).
	BrwdPath string
	// PolicyPath overrides --profile-policy (default DefaultBrwPolicyPath).
	PolicyPath string
	// Prune deletes source="brw" servers/routes absent from the input.
	Prune bool
}

// SyncAction is one planned (or applied) change.
type SyncAction struct {
	Action    string `json:"action"` // created|updated|adopted|unchanged|skipped|pruned
	Kind      string `json:"kind"`   // server|route
	ID        string `json:"id"`
	Namespace string `json:"namespace,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// SyncPlan is the full ordered list of actions a sync computed/applied.
type SyncPlan struct {
	DryRun  bool         `json:"dry_run"`
	Actions []SyncAction `json:"actions"`
}

// sanitizeBrwNamespace lowercases and replaces every non-alphanumeric rune
// with "_", so "brw-chromium" -> "brw_chromium".
func sanitizeBrwNamespace(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// SyncBrwProfiles reconciles the gateway's downstream servers + routes with
// the set of brw browser-profile daemons reported by `brwctl daemons`.
//
// Each daemon becomes one stdio downstream server (a `brwd --mcp` child that
// proxies to the daemon's HTTP bridge via --upstream-http) plus, for each
// target workspace, one allow route. Operations are keyed by a deterministic
// namespace derived from the daemon's workspace, so re-running is idempotent.
//
// Adoption rules: an existing server holding the same namespace is updated in
// place when it is already source="brw" (preserving CreatedAt + the
// capabilities cache); a pre-existing server with any other source is left
// completely untouched and merely recorded as "adopted" — the sync never
// overwrites a user's manually registered server.
//
// All writes go through svc (config.Service) so validateTransport,
// checkNamespaceUnique, and validateRouteRefs run. When opts.DryRun is set the
// plan is computed without any writes.
func SyncBrwProfiles(ctx context.Context, svc *Service, st store.Store, daemons []BrwDaemon, opts SyncOptions) (SyncPlan, error) {
	plan := SyncPlan{DryRun: opts.DryRun}

	brwdPath := strings.TrimSpace(opts.BrwdPath)
	if brwdPath == "" {
		brwdPath = DefaultBrwdPath
	}
	policyPath := strings.TrimSpace(opts.PolicyPath)
	if policyPath == "" {
		policyPath = DefaultBrwPolicyPath
	}

	// Tracks every brw-owned row that should survive a prune.
	managedServerIDs := map[string]bool{}
	desiredRouteIDs := map[string]bool{}

	for _, d := range daemons {
		ws := strings.TrimSpace(d.Identity.Workspace)
		if ws == "" {
			ws = strings.TrimSpace(d.Workspace)
		}
		namespace := sanitizeBrwNamespace(ws)
		if namespace == "" {
			plan.Actions = append(plan.Actions, SyncAction{
				Action: ActionSkipped, Kind: "server",
				Detail: fmt.Sprintf("daemon %q has empty workspace; cannot derive namespace", d.Name),
			})
			continue
		}
		serverID := "brw-" + namespace

		args := []string{
			"--workspace", ws,
			"--profile-policy", policyPath,
			"--upstream-http", d.HTTPAddr,
			"--mcp",
			"--http", "off",
			"--mcp-tools", "all",
		}
		argsJSON, err := json.Marshal(args)
		if err != nil {
			return plan, fmt.Errorf("marshal args for %s: %w", serverID, err)
		}

		// Resolve the server that currently holds this namespace (preferred)
		// or the deterministic ID (fallback). Re-listed each iteration so an
		// apply-mode create earlier in the loop is seen by a later daemon
		// that maps to the same namespace.
		servers, err := st.ListDownstreamServers(ctx)
		if err != nil {
			return plan, fmt.Errorf("list downstream servers: %w", err)
		}
		var existing *store.DownstreamServer
		for i := range servers {
			if servers[i].ToolNamespace == namespace {
				existing = &servers[i]
				break
			}
		}
		if existing == nil {
			if got, gerr := st.GetDownstreamServer(ctx, serverID); gerr == nil {
				existing = got
			}
		}

		// effectiveServerID is what routes should point at — the deterministic
		// ID for a created/updated brw server, or the adopted server's own ID.
		effectiveServerID := serverID

		switch {
		case existing == nil:
			ds := &store.DownstreamServer{
				ID: serverID, Name: serverID, Transport: "stdio",
				Command: brwdPath, Args: argsJSON, ToolNamespace: namespace,
				MaxInstances: 1, IdleTimeoutSec: 300, Disabled: false, Source: brwSource,
			}
			if !opts.DryRun {
				if err := svc.CreateDownstreamServer(ctx, ds); err != nil {
					return plan, fmt.Errorf("create downstream %s: %w", serverID, err)
				}
			}
			managedServerIDs[serverID] = true
			plan.Actions = append(plan.Actions, SyncAction{
				Action: ActionCreated, Kind: "server", ID: serverID, Namespace: namespace,
				Detail: fmt.Sprintf("stdio %s --upstream-http %s", brwdPath, d.HTTPAddr),
			})

		case existing.Source != brwSource:
			// Pre-existing manual/api server holds this namespace: never
			// overwrite. Routes target the adopted server directly.
			effectiveServerID = existing.ID
			plan.Actions = append(plan.Actions, SyncAction{
				Action: ActionAdopted, Kind: "server", ID: existing.ID, Namespace: namespace,
				Detail: fmt.Sprintf("adopted, unchanged (source=%q preserved)", existing.Source),
			})

		default:
			// Existing brw server: update in place, preserving CreatedAt and
			// the capabilities cache; only refresh the mutable wiring fields.
			effectiveServerID = existing.ID
			ds := &store.DownstreamServer{
				ID: existing.ID, Name: existing.Name, Transport: "stdio",
				Command: brwdPath, Args: argsJSON, ToolNamespace: namespace,
				Discovery:         existing.Discovery,
				CapabilitiesCache: existing.CapabilitiesCache,
				CacheConfig:       existing.CacheConfig,
				IdleTimeoutSec:    300,
				CallTimeoutSec:    existing.CallTimeoutSec,
				MaxInstances:      1,
				RestartPolicy:     existing.RestartPolicy,
				Disabled:          false,
				Source:            brwSource,
				CreatedAt:         existing.CreatedAt,
			}
			managedServerIDs[existing.ID] = true
			if brwServerEqual(existing, ds) {
				plan.Actions = append(plan.Actions, SyncAction{
					Action: ActionUnchanged, Kind: "server", ID: existing.ID, Namespace: namespace,
				})
			} else {
				if !opts.DryRun {
					if err := svc.UpdateDownstreamServer(ctx, ds); err != nil {
						return plan, fmt.Errorf("update downstream %s: %w", existing.ID, err)
					}
				}
				plan.Actions = append(plan.Actions, SyncAction{
					Action: ActionUpdated, Kind: "server", ID: existing.ID, Namespace: namespace,
					Detail: fmt.Sprintf("stdio %s --upstream-http %s", brwdPath, d.HTTPAddr),
				})
			}
		}

		// One allow route per target workspace.
		for _, wsID := range opts.Workspaces {
			wsID = strings.TrimSpace(wsID)
			if wsID == "" {
				continue
			}
			routeID := "brw-route-" + wsID + "-" + namespace
			if _, err := st.GetWorkspace(ctx, wsID); err != nil {
				plan.Actions = append(plan.Actions, SyncAction{
					Action: ActionSkipped, Kind: "route", ID: routeID, Namespace: namespace,
					Detail: fmt.Sprintf("workspace %q does not exist", wsID),
				})
				continue
			}
			desiredRoute := store.RouteRule{
				ID: routeID, Name: fmt.Sprintf("brw %s → %s", namespace, wsID),
				WorkspaceID: wsID, DownstreamServerID: effectiveServerID,
				ToolMatch: json.RawMessage(`["` + namespace + `__*"]`),
				PathGlob:  "**", Priority: 50, Policy: "allow", Source: brwSource,
			}
			desiredRouteIDs[routeID] = true

			existingRoute, rerr := st.GetRouteRule(ctx, routeID)
			if rerr != nil {
				if !opts.DryRun {
					if err := svc.CreateRouteRule(ctx, &desiredRoute); err != nil {
						return plan, fmt.Errorf("create route %s: %w", routeID, err)
					}
				}
				plan.Actions = append(plan.Actions, SyncAction{
					Action: ActionCreated, Kind: "route", ID: routeID, Namespace: namespace,
					Detail: fmt.Sprintf("workspace=%s → %s", wsID, effectiveServerID),
				})
				continue
			}
			if brwRouteEqual(existingRoute, &desiredRoute) {
				plan.Actions = append(plan.Actions, SyncAction{
					Action: ActionUnchanged, Kind: "route", ID: routeID, Namespace: namespace,
				})
				continue
			}
			desiredRoute.CreatedAt = existingRoute.CreatedAt
			if !opts.DryRun {
				if err := svc.UpdateRouteRule(ctx, &desiredRoute); err != nil {
					return plan, fmt.Errorf("update route %s: %w", routeID, err)
				}
			}
			plan.Actions = append(plan.Actions, SyncAction{
				Action: ActionUpdated, Kind: "route", ID: routeID, Namespace: namespace,
				Detail: fmt.Sprintf("workspace=%s → %s", wsID, effectiveServerID),
			})
		}
	}

	if opts.Prune {
		if err := pruneBrw(ctx, st, managedServerIDs, desiredRouteIDs, opts.DryRun, &plan); err != nil {
			return plan, err
		}
	}

	return plan, nil
}

// pruneBrw deletes source="brw" routes then servers that are absent from the
// reconciled input. Routes are removed first because route_rules carries a
// NOT NULL FK to downstream_servers(id).
func pruneBrw(ctx context.Context, st store.Store, managedServerIDs, desiredRouteIDs map[string]bool, dryRun bool, plan *SyncPlan) error {
	routes, err := st.ListRouteRules(ctx, "")
	if err != nil {
		return fmt.Errorf("list routes for prune: %w", err)
	}
	for _, r := range routes {
		if r.Source != brwSource || desiredRouteIDs[r.ID] {
			continue
		}
		if !dryRun {
			if err := st.DeleteRouteRule(ctx, r.ID); err != nil {
				return fmt.Errorf("prune route %s: %w", r.ID, err)
			}
		}
		plan.Actions = append(plan.Actions, SyncAction{
			Action: ActionPruned, Kind: "route", ID: r.ID,
			Detail: "absent from discovery input",
		})
	}

	servers, err := st.ListDownstreamServers(ctx)
	if err != nil {
		return fmt.Errorf("list servers for prune: %w", err)
	}
	for _, s := range servers {
		if s.Source != brwSource || managedServerIDs[s.ID] {
			continue
		}
		if !dryRun {
			if err := st.DeleteDownstreamServer(ctx, s.ID); err != nil {
				return fmt.Errorf("prune server %s: %w", s.ID, err)
			}
		}
		plan.Actions = append(plan.Actions, SyncAction{
			Action: ActionPruned, Kind: "server", ID: s.ID, Namespace: s.ToolNamespace,
			Detail: "absent from discovery input",
		})
	}
	return nil
}

// brwServerEqual reports whether the desired brw server matches the existing
// row on every field this sync manages.
func brwServerEqual(a, b *store.DownstreamServer) bool {
	return a.Command == b.Command &&
		string(a.Args) == string(b.Args) &&
		a.ToolNamespace == b.ToolNamespace &&
		a.Transport == b.Transport &&
		a.MaxInstances == b.MaxInstances &&
		a.IdleTimeoutSec == b.IdleTimeoutSec &&
		a.Disabled == b.Disabled &&
		a.Source == b.Source &&
		ptrStrEqual(a.URL, b.URL)
}

// brwRouteEqual reports whether the desired brw route matches the existing row
// on every field this sync manages.
func brwRouteEqual(a, b *store.RouteRule) bool {
	return a.WorkspaceID == b.WorkspaceID &&
		a.DownstreamServerID == b.DownstreamServerID &&
		string(a.ToolMatch) == string(b.ToolMatch) &&
		a.PathGlob == b.PathGlob &&
		a.Priority == b.Priority &&
		a.Policy == b.Policy &&
		a.Source == b.Source
}

func ptrStrEqual(a, b *string) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}
