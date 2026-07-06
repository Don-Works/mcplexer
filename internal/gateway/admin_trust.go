package gateway

import (
	"context"
	"path/filepath"
	"strings"
)

// AdminTrust classifies HOW a session qualified for the admin surface.
// The CWD gate (admin_gate.go) answers "may this session see/call admin
// tools?" as a boolean; AdminTrust preserves WHICH signal granted it so
// downstream policy can hold the weaker dev-mode escape to stricter
// rules than the real data-directory context.
//
// Motivation (2026-07-06 incident): an agent whose workspace routing
// deliberately excluded client credential servers qualified for admin
// via the source-repo dev escape and used create_route to mint routes
// referencing another workspace's downstream server + auth scope,
// making that workspace's credentialed data reachable with zero human
// approval. The dev escape exists for gateway development ergonomics —
// it must not double as a workspace-segregation bypass.
type AdminTrust string

const (
	// AdminTrustNone — the session does not qualify for admin at all.
	AdminTrustNone AdminTrust = ""
	// AdminTrustDataDir — qualified via the real admin context: CWD (or
	// an admin-trusted workspace tag) inside the data directory, the
	// gate being disabled, or the explicit env break-glass. Full admin
	// authority.
	AdminTrustDataDir AdminTrust = "datadir"
	// AdminTrustSourceRepo — qualified ONLY via the dev-mode escape (a
	// CWD or workspace root inside a mcplexer source tree). Admin tools
	// are visible, but cross-workspace credential references in route
	// mutations are refused (internal/control route guard).
	AdminTrustSourceRepo AdminTrust = "source-repo"
)

// AdminTrustLevel reports how (cwd, workspaceRoots) qualifies for the
// admin surface. The decision mirrors IsAdminContext exactly — same
// signals, same order — but returns the strongest matching level
// instead of collapsing to a boolean. Data-dir qualification wins over
// the source-repo escape when both hold.
func (g *AdminCWDGate) AdminTrustLevel(cwd string, workspaceRoots []string) AdminTrust {
	if !g.Enabled() || adminAnyCWDBypass() {
		return AdminTrustDataDir
	}
	sourceRepo := false
	if cwd != "" {
		cleaned := filepath.Clean(cwd)
		if cleaned == g.dataDir || strings.HasPrefix(cleaned, g.dataDir+string(filepath.Separator)) {
			return AdminTrustDataDir
		}
		if isMcplexerSourceCWD(cleaned) {
			sourceRepo = true
		}
	}
	for _, root := range workspaceRoots {
		if root == "" {
			continue
		}
		if isMcplexerSourceCWD(filepath.Clean(root)) {
			sourceRepo = true
		}
	}
	if sourceRepo {
		return AdminTrustSourceRepo
	}
	return AdminTrustNone
}

// adminTrustLevel resolves the current session's admin qualification at
// dispatch time. In-process worker calls (operator-configured, already
// allowlisted) and admin-trusted workspace chains carry data-dir
// authority; everything else asks the CWD gate.
func (h *handler) adminTrustLevel(ctx context.Context) AdminTrust {
	if IsInProcessWorkerCall(ctx) || h.sessions.isAdminTrusted() {
		return AdminTrustDataDir
	}
	return h.adminGate.AdminTrustLevel(h.sessions.clientRoot(), h.sessions.workspaceRoots())
}

// adminTrustKey carries the AdminTrust classification on the tool-call
// context from the gateway dispatch layer into the control backend.
type adminTrustKey struct{}

// WithAdminTrust stamps the session's admin qualification onto ctx.
// Attached in handleToolsCall for admin tool calls only.
func WithAdminTrust(ctx context.Context, t AdminTrust) context.Context {
	return context.WithValue(ctx, adminTrustKey{}, t)
}

// AdminTrustFromContext returns the AdminTrust stamped by
// WithAdminTrust, or AdminTrustNone when the ctx never crossed the
// gateway's admin dispatch (REST API, standalone control server,
// direct handler tests). Callers MUST treat the zero value as "not a
// dev-escape call" — the restriction targets AdminTrustSourceRepo
// specifically, so unstamped trusted paths keep full authority.
func AdminTrustFromContext(ctx context.Context) AdminTrust {
	v, _ := ctx.Value(adminTrustKey{}).(AdminTrust)
	return v
}
