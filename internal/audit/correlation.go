// Package audit — correlation.go threads a single correlation_id through
// context.Context so every slog line and audit row produced by one
// logical operation joins on a shared key.
//
// Why it matters: worker runs, MCP gateway calls, HTTP API requests and
// scheduler ticks all fan out into mesh/secrets/dispatch/output layers.
// Without a ctx-borne id, those deeper layers have no way to know they
// belong to the parent operation — incident reconstruction can't trace
// a single user action across the slog stream and the audit ledger.
//
// Two-pronged plumbing:
//
//  1. WithCorrelation / FromCtx — the canonical pair every caller uses.
//     Idempotent and nil-safe.
//  2. ContextHandler — a slog.Handler wrapper that auto-stamps
//     correlation_id on every record whose ctx carries one. Installed
//     once at daemon boot so deep call sites stay clean (they just call
//     slog.Default().InfoContext / .ErrorContext and the attr appears).
//
// Audit emitters read FromCtx in their own helpers and populate
// AuditRecord.CorrelationID — see internal/audit/logger.go and the
// per-package emitAudit helpers in workers/runner, workers/admin and
// secrets.
package audit

import (
	"context"
	"log/slog"
)

// correlationKey is the unexported context key carrier. Using a struct
// type (rather than a string) keeps the value private to this package
// — external code MUST go through WithCorrelation / FromCtx.
type correlationKey struct{}

// correlationAttrKey is the slog attribute name. Single source of truth
// so the handler wrapper, the audit emitters and the dashboards all
// agree on the wire format.
const correlationAttrKey = "correlation_id"

// WithCorrelation returns a new ctx carrying id. Idempotent — if ctx
// already has a correlation_id the new value replaces it. Callers that
// want to NEST (parent op spawns a child op with its own narrower id)
// should read the existing value with FromCtx first if they want to
// preserve it. Empty id returns ctx unchanged so accidental "" calls
// don't clobber a real upstream id.
func WithCorrelation(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, correlationKey{}, id)
}

// FromCtx returns the correlation_id stored in ctx, or "" if none. Safe
// to call with a nil ctx (returns "").
func FromCtx(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(correlationKey{}).(string)
	return id
}

// SlogAttrs returns a single-element attr slice carrying the
// correlation_id from ctx, or an empty slice when ctx has no id. Lets
// callers that already build their own attr list splice the id in:
//
//	slog.LogAttrs(ctx, slog.LevelInfo, "doing work",
//	    append(audit.SlogAttrs(ctx), slog.Int("count", n))...)
//
// Most call sites should prefer the ContextHandler-installed auto-stamp
// path; this helper is for places that want to be explicit (or that
// build a record off the default handler without using *Context methods).
func SlogAttrs(ctx context.Context) []slog.Attr {
	id := FromCtx(ctx)
	if id == "" {
		return nil
	}
	return []slog.Attr{slog.String(correlationAttrKey, id)}
}

// SlogLogger returns a slog.Logger pre-decorated with the
// correlation_id from ctx. When ctx has no id the default logger is
// returned so the caller can use the same code path regardless. Useful
// at deep call sites that want simple log lines without threading ctx
// through every slog.* invocation. Named SlogLogger (not Logger) to
// avoid shadowing the audit.Logger type defined in logger.go.
func SlogLogger(ctx context.Context) *slog.Logger {
	id := FromCtx(ctx)
	if id == "" {
		return slog.Default()
	}
	return slog.Default().With(correlationAttrKey, id)
}

// ContextHandler wraps a slog.Handler so every record produced through
// the *Context-flavoured log methods (InfoContext, ErrorContext, etc.)
// automatically carries correlation_id when ctx has one. Install once
// at daemon boot:
//
//	base := slog.NewJSONHandler(os.Stderr, opts)
//	slog.SetDefault(slog.New(audit.NewContextHandler(base)))
//
// The wrapper is a thin shim — every other Handler method (Enabled,
// WithAttrs, WithGroup) delegates straight through.
type ContextHandler struct {
	slog.Handler
}

// NewContextHandler wraps base so Handle adds correlation_id from ctx
// when one is present. A nil base returns nil so callers can chain
// without intermediate nil checks.
func NewContextHandler(base slog.Handler) *ContextHandler {
	if base == nil {
		return nil
	}
	return &ContextHandler{Handler: base}
}

// Handle appends the correlation_id from ctx (when set) and delegates
// to the wrapped handler.
func (h *ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if h == nil || h.Handler == nil {
		return nil
	}
	if id := FromCtx(ctx); id != "" {
		r.AddAttrs(slog.String(correlationAttrKey, id))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs delegates to the wrapped handler and re-wraps the result so
// the auto-stamp behaviour survives slog.With chains.
func (h *ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if h == nil || h.Handler == nil {
		return h
	}
	return &ContextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup delegates to the wrapped handler and re-wraps the result so
// the auto-stamp behaviour survives slog.WithGroup chains.
func (h *ContextHandler) WithGroup(name string) slog.Handler {
	if h == nil || h.Handler == nil {
		return h
	}
	return &ContextHandler{Handler: h.Handler.WithGroup(name)}
}
