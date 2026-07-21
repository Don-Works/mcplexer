package downstream

import (
	"time"
)

// TimingStatus categorises the outcome of a single per-server tools/list
// invocation for telemetry. Mirrored in the dashboard so users can see
// which downstreams are healthy / slow / failing at a glance.
type TimingStatus string

const (
	TimingOK      TimingStatus = "ok"
	TimingSlow    TimingStatus = "slow"
	TimingTimeout TimingStatus = "timeout"
	TimingError   TimingStatus = "error"
)

// ServerTiming is the most-recent tools/list outcome for a single
// downstream server. Surface this via API for the dashboard's server-
// performance panel.
type ServerTiming struct {
	ServerID   string       `json:"server_id"`
	ServerName string       `json:"server_name"`
	Status     TimingStatus `json:"status"`
	ElapsedMS  int64        `json:"elapsed_ms"`
	At         time.Time    `json:"at"`
}

// recordListToolsTiming snapshots the most-recent outcome of a per-server
// tools/list call. Subsequent calls for the same server overwrite. The
// snapshot is read by API consumers; callers that want history should
// subscribe to logs.
func (m *Manager) recordListToolsTiming(id string, status TimingStatus, elapsed time.Duration, name string) {
	if id == "" {
		return
	}
	if name == "" {
		name = id
	}
	m.timingsMu.Lock()
	m.latestTimings[id] = ServerTiming{
		ServerID:   id,
		ServerName: name,
		Status:     status,
		ElapsedMS:  elapsed.Milliseconds(),
		At:         time.Now(),
	}
	m.timingsMu.Unlock()
}

// LatestTimings returns a snapshot of the most-recent tools/list outcomes
// for every server observed since startup. Safe to call from any
// goroutine; returns a copy.
func (m *Manager) LatestTimings() []ServerTiming {
	m.timingsMu.RLock()
	defer m.timingsMu.RUnlock()
	out := make([]ServerTiming, 0, len(m.latestTimings))
	for _, t := range m.latestTimings {
		out = append(out, t)
	}
	return out
}
