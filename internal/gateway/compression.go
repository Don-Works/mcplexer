package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/compression"
	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
)

// applyCompression runs the pipeline over a result the model will read — a
// downstream tool result OR execute_code output — enforcing the
// verify-after-compress kill-switch, recording the measurement, and returning
// the (possibly compressed) result. In shadow it measures and returns the
// original; when off/disabled it is a no-op. Persistence is detached from the
// request ctx so it is best-effort and never slows/fails the call. Callers that
// must NOT compress (downstream results consumed inside the sandbox) gate the
// call site.
func (h *handler) applyCompression(ctx context.Context, result json.RawMessage) json.RawMessage {
	return h.applyCompressionForTool(ctx, "", result)
}

// applyCompressionForTool is applyCompression with the originating tool name,
// which the session-dedup step uses to label its pointer envelopes.
func (h *handler) applyCompressionForTool(ctx context.Context, toolName string, result json.RawMessage) json.RawMessage {
	if h == nil || h.compression == nil {
		return result
	}
	mode := h.compressionMode(ctx)
	disabled := h.compressionDisabled(ctx)
	original := result
	var obs []compression.Observation
	// Session-dedup runs before the pipeline: a byte-identical repeat of an
	// earlier delivery collapses to a pointer envelope, leaving the pipeline
	// (almost) nothing to chew on.
	if h.sessionDedup != nil && !disabled[sessionDedupName] {
		deduped, dobs := h.sessionDedup.Process(
			mode, models.EstimateContextTokens, h.currentSessionID(), toolName, result)
		result = deduped
		obs = append(obs, dobs...)
	}
	compressed, pobs := h.compression.Process(mode, disabled, result)
	obs = append(obs, pobs...)
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if string(compressed) != string(original) {
		// Persist stashed originals BEFORE the kill-switch check so markers
		// resolve; if any is unrecoverable, bypass to the original.
		h.persistCCR(pctx, obs)
		if h.ccrMarkersResolve(pctx, compressed) {
			result = compressed
		} else {
			for i := range obs {
				obs[i].Applied = false
				obs[i].Stash = nil
			}
			result = original
			slog.Warn("compression kill-switch: unresolvable CCR marker, returning original result")
		}
	} else {
		result = original
	}
	h.recordCompression(obs)
	h.persistCompression(pctx, obs)
	return result
}

// currentSessionID returns the MCP session id, or "" when unbound (tests,
// pre-initialize calls) — session-dedup skips silently in that case.
func (h *handler) currentSessionID() string {
	if h == nil || h.sessions == nil {
		return ""
	}
	return h.sessions.sessionID()
}

// compressionMinBytes is the smallest tool-result payload worth running the
// compression pipeline over. Below this the measurement cost outweighs any
// realistic saving.
const compressionMinBytes = 256

// newCompressionPipeline builds the gateway's token-compression pipeline with
// the default transform set, wired to the token estimator. The pipeline is
// stateless; the effective mode is resolved per-call from settings.
func newCompressionPipeline() *compression.Pipeline {
	p := compression.New(func(n int) int { return models.EstimateContextTokens(n) }, compressionMinBytes)
	p.Register(compression.DefaultTransforms()...)
	return p
}

// compressionMode resolves the effective compression mode from settings,
// defaulting to shadow (measure-only, dry-run) when settings are unavailable.
func (h *handler) compressionMode(ctx context.Context) compression.Mode {
	if h != nil && h.settingsSvc != nil {
		return compression.ParseMode(h.settingsSvc.Load(ctx).CompressionMode)
	}
	return compression.ModeShadow
}

// compressionDisabled returns the set of transform names disabled for this
// call: the operator's settings toggles, plus harness-conditional gates.
func (h *handler) compressionDisabled(ctx context.Context) map[string]bool {
	m := map[string]bool{}
	if h != nil && h.settingsSvc != nil {
		for _, n := range h.settingsSvc.Load(ctx).CompressionDisabledTransforms {
			m[n] = true
		}
	}
	// structured_dedup replaces content[].text with a marker and relies on the
	// client forwarding structuredContent to the model. Client behavior is
	// inconsistent (Claude Code forwards ONLY structuredContent and drops
	// text; claude.ai web and ChatGPT forward both; others may forward only
	// text — for those, dedup would leave the model a bare marker). Gate it to
	// clients known to surface structuredContent; unknown clients keep the
	// text copy.
	if h != nil && h.sessions != nil && !clientForwardsStructuredContent(h.sessions.clientType()) {
		m["structured_dedup"] = true
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// clientForwardsStructuredContent reports whether the connecting harness is
// known to put structuredContent into the model's context (see the client
// matrix in the 2026-07 compression audit: Claude Code CLI forwards only
// structuredContent; claude.ai web and ChatGPT forward both). Anything
// unrecognized is treated as text-only — the conservative default.
func clientForwardsStructuredContent(clientType string) bool {
	lower := strings.ToLower(strings.TrimSpace(clientType))
	if lower == "" {
		return false
	}
	for _, known := range []string{"claude", "chatgpt"} {
		if strings.Contains(lower, known) {
			return true
		}
	}
	return false
}

// persistCompression writes the pipeline's observations to the durable savings
// ledger so the dashboard sees cross-connection, restart-surviving numbers
// (the in-memory ContextCostStats only ever sees one socket connection).
// Best-effort: measurement must never fail or meaningfully slow a tool call.
func (h *handler) persistCompression(ctx context.Context, obs []compression.Observation) {
	if h == nil || h.store == nil || len(obs) == 0 {
		return
	}
	rows := make([]store.CompressionObservation, 0, len(obs))
	for _, o := range obs {
		wb, wt := o.SavedBytes, o.SavedTokens
		if wb < 0 {
			wb = 0
		}
		if wt < 0 {
			wt = 0
		}
		ab, at := 0, 0
		if o.Applied {
			ab, at = wb, wt
		}
		rows = append(rows, store.CompressionObservation{
			Transform:         o.Transform,
			Lossless:          o.Lossless,
			Changed:           o.Changed,
			Applied:           o.Applied,
			OrigBytes:         o.OrigBytes,
			WouldSaveBytes:    wb,
			WouldSaveTokens:   wt,
			AppliedSaveBytes:  ab,
			AppliedSaveTokens: at,
		})
	}
	// Attribute to the session's workspace so the /stats workspace_id filter is
	// meaningful; empty (no bound workspace) still aggregates globally.
	if err := h.store.RecordCompression(ctx, h.currentWorkspaceID(ctx), time.Now(), rows); err != nil {
		slog.Debug("persist compression stats", "err", err)
	}
}
