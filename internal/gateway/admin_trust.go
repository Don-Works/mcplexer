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
	// AdminTrustDataDir — qualified via the real admin context: a CWD or
	// workspace root physically inside the data directory, or the gate
	// being disabled (no data dir configured). Full admin authority,
	// including cross-workspace credential references. This is the
	// operator-at-the-data-dir context — owning that path already owns
	// the host, so there is no workspace-segregation expectation to
	// protect.
	AdminTrustDataDir AdminTrust = "datadir"
	// AdminTrustSourceRepo — qualified via a dev-mode escape: a CWD or
	// workspace root inside a mcplexer source tree, OR the
	// MCPLEXER_ADMIN_ALLOW_ANY_CWD env break-glass. Admin tools are
	// visible and callable, but cross-workspace credential references in
	// route mutations are refused (internal/control route guard). Both
	// escapes are dev conveniences for exercising admin tools while
	// working on the gateway — neither should double as a
	// workspace-segregation bypass (2026-07-06 incident).
	AdminTrustSourceRepo AdminTrust = "source-repo"
)

// AdminTrustLevel reports how (cwd, workspaceRoots) qualifies for the
// admin surface, returning the strongest matching level instead of the
// boolean IsAdminContext collapses to. A genuine data-dir context wins;
// the source-repo tree and the env break-glass both resolve to the
// weaker source-repo level so the route guard still polices them.
//
// Agreement invariant with IsAdminContext: this returns AdminTrustNone
// iff IsAdminContext would return false. The env break-glass makes
// IsAdminContext true from any dir, so it must yield a non-None level
// here too — but source-repo, not data-dir, so the segregation guard
// keeps biting.
func (g *AdminCWDGate) AdminTrustLevel(cwd string, workspaceRoots []string) AdminTrust {
	if !g.Enabled() {
		return AdminTrustDataDir
	}
	// A genuine data-dir CWD is the operator context — full authority.
	if cwd != "" {
		cleaned := filepath.Clean(cwd)
		if cleaned == g.dataDir || strings.HasPrefix(cleaned, g.dataDir+string(filepath.Separator)) {
			return AdminTrustDataDir
		}
	}
	sourceRepo := cwd != "" && isMcplexerSourceCWD(filepath.Clean(cwd))
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
	// The env break-glass lifts the CWD gate from every directory. It
	// still grants admin (that is its purpose), but only at source-repo
	// trust: a dev exercising admin tools from an arbitrary dir must not
	// thereby be able to borrow another workspace's credentialed servers.
	if adminAnyCWDBypass() {
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
