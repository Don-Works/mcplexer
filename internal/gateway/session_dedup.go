package gateway

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/compression"
)

// Session-level duplicate-result dedup (T-G, 2026-07 audit): when a tool call
// returns bytes identical to a result already delivered earlier in the same
// session (repeated reads, polling loops with unchanged state), the model has
// the content in its context already — re-sending it buys nothing. The repeat
// is replaced with a short pointer envelope; the full original is stashed in
// CCR so the pointer is recoverable even if the earlier copy left the
// (possibly compacted) context.
const (
	sessionDedupName          = "session_dedup"
	sessionDedupMinBytes      = 1024
	sessionDedupMaxPerSession = 256
	sessionDedupMaxSessions   = 64
)

type dedupSeen struct {
	tool string
	at   time.Time
}

type sessionSeen struct {
	hashes map[string]dedupSeen
	order  []string
}

// sessionDedup tracks content hashes of recently delivered results per MCP
// session, bounded in both directions. Safe for concurrent use.
type sessionDedup struct {
	mu       sync.Mutex
	sessions map[string]*sessionSeen
	order    []string
}

func newSessionDedup() *sessionDedup {
	return &sessionDedup{sessions: map[string]*sessionSeen{}}
}

// Process measures (and in On mode applies) the dedup for one delivered
// result. It always records the result's hash so shadow mode measures real
// hit rates. Returns the (possibly replaced) result and an observation slice
// to merge into the compression accounting — empty when the payload is too
// small, an error envelope, or session-less.
func (d *sessionDedup) Process(mode compression.Mode, estimate compression.TokenEstimator, sessionID, toolName string, result json.RawMessage) (json.RawMessage, []compression.Observation) {
	if d == nil || mode == compression.ModeOff || sessionID == "" ||
		len(result) < sessionDedupMinBytes || envelopeHasError(result) {
		return result, nil
	}
	hash := compression.CCRKey(result)
	prev, hit := d.recordAndCheck(sessionID, hash, toolName)
	if !hit {
		return result, nil
	}

	replacement := dedupPointerEnvelope(prev, hash, result)
	o := compression.Observation{
		Transform:  sessionDedupName,
		Lossless:   false,
		OrigBytes:  len(result),
		OutBytes:   len(replacement),
		SavedBytes: len(result) - len(replacement),
		Changed:    true,
		OrigTokens: estimate(len(result)),
		OutTokens:  estimate(len(replacement)),
	}
	o.SavedTokens = o.OrigTokens - o.OutTokens
	if mode != compression.ModeOn || o.SavedBytes <= 0 {
		return result, []compression.Observation{o}
	}
	o.Applied = true
	o.Stash = [][]byte{result}
	return replacement, []compression.Observation{o}
}

// recordAndCheck registers the hash for the session and reports whether it
// was already present (a duplicate delivery).
func (d *sessionDedup) recordAndCheck(sessionID, hash, tool string) (dedupSeen, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.sessions[sessionID]
	if s == nil {
		if len(d.order) >= sessionDedupMaxSessions {
			delete(d.sessions, d.order[0])
			d.order = d.order[1:]
		}
		s = &sessionSeen{hashes: map[string]dedupSeen{}}
		d.sessions[sessionID] = s
		d.order = append(d.order, sessionID)
	}
	if prev, ok := s.hashes[hash]; ok {
		return prev, true
	}
	if len(s.order) >= sessionDedupMaxPerSession {
		delete(s.hashes, s.order[0])
		s.order = s.order[1:]
	}
	s.hashes[hash] = dedupSeen{tool: tool, at: time.Now()}
	s.order = append(s.order, hash)
	return dedupSeen{}, false
}

// dedupPointerEnvelope builds the replacement result: a single text block
// naming the earlier delivery plus a CCR marker for exact recovery.
func dedupPointerEnvelope(prev dedupSeen, hash string, original json.RawMessage) json.RawMessage {
	text := fmt.Sprintf(
		"[unchanged: byte-identical to the result %s returned at %s earlier in this session — reuse that content] %s",
		prev.tool, prev.at.UTC().Format(time.RFC3339),
		compression.CCRMarker(hash, len(original)),
	)
	b, _ := json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	})
	return b
}

// envelopeHasError reports whether the result is an isError envelope — errors
// are never deduped (their repetition is itself signal, and clients may
// pattern-match the exact error shape).
func envelopeHasError(result json.RawMessage) bool {
	var e struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(result, &e); err != nil {
		return true // unparseable → leave alone
	}
	return e.IsError
}
