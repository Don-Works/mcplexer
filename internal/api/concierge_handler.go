// concierge_handler.go — REST surfaces for the concierge self-improving
// chat loop that sit alongside the chat-signals log:
//
//	GET  /api/v1/concierge/ab/arms       → A/B arm leaderboard (win/loss/draw
//	                                       counts derived from chat-turn signals)
//	POST /api/v1/concierge/lessons/pin   → deterministic friction-extractor
//	                                       seed (creates a lesson memory)
//
// Both endpoints exist to let the integration suite
// (scenario_concierge_self_improving, D1) assert the loop end-to-end
// without having to drive the LLM-backed friction-extractor worker. They
// surface state that already lives in the system (chat_turn_signals
// rows, memory entries) — no new persistence is introduced here.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/concierge"
	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
)

type conciergeHandler struct {
	svc    *concierge.Service
	memSvc *memory.Service
}

func newConciergeHandler(svc *concierge.Service, memSvc *memory.Service) *conciergeHandler {
	return &conciergeHandler{svc: svc, memSvc: memSvc}
}

// armRow is one row in the GET /api/v1/concierge/ab/arms response. The
// {id, label, wins, losses, draws, last_used_at} shape is the contract
// the integration test asserts against — see
// scenario_concierge_self_improving, step D1.
//
//	wins   = confirmation signals (positive feedback)
//	losses = correction + frustration signals (negative feedback)
//	draws  = neutral + redirect + escalation signals (non-signal +
//	         conversation-shape changes that don't reflect on the arm
//	         quality directly)
type armRow struct {
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	Wins       int       `json:"wins"`
	Losses     int       `json:"losses"`
	Draws      int       `json:"draws"`
	LastUsedAt time.Time `json:"last_used_at"`
}

// armsResponse is the GET /api/v1/concierge/ab/arms envelope.
type armsResponse struct {
	Arms []armRow `json:"arms"`
}

// handleArms serves GET /api/v1/concierge/ab/arms.
//
// Query parameters (all optional, exact match):
//
//	worker_id, workspace_id, channel, user_id_external
//	limit — passed through to the underlying signal listing
//	         (default 1000 so the leaderboard sees enough evidence)
//
// Arms are sorted by wins DESC, then by total signals DESC (more
// evidence breaks ties). Ties on both fall back to lexicographic id.
func (h *conciergeHandler) handleArms(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ChatTurnSignalFilter{
		WorkerID:       strings.TrimSpace(q.Get("worker_id")),
		WorkspaceID:    strings.TrimSpace(q.Get("workspace_id")),
		Channel:        strings.TrimSpace(q.Get("channel")),
		UserIDExternal: strings.TrimSpace(q.Get("user_id_external")),
		// The leaderboard wants the full window, not just the most recent
		// page. The store caps Limit at 1000 (per chat_turn_signals.go); a
		// future enhancement can move the aggregation into SQL when this
		// becomes a bottleneck.
		Limit: 1000,
	}
	rows, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "arms list failed", err.Error())
		return
	}
	out := buildArmLeaderboard(rows)
	writeJSON(w, http.StatusOK, armsResponse{Arms: out})
}

// armBucket is the in-flight accumulator used while reducing chat-turn
// signals into the per-arm leaderboard. Lifted to package scope so the
// sort helper can reach the total field.
type armBucket struct {
	row   armRow
	total int
}

// buildArmLeaderboard reduces signal rows into per-arm rollups. Split
// out so the handler stays small and the aggregation is unit-testable
// without spinning up an HTTP server.
func buildArmLeaderboard(rows []store.ChatTurnSignal) []armRow {
	agg := make(map[string]*armBucket)
	for _, r := range rows {
		id := fmt.Sprintf("%s:v%d", r.WorkerID, r.PromptVersion)
		b, ok := agg[id]
		if !ok {
			b = &armBucket{row: armRow{
				ID:    id,
				Label: fmt.Sprintf("v%d", r.PromptVersion),
			}}
			agg[id] = b
		}
		switch r.Label {
		case store.ChatTurnLabelConfirmation:
			b.row.Wins++
		case store.ChatTurnLabelCorrection, store.ChatTurnLabelFrustration:
			b.row.Losses++
		case store.ChatTurnLabelNeutral, store.ChatTurnLabelRedirect,
			store.ChatTurnLabelEscalation:
			b.row.Draws++
		}
		b.total++
		if r.CreatedAt.After(b.row.LastUsedAt) {
			b.row.LastUsedAt = r.CreatedAt
		}
	}
	out := make([]armRow, 0, len(agg))
	for _, b := range agg {
		out = append(out, b.row)
	}
	sortArms(out, agg)
	return out
}

// sortArms in-place — wins DESC, total DESC, id ASC. Insertion sort
// because N is bounded by the number of (worker, prompt_version) pairs,
// which is tiny in practice (< 10).
func sortArms(rows []armRow, agg map[string]*armBucket) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			a, b := rows[j-1], rows[j]
			if armLess(a, b, agg) {
				break
			}
			rows[j-1], rows[j] = b, a
		}
	}
}

func armLess(a, b armRow, agg map[string]*armBucket) bool {
	if a.Wins != b.Wins {
		return a.Wins > b.Wins
	}
	ta, tb := agg[a.ID].total, agg[b.ID].total
	if ta != tb {
		return ta > tb
	}
	return a.ID < b.ID
}

// pinLessonRequest is the POST body for /api/v1/concierge/lessons/pin.
//
//	topic              short subject the lesson is about (used in the
//	                   memory name so RecentLessonsFor can scope it)
//	lesson             the one-line rule to remember (required)
//	evidence_summary   why this lesson — surfaces in the memory metadata
//	                   so an operator reading the dashboard can audit
//	                   what drove the pin
type pinLessonRequest struct {
	Topic           string `json:"topic"`
	Lesson          string `json:"lesson"`
	EvidenceSummary string `json:"evidence_summary"`
}

// pinLessonResponse echoes the persisted memory id so the caller can
// link audit rows.
type pinLessonResponse struct {
	MemoryID string `json:"memory_id"`
}

// handlePinLesson serves POST /api/v1/concierge/lessons/pin.
//
// Creates a kind=fact memory tagged "concierge_lesson" (+ legacy
// "concierge"/"lesson" tags so RecentLessonsFor and the integration
// suite's tag-filter lookup both see it). Pinned=true so the memory
// consolidator's auto-prune skips it.
func (h *conciergeHandler) handlePinLesson(w http.ResponseWriter, r *http.Request) {
	if h.memSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "memory service not wired")
		return
	}
	var body pinLessonRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	lesson := strings.TrimSpace(body.Lesson)
	if lesson == "" {
		writeError(w, http.StatusBadRequest, "lesson is required")
		return
	}
	topic := strings.TrimSpace(body.Topic)
	// Cap topic length defensively — it ends up in the memory `name`
	// column which the consolidator groups on. 120 chars is plenty for
	// a topic line.
	if len(topic) > 120 {
		topic = topic[:120]
	}

	name := "concierge.lesson:global"
	if topic != "" {
		name = "concierge.lesson:" + topic
	}
	tags := []string{"concierge_lesson", "concierge", "lesson"}
	meta := map[string]any{
		"topic":            topic,
		"evidence_summary": strings.TrimSpace(body.EvidenceSummary),
		"pinned_at":        time.Now().UTC().Format(time.RFC3339),
		"source":           "concierge.lessons.pin",
	}
	id, err := h.memSvc.Write(r.Context(), memory.WriteOptions{
		Name:       name,
		Kind:       store.MemoryKindFact,
		Content:    lesson,
		Tags:       tags,
		Metadata:   meta,
		Pinned:     true,
		SourceKind: store.MemorySourceAgent,
	})
	if err != nil {
		// Validation errors from memory.Write (missing name/content) get
		// surfaced as 400; anything else is internal.
		if errors.Is(err, errMemoryValidation) ||
			strings.Contains(err.Error(), "memory: name required") ||
			strings.Contains(err.Error(), "memory: content required") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError, "pin lesson failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, pinLessonResponse{MemoryID: id})
}

// errMemoryValidation is a sentinel that lets handlePinLesson distinguish
// validation errors from infra errors. memory.Write currently returns
// plain errors.New strings — until that's tightened up, we string-match
// the well-known prefixes (above). This sentinel reserves a future hook.
var errMemoryValidation = errors.New("memory validation error")
