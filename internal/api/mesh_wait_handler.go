package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
)

// meshWaitDefaultTimeout is the long-poll default when the caller omits
// timeout=. meshWaitMaxTimeout caps it server-side. Mirrors the pinned HTTP
// contract (default 1800s, max 3600s).
const (
	meshWaitDefaultTimeout = 1800 * time.Second
	meshWaitMaxTimeout     = 3600 * time.Second
)

// meshWaitHandler exposes mesh.Manager.WaitForMessage over HTTP as an
// event-driven long-poll. Auth (Bearer + localhost-only) is supplied by the
// global middleware chain wrapping the mux — same posture as mesh_send.
type meshWaitHandler struct {
	mgr *mesh.Manager
}

// meshWaitMessage is the per-message shape returned in a 200 body. Mirrors the
// public fields of store.MeshMessage the contract pins.
type meshWaitMessage struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Priority    string    `json:"priority"`
	Sender      string    `json:"sender"`
	Audience    string    `json:"audience"`
	Tags        []string  `json:"tags"`
	Content     string    `json:"content"`
	CreatedAt   time.Time `json:"created_at"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	ReplyTo     string    `json:"reply_to,omitempty"`
	ThreadRoot  string    `json:"thread_root,omitempty"`
}

type meshWaitResponse struct {
	Matched  bool              `json:"matched"`
	Count    int               `json:"count"`
	Messages []meshWaitMessage `json:"messages"`
}

// wait parses the query params per the contract, ties the wait to the request
// context (client disconnect cancels), and maps the result:
//   - matches  -> 200 {matched,count,messages}
//   - timeout  -> 204 (empty body)
//   - unknown agent -> 400 {error}
//   - mesh disabled  -> 503
func (h *meshWaitHandler) wait(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.mgr == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh disabled")
		return
	}
	q := r.URL.Query()
	agent := strings.TrimSpace(q.Get("agent"))
	if agent == "" {
		writeError(w, http.StatusBadRequest, "agent is required")
		return
	}

	crit := mesh.WaitCriteria{
		AgentName:        agent,
		Role:             strings.TrimSpace(q.Get("role")),
		Tags:             splitCSV(q.Get("tags")),
		Kinds:            splitCSV(q.Get("kind")),
		FromPeer:         strings.TrimSpace(q.Get("from")),
		IncludeRole:      parseBoolDefault(q.Get("include_role"), false),
		IncludeBroadcast: parseBoolDefault(q.Get("include_broadcast"), false),
		Consume:          parseBoolDefault(q.Get("consume"), false),
		WorkspaceID:      strings.TrimSpace(q.Get("workspace_id")),
	}
	timeout := parseWaitTimeout(q.Get("timeout"))

	msgs, err := h.mgr.WaitForMessage(r.Context(), crit, timeout)
	if err != nil {
		if errors.Is(err, mesh.ErrUnknownAgent) {
			writeError(w, http.StatusBadRequest,
				"unknown agent "+agent+"; register via mesh__receive(name:"+agent+") first")
			return
		}
		// Client disconnect / deadline: nothing to send, drop the request.
		if r.Context().Err() != nil {
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(msgs) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	out := make([]meshWaitMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, toWaitMessage(m))
	}
	writeJSON(w, http.StatusOK, meshWaitResponse{Matched: true, Count: len(out), Messages: out})
}

// toWaitMessage projects a store.MeshMessage onto the wire shape, preferring
// the friendly sender name and falling back to the routing session_id.
func toWaitMessage(m *store.MeshMessage) meshWaitMessage {
	sender := m.SenderDisplayName
	if sender == "" {
		sender = m.AgentName
	}
	if sender == "" {
		sender = m.SessionID
	}
	return meshWaitMessage{
		ID:          m.ID,
		Kind:        m.Kind,
		Priority:    m.Priority,
		Sender:      sender,
		Audience:    m.Audience,
		Tags:        splitCSV(m.Tags),
		Content:     m.Content,
		CreatedAt:   m.CreatedAt,
		WorkspaceID: m.WorkspaceID,
		ReplyTo:     m.ReplyTo,
		ThreadRoot:  m.ThreadRoot,
	}
}

// parseWaitTimeout reads the timeout= query value (seconds), applying the
// contract default + server cap. Non-numeric / non-positive falls to default.
func parseWaitTimeout(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return meshWaitDefaultTimeout
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		return meshWaitDefaultTimeout
	}
	d := time.Duration(secs) * time.Second
	if d > meshWaitMaxTimeout {
		return meshWaitMaxTimeout
	}
	return d
}

// parseBoolDefault parses a query-param bool, returning def when empty or
// unparseable so callers get the contract's documented default.
func parseBoolDefault(raw string, def bool) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return b
}

// splitCSV trims + splits a comma-separated value, dropping empties. Returns
// nil for empty input so downstream len checks skip the filter.
func splitCSV(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
