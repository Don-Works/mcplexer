// monitoring_testingest_handler.go — a gated seam for seeding log history.
//
// THIS IS TEST SCAFFOLDING, not a product feature, and it is labelled as such
// deliberately. store.InsertLogLines is reachable only through
// internal/logwatch/collect, which pulls over SSH or docker; an integration rig
// with no host to SSH into therefore cannot seed a single line, and the whole
// monitoring feature ships with its behaviour unexercised. This endpoint exists
// to close that gap and for no other reason.
//
// Two properties make it worth having rather than harmful:
//
//  1. It enters the pipeline at exactly the seam the collector uses —
//     distill.Distiller.Ingest, the collect.Sink implementation. Lines are
//     masked, classified, template-upserted (novelty and severity included),
//     persisted, rate-spike evaluated and retention-pruned by the same code in
//     the same order. Nothing here reimplements ingestion, so a test that
//     passes proves something about the product rather than about the backdoor.
//
//  2. Timestamps are honoured verbatim, which is the point: absence and
//     baseline behaviour is only observable over days of history. Backdating is
//     also precisely why this must stay gated — a client that can assert
//     arbitrary historical timestamps can poison every learned baseline and
//     manufacture or erase an absence at will.
//
// The gate is off unless MCPLEXER_ALLOW_TEST_INGEST=1, following the existing
// MCPLEXER_ALLOW_* CLI-provider opt-ins. The route is registered either way and
// answers 404 when closed: an unregistered /api/ path falls through to the SPA
// handler and returns index.html with a 200, which is a far worse answer than
// an explicit not-found.
package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/collect"
	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
)

// Bounds. A scenario seeds days of history in one call, so the line cap is
// generous, but neither it nor the body limit is negotiable by the caller.
const (
	testIngestMaxLines     = 5000
	testIngestMaxLineBytes = 8192
	testIngestMaxBodyBytes = 8 << 20
	// testIngestFutureSlack tolerates clock skew between the rig and the
	// daemon. Anything beyond it is rejected: a future timestamp silently
	// breaks every rolling window this feature is built on.
	testIngestFutureSlack = time.Hour
)

// testIngestEnabled reports the env opt-in. Read per request rather than
// cached at boot so a test can flip it without rebuilding a router.
func testIngestEnabled() bool { return os.Getenv("MCPLEXER_ALLOW_TEST_INGEST") == "1" }

// writeTestIngestClosed is the response when the gate is shut. It names the
// variable, because the alternative is an operator staring at a bare 404 for a
// route they can see in the source.
func writeTestIngestClosed(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound,
		"not found — test ingest is disabled; set MCPLEXER_ALLOW_TEST_INGEST=1 to enable it")
}

type monitoringTestIngestHandler struct {
	store    store.Store
	notifier distill.Notifier // may be nil; the distiller then logs only
}

type testIngestLine struct {
	TS      string `json:"ts"`
	Message string `json:"message"`
	// Stream is accepted for contract compatibility and deliberately NOT
	// persisted: collect.Line has no stream concept, so folding "stderr" into
	// the text would produce masked templates that real collection never
	// produces — the exact divergence this endpoint exists to avoid.
	Stream string `json:"stream,omitempty"`
}

// ingest seeds one source's history through the real distill pipeline.
func (h *monitoringTestIngestHandler) ingest(w http.ResponseWriter, r *http.Request) {
	if !testIngestEnabled() {
		writeTestIngestClosed(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, testIngestMaxBodyBytes)
	var in struct {
		WorkspaceID string           `json:"workspace_id"`
		SourceID    string           `json:"source_id"`
		Lines       []testIngestLine `json:"lines"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if in.WorkspaceID == "" || in.SourceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id and source_id are required")
		return
	}
	if len(in.Lines) == 0 {
		writeError(w, http.StatusBadRequest, "lines must not be empty")
		return
	}
	if len(in.Lines) > testIngestMaxLines {
		writeErrorDetail(w, http.StatusRequestEntityTooLarge,
			"too many lines", "at most 5000 lines per call")
		return
	}
	src, host, ok := h.resolveSource(w, r, in.WorkspaceID, in.SourceID)
	if !ok {
		return
	}
	lines, err := buildTestIngestLines(in.Lines, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := distill.NewDistiller(h.store, h.notifier).Ingest(r.Context(), src, host, lines); err != nil {
		writeMonitoringErr(w, err, "ingest test lines")
		return
	}
	writeJSON(w, http.StatusOK, testIngestResult(src, lines))
}

// resolveSource loads the source and its host, scoped to the workspace. A
// foreign source is reported exactly as a missing one, matching the ownership
// conflation the neighbouring monitoring endpoints use.
func (h *monitoringTestIngestHandler) resolveSource(
	w http.ResponseWriter, r *http.Request, wsID, sourceID string,
) (*store.LogSource, *store.RemoteHost, bool) {
	src, err := h.store.GetLogSource(r.Context(), sourceID)
	if err != nil && !errors.Is(err, store.ErrLogSourceNotFound) {
		writeMonitoringErr(w, err, "resolve log source")
		return nil, nil, false
	}
	if err != nil || src.WorkspaceID != wsID {
		writeError(w, http.StatusNotFound, store.ErrLogSourceNotFound.Error())
		return nil, nil, false
	}
	host, err := h.store.GetRemoteHost(r.Context(), src.RemoteHostID)
	if err != nil {
		writeMonitoringErr(w, err, "resolve remote host")
		return nil, nil, false
	}
	return src, host, true
}

// buildTestIngestLines validates and converts the payload. An empty ts defaults
// to now; anything else must be RFC3339, because a permissive parser guessing
// at "2026-07-13 09:00" would silently shift a scenario's history.
func buildTestIngestLines(in []testIngestLine, now time.Time) ([]collect.Line, error) {
	out := make([]collect.Line, 0, len(in))
	for i, line := range in {
		if line.Message == "" {
			return nil, fmt.Errorf("lines[%d]: message is required", i)
		}
		if len(line.Message) > testIngestMaxLineBytes {
			return nil, fmt.Errorf("lines[%d]: message exceeds %d bytes", i, testIngestMaxLineBytes)
		}
		ts := now
		if line.TS != "" {
			parsed, err := time.Parse(time.RFC3339, line.TS)
			if err != nil {
				return nil, fmt.Errorf("lines[%d]: ts must be RFC3339", i)
			}
			ts = parsed.UTC()
		}
		if ts.After(now.Add(testIngestFutureSlack)) {
			return nil, fmt.Errorf("lines[%d]: ts is in the future", i)
		}
		out = append(out, collect.Line{TS: ts, Text: line.Message})
	}
	return out, nil
}

// testIngestResult reports what was accepted AND what retention will do to it.
//
// The last thing Distiller.Ingest does is prune below now-RetentionDays, so
// lines backdated past that boundary are inserted and then deleted within the
// same call. The day-history rows they create survive (the log_template_days
// trigger fires on insert and pruning never touches that table), so template
// cadence still learns from them — but a scenario asserting on retained raw
// lines would be quietly wrong. Reporting the cutoff makes that visible instead
// of leaving it to be discovered as a flaky test.
func testIngestResult(src *store.LogSource, lines []collect.Line) map[string]any {
	cutoff := time.Now().UTC().AddDate(0, 0, -src.RetentionDays)
	first, last, belowRetention := lines[0].TS, lines[0].TS, 0
	for _, line := range lines {
		if line.TS.Before(first) {
			first = line.TS
		}
		if line.TS.After(last) {
			last = line.TS
		}
		if line.TS.Before(cutoff) {
			belowRetention++
		}
	}
	return map[string]any{
		"ingested":     len(lines),
		"source_id":    src.ID,
		"first_ts":     first,
		"last_ts":      last,
		"days_spanned": int(last.Sub(first).Hours()/24) + 1,
		// Raw lines older than this were pruned by the same call that wrote
		// them; their log_template_days rows persist.
		"retention_days":        src.RetentionDays,
		"retention_cutoff":      cutoff,
		"lines_below_retention": belowRetention,
	}
}
