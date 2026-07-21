// Package audit — attribution.go threads the identity of the *caller* that
// triggered a logical operation through context.Context, so deep emitters
// can attribute their own audit rows to the originating agent rather than
// synthesizing a detached placeholder.
//
// Motivation: the secrets resolver (internal/secrets) emits a secret.read /
// secret.list row every time a `secret://` ref is substituted or a scope is
// enumerated. Those emissions happen several layers below the gateway
// handler that knows which session + workspace the request belongs to.
// Without a ctx-borne carrier the resolver had no choice but to attribute
// the row to the auth *scope* (SessionID="scope:<id>", empty workspace),
// which read inconsistently against every other tool row in the audit table
// (those carry the session's real workspace + session id).
//
// This mirrors the correlation_id plumbing in correlation.go but carries a
// richer payload. A zero Attribution (no MCP caller — dashboard / API / CLI
// initiated work) is valid and signals "attribute to your own default";
// emitters fall back to their scope-attributed placeholder in that case.
package audit

import "context"

// Attribution is a snapshot of the caller's identity at the gateway entry,
// propagated to deep emitters via ctx. All fields are best-effort; an empty
// field means "the caller didn't have one" and the emitter should leave the
// corresponding audit column blank (the honest representation) rather than
// invent a value.
type Attribution struct {
	SessionID     string
	ClientType    string
	Model         string
	WorkspaceID   string
	WorkspaceName string
	Subpath       string
	ActorKind     string
	ActorID       string
}

// attributionKey is the unexported context key carrier. A struct type keeps
// the value private to this package — external code goes through
// WithAttribution / AttributionFromCtx.
type attributionKey struct{}

// WithAttribution returns a new ctx carrying attr. A zero-identity attr (no
// SessionID and no WorkspaceID) returns ctx unchanged so an accidental empty
// stamp can't shadow a real upstream attribution — callers can unconditionally
// stamp whatever the session yields and rely on this guard.
func WithAttribution(ctx context.Context, attr Attribution) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if attr.SessionID == "" && attr.WorkspaceID == "" {
		return ctx
	}
	return context.WithValue(ctx, attributionKey{}, attr)
}

// AttributionFromCtx returns the caller attribution stored in ctx and whether
// one was present. Safe to call with a nil ctx (returns the zero value, false).
func AttributionFromCtx(ctx context.Context) (Attribution, bool) {
	if ctx == nil {
		return Attribution{}, false
	}
	a, ok := ctx.Value(attributionKey{}).(Attribution)
	return a, ok
}
