package collect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// sourceCursorState keeps the log-tail cursor and the last explicit Docker
// observation in the existing cursor_hash column. Legacy plain hashes remain
// readable, so this can roll out without a schema migration or cursor reset.
type sourceCursorState struct {
	Version      int    `json:"v"`
	TailHash     string `json:"tail,omitempty"`
	RuntimeSeen  bool   `json:"runtime_seen,omitempty"`
	RuntimeID    string `json:"runtime_id,omitempty"`
	RestartCount int    `json:"restart_count,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	EventsSince  string `json:"events_since,omitempty"`
	PortState    string `json:"port_state,omitempty"`
}

func decodeCursorState(raw string) sourceCursorState {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "{") {
		var state sourceCursorState
		if json.Unmarshal([]byte(raw), &state) == nil && state.Version == 2 {
			return state
		}
	}
	return sourceCursorState{TailHash: raw}
}

func (s sourceCursorState) encode() string {
	if !s.RuntimeSeen && s.EventsSince == "" && s.PortState == "" {
		return s.TailHash
	}
	s.Version = 2
	b, err := json.Marshal(s)
	if err != nil {
		return s.TailHash
	}
	return string(b)
}

func (s sourceCursorState) eventSince(fallback time.Time) time.Time {
	if s.EventsSince != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, s.EventsSince); err == nil {
			return parsed.UTC()
		}
	}
	return fallback
}

func (s sourceCursorState) startedAt() time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, s.StartedAt)
	return parsed.UTC()
}

// reconcileCursor checks only log-stream continuity. A mismatch is not a
// restart claim: stdout/stderr interleaving can reorder an inclusive --since
// result even when the container never changed state.
func reconcileCursor(
	lines []Line, firstRaw rawLine, src *store.LogSource, state sourceCursorState,
) ([]Line, bool) {
	if src.CursorTS == nil || state.TailHash == "" || len(lines) == 0 {
		return lines, false
	}
	if lineHash(firstRaw.raw) == state.TailHash {
		return lines[1:], false
	}
	return lines, true
}

func cursorDiscontinuityLine(src *store.LogSource, first rawLine, observedAt time.Time) Line {
	base := Line{TS: observedAt}
	if src.CursorTS == nil || first.ts.IsZero() {
		base.Text = "logwatch: log stream non-monotonic observed — cursor tail was not the first returned line; restart not asserted"
		return base
	}
	delta := first.ts.Sub(*src.CursorTS)
	switch {
	case delta > 0:
		base.Text = fmt.Sprintf("logwatch: log cursor discontinuity observed — first returned timestamp is %s after the cursor; ingestion-gap evidence only, cause unverified", delta)
	case delta < 0:
		base.Text = fmt.Sprintf("logwatch: log stream non-monotonic observed — first returned timestamp is %s before the cursor; restart not asserted", -delta)
	default:
		base.Text = "logwatch: log stream non-monotonic observed — inclusive cursor returned a different first line at the same timestamp; restart not asserted"
	}
	return base
}

func lineHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}
