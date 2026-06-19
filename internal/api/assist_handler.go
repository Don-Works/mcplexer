// assist_handler.go backs the Brain GUI's lean AI augmentation endpoints:
//
//	POST /api/v1/assist/complete          (SSE)  ghost-text continuation
//	POST /api/v1/assist/memory-candidates  (JSON) proactive memory inbox
//	POST /api/v1/assist/guidance           (JSON) inline guidance nudges
//
// Both construct a models.Adapter DIRECTLY via internal/assist — never the
// worker runner (the runner is for scheduled, billed, tool-using jobs, not
// interactive latency). When no model profile resolves, both return 204 so
// the GUI degrades silently.
//
// Streaming note: the models.ModelAdapter interface is one-shot (Send), so
// per-token streaming lives HERE — we call Send once and chunk the single
// result into `event: token` SSE frames. This gives the GUI an incremental
// feed (sub-100ms first-frame once the model returns) without adding a
// streaming method to every provider adapter. The frame contract is
// `event: token` (data: chunk) ... `event: done` (data: {profile}).
package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/assist"
	"github.com/don-works/mcplexer/internal/store"
)

// assistHandler holds the assist engine + the store (for memory-candidate
// suppression filtering). enabled mirrors the brain gate: assist is part of
// the brain surface, so when the brain is disabled these endpoints 503.
type assistHandler struct {
	assistant *assist.Assistant
	store     store.Store
	enabled   bool
}

// ready guards both endpoints. When assist is unwired (brain disabled or no
// store) it 503s so the SPA can tell "not enabled" apart from "no model"
// (which is a 204 silent-degrade).
func (h *assistHandler) ready(w http.ResponseWriter) bool {
	if !h.enabled || h.assistant == nil {
		writeError(w, http.StatusServiceUnavailable, "brain assist not enabled")
		return false
	}
	return true
}

// complete streams a ghost-text continuation over SSE. 204 when no model
// profile resolves (ErrNoProfile) so the client shows nothing.
func (h *assistHandler) complete(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	var req assist.CompleteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode assist complete: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Context) == "" {
		writeError(w, http.StatusBadRequest, "context is required")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Run the (one-shot) completion BEFORE writing any SSE headers so a
	// no-profile 204 / error still maps to a real status code (once headers
	// flush, the status is locked at 200).
	text, profile, err := h.assistant.Complete(r.Context(), req)
	if errors.Is(err, assist.ErrNoProfile) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "assist complete: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	flusher.Flush()

	streamCompletion(w, flusher, text, profile)
}

// streamCompletion chunks one completion into word-granular `event: token`
// frames (so the GUI's graded-accept word boundaries line up), then emits a
// terminal `event: done` carrying the resolving profile for the
// `model · <profile>` provenance label.
func streamCompletion(w http.ResponseWriter, flusher http.Flusher, text, profile string) {
	for _, chunk := range chunkByWord(text) {
		_, _ = fmt.Fprintf(w, "event: token\ndata: %s\n\n", sseData(chunk))
		flusher.Flush()
	}
	_, _ = fmt.Fprintf(w, "event: done\ndata: {\"profile\":%q}\n\n", profile)
	flusher.Flush()
}

// chunkByWord splits s into word-sized chunks, each keeping its leading
// whitespace so concatenating the chunks reproduces s exactly. Empty input
// yields no chunks.
func chunkByWord(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	for i, r := range s {
		if (r == ' ' || r == '\n' || r == '\t') && cur.Len() > 0 && i > 0 {
			// Flush the accumulated word; start the next chunk WITH this
			// separator so the join is lossless.
			out = append(out, cur.String())
			cur.Reset()
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// sseData escapes a chunk for a single SSE data line: newlines would break
// the frame, so encode them as the literal \n the client decodes back.
func sseData(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	return strings.ReplaceAll(s, "\n", "\\n")
}

// guidance returns 0..N inline guidance nudges as JSON (DESIGN §4.4). Unlike
// complete / memoryCandidates this NEVER 204s: the deterministic nudges
// (missing-criteria, auto-tag, entity-extraction) need no model, so guidance
// works even with no profile wired — the model-backed link-memory nudge is
// simply omitted. Still 503s when the brain (assist) is disabled.
func (h *assistHandler) guidance(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	var req assist.GuidanceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode guidance: "+err.Error())
		return
	}
	nudges, profile, err := h.assistant.Guidance(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "assist guidance: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"nudges":  nudges,
		"profile": profile,
	})
}

// memoryCandidates returns 0..N proactive-memory candidates as JSON, after
// filtering any the user has stickily suppressed (the "never" choice). 204
// when no model profile resolves so the right rail stays empty silently.
func (h *assistHandler) memoryCandidates(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	var req assist.MemoryCandidateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode memory candidates: "+err.Error())
		return
	}
	cands, profile, err := h.assistant.MemoryCandidates(r.Context(), req)
	if errors.Is(err, assist.ErrNoProfile) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "assist memory candidates: "+err.Error())
		return
	}
	cands = h.filterSuppressed(r, req.RecordID, cands)
	writeJSON(w, http.StatusOK, map[string]any{
		"candidates": cands,
		"profile":    profile,
	})
}

// filterSuppressed drops candidates the user has stickily suppressed for this
// record (content-hash match, or the suppress-all marker). A nil store or a
// blank record id skips the check (a candidate on an unsaved record can't be
// suppressed yet). A lookup error fails open (keep the candidate) — a missed
// suppression is a lesser harm than dropping a real candidate.
func (h *assistHandler) filterSuppressed(r *http.Request, recordID string, in []assist.Candidate) []assist.Candidate {
	if h.store == nil || strings.TrimSpace(recordID) == "" {
		return in
	}
	out := make([]assist.Candidate, 0, len(in))
	for _, c := range in {
		suppressed, err := h.store.IsCandidateSuppressed(r.Context(), recordID, c.ContentHash)
		if err == nil && suppressed {
			continue
		}
		out = append(out, c)
	}
	return out
}
