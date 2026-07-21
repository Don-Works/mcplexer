package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/don-works/mcplexer/internal/cache"
	"github.com/don-works/mcplexer/internal/store"
)

// WorkspaceAncestor pairs a workspace ID with its root path for subpath
// computation during routing.
type WorkspaceAncestor struct {
	ID       string
	Name     string
	RootPath string
}

// RouteContext is the input to the routing engine.
type RouteContext struct {
	WorkspaceID string
	Subpath     string
	ToolName    string
}

// RouteResult is the output of a successful route match.
type RouteResult struct {
	DownstreamServerID string
	AuthScopeID        string
	MatchedRuleID      string
	OriginalToolName   string
	ScopePolicy        json.RawMessage
	ApprovalMode       string
	ApprovalTimeout    int

	// Set by RouteWithFallback to record which workspace and subpath matched.
	MatchedWorkspaceID   string
	MatchedWorkspaceName string
	Subpath              string
}

var (
	// ErrNoRoute means no matching route rule was found.
	ErrNoRoute = errors.New("no matching route")

	// ErrDenied means a deny rule matched. Use errors.Is(err, ErrDenied)
	// to check; DeniedError wraps this sentinel to carry rule details.
	ErrDenied = errors.New("route denied by policy")
)

// DeniedError wraps ErrDenied with the ID of the rule that denied the request.
type DeniedError struct {
	RuleID string
}

func (e *DeniedError) Error() string {
	return "route denied by policy: rule " + e.RuleID
}

func (e *DeniedError) Unwrap() error {
	return ErrDenied
}

// Engine resolves tool calls to downstream servers via route rules.
type Engine struct {
	store      store.Store
	rulesCache *cache.Cache[string, []parsedRule]
	wsVersion  atomic.Int64 // bumped when workspaces change
}

// NewEngine creates a new routing engine.
func NewEngine(s store.Store) *Engine {
	return &Engine{
		store:      s,
		rulesCache: cache.New[string, []parsedRule](100, 30*time.Second),
	}
}

// Route finds the best matching route for the given context.
func (e *Engine) Route(ctx context.Context, rc RouteContext) (*RouteResult, error) {
	parsed, err := e.rulesCache.GetOrLoad(rc.WorkspaceID, func() ([]parsedRule, error) {
		rules, err := e.store.ListRouteRules(ctx, rc.WorkspaceID)
		if err != nil {
			return nil, err
		}
		p := parseRules(rules)
		if err := e.resolveNamespaces(ctx, p); err != nil {
			// Fail closed: a transient store failure must NOT be cached, and
			// must NOT leave a wildcard rule with an empty namespace (which
			// would bypass the namespace guard in matchRoute and misroute
			// foreign-namespace tools). Returning the error here means the
			// GetOrLoad does not cache the partial result and the route
			// attempt fails rather than silently widening a wildcard rule.
			return nil, err
		}
		sortRules(p)
		return p, nil
	})
	if err != nil {
		return nil, err
	}

	return matchRoute(parsed, rc)
}

// InvalidateWorkspace removes cached rules for a specific workspace.
func (e *Engine) InvalidateWorkspace(workspaceID string) {
	e.rulesCache.Invalidate(workspaceID)
}

// InvalidateAllRoutes removes all cached route rules and bumps the workspace
// version so that active sessions lazily re-resolve their workspace chain.
func (e *Engine) InvalidateAllRoutes() {
	e.rulesCache.Flush()
	e.wsVersion.Add(1)
}

// WorkspaceVersion returns the current workspace version counter. Sessions
// compare this against their last-seen value to detect workspace changes.
func (e *Engine) WorkspaceVersion() int64 {
	return e.wsVersion.Load()
}

// RouteStats returns cache statistics for route resolution.
func (e *Engine) RouteStats() cache.Stats {
	return e.rulesCache.Stats()
}

// resolveNamespaces looks up the tool_namespace for each rule's downstream
// server so that matchRoute can enforce namespace-aware matching.
//
// It distinguishes two empty-namespace cases:
//   - "server has no namespace" (store.ErrNotFound, or a nil server, or a
//     server with an empty ToolNamespace) — legitimate; the rule keeps its
//     empty namespace and the guard is intentionally not applied.
//   - "lookup failed" (any other error, e.g. a transient store failure) — a
//     real error is returned so the caller fails closed and does NOT cache a
//     partial resolution. Caching a partial resolution would leave a wildcard
//     rule's namespace empty, bypassing the namespace guard in matchRoute and
//     misrouting foreign-namespace tools to the wrong server for the cache TTL.
func (e *Engine) resolveNamespaces(ctx context.Context, rules []parsedRule) error {
	serverIDs := make(map[string]struct{})
	for _, r := range rules {
		if r.DownstreamServerID != "" {
			serverIDs[r.DownstreamServerID] = struct{}{}
		}
	}

	nsMap := make(map[string]string, len(serverIDs))
	for id := range serverIDs {
		srv, err := e.store.GetDownstreamServer(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			// Server deleted / unknown: legitimately no namespace. Continue.
			continue
		}
		if err != nil {
			// Transient store failure: fail closed rather than silently
			// widening a wildcard rule. Log so the cause is not dropped.
			slog.Error("routing: failed to resolve downstream namespace, failing route closed",
				"downstream_server_id", id, "error", err)
			return fmt.Errorf("resolve namespace for downstream %s: %w", id, err)
		}
		if srv == nil {
			// No row but no error: treat as no-namespace (legit).
			continue
		}
		if srv.ToolNamespace != "" {
			nsMap[id] = srv.ToolNamespace
		}
	}

	for i := range rules {
		if ns, ok := nsMap[rules[i].DownstreamServerID]; ok {
			rules[i].namespace = ns
		}
	}
	return nil
}

// RouteWithFallback tries routing through a chain of workspace ancestors (most
// specific first), computing the subpath for each workspace from the client's
// root directory. A deny at any level stops the search. ErrNoRoute continues
// to the next ancestor. Returns the first successful match or ErrNoRoute.
func (e *Engine) RouteWithFallback(ctx context.Context, rc RouteContext, clientRoot string, ancestors []WorkspaceAncestor) (*RouteResult, error) {
	if len(ancestors) == 0 {
		return e.Route(ctx, rc)
	}

	for _, ws := range ancestors {
		rc.WorkspaceID = ws.ID
		rc.Subpath = ComputeSubpath(clientRoot, ws.RootPath)
		result, err := e.Route(ctx, rc)
		if err == nil {
			result.MatchedWorkspaceID = ws.ID
			result.MatchedWorkspaceName = ws.Name
			result.Subpath = rc.Subpath
			return result, nil
		}
		if errors.Is(err, ErrDenied) {
			return nil, err
		}
		// ErrNoRoute: continue to next ancestor.
	}
	return nil, ErrNoRoute
}

// ComputeSubpath returns the relative path of clientRoot within wsRoot.
// If the client is at the workspace root, returns "" (matches "**").
// If clientRoot is not under wsRoot, returns "".
// ComputeSubpath returns the relative path of clientRoot within wsRoot.
// If the client is at the workspace root, returns "" (matches "**").
// If clientRoot is not under wsRoot, returns "".
func ComputeSubpath(clientRoot, wsRoot string) string {
	if clientRoot == "" || wsRoot == "" {
		return ""
	}

	// Normalize by trimming trailing slashes.
	clientRoot = strings.TrimSuffix(clientRoot, "/")
	wsRoot = strings.TrimSuffix(wsRoot, "/")

	if clientRoot == wsRoot {
		return ""
	}
	// Handle root workspace "/" specially — no trailing separator needed.
	if wsRoot == "" { // Was originally "/" but after TrimSuffix it's ""
		return strings.TrimPrefix(clientRoot, "/")
	}

	if sub, ok := strings.CutPrefix(clientRoot, wsRoot+"/"); ok {
		return sub
	}
	return ""
}

// matchRoute evaluates sorted rules against the route context.
// The first rule to match (by priority and specificity) wins.
func matchRoute(rules []parsedRule, rc RouteContext) (*RouteResult, error) {
	for i := range rules {
		r := &rules[i]

		if !GlobMatch(r.PathGlob, rc.Subpath) {
			continue
		}
		if !matchTool(rc.ToolName, r.toolPatterns) {
			continue
		}
		// Namespace guard: if the rule's downstream has a tool namespace,
		// the tool must belong to that namespace. Prevents a wildcard rule
		// pointing to one server from catching tools for another.
		if r.namespace != "" && !strings.HasPrefix(rc.ToolName, r.namespace+"__") {
			continue
		}

		if r.Policy == "deny" {
			return nil, &DeniedError{RuleID: r.ID}
		}

		return &RouteResult{
			DownstreamServerID: r.DownstreamServerID,
			AuthScopeID:        r.AuthScopeID,
			MatchedRuleID:      r.ID,
			OriginalToolName:   rc.ToolName,
			ScopePolicy:        r.ScopePolicy,
			ApprovalMode:       r.ApprovalMode,
			ApprovalTimeout:    r.ApprovalTimeout,
		}, nil
	}

	return nil, ErrNoRoute
}
