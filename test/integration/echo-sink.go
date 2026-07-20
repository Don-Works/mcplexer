// echo-sink.go — a webhook sink for the monitoring scenarios, hosted inside
// the echo-llm stub so that NO notification ever leaves the container.
//
// The monitoring acceptance tests have to prove two things about alert
// delivery, and neither can be proved by watching a real channel:
//
//  1. a healthy route accepts the message and the delivery is RECORDED, so a
//     scenario can assert on delivery state rather than on the absence of an
//     error; and
//  2. a route that rejects every message with HTTP 400 stays visible even
//     while a throttle is suppressing traffic — the exact shape of the
//     2026-07-14 incident, where a webhook 400'd once, logged "send failed",
//     and the workspace hourly cap then masked it for six days.
//
// Both routes record what they received, so "was the operator actually told?"
// is answered by reading /sink/deliveries rather than by inferring from a
// return code. Recording is capped so a runaway retry loop cannot exhaust
// container memory during an overnight repeat run.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

const maxRecordedDeliveries = 512

// delivery is one POST the sink received, in arrival order.
type delivery struct {
	At     time.Time `json:"at"`
	Path   string    `json:"path"`
	Status int       `json:"status"`
	Body   string    `json:"body"`
}

// sink records deliveries for later assertion. Guarded by a mutex because the
// dispatcher fans out to routes concurrently.
type sink struct {
	mu   sync.Mutex
	rows []delivery
}

var deliverySink = &sink{}

// record appends one delivery, dropping the oldest once the cap is reached so
// the sink degrades to a ring buffer rather than growing without bound.
func (s *sink) record(d delivery) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, d)
	if len(s.rows) > maxRecordedDeliveries {
		s.rows = s.rows[len(s.rows)-maxRecordedDeliveries:]
	}
}

// snapshot returns a copy so the caller never reads the slice under mutation.
func (s *sink) snapshot() []delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]delivery, len(s.rows))
	copy(out, s.rows)
	return out
}

func (s *sink) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = nil
}

// handleSink records the request and answers with the status the path
// promises. The 400 path is deliberately NON-transient (see
// escalate.transientHTTPStatus): a permanently rejecting route must not be
// retried into looking like a flake, because Incident 2 was a permanent
// rejection that everything downstream treated as noise.
func handleSink(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
		deliverySink.record(delivery{
			At: time.Now().UTC(), Path: r.URL.Path,
			Status: status, Body: string(body),
		})
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"sink":"ok"}`))
	}
}

// handleDeliveries serves the recorded delivery ledger. DELETE clears it so a
// scenario can establish a clean baseline without restarting the container.
func handleDeliveries(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		deliverySink.reset()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	rows := deliverySink.snapshot()
	accepted, rejected := 0, 0
	for _, d := range rows {
		if d.Status >= 200 && d.Status < 300 {
			accepted++
			continue
		}
		rejected++
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"deliveries": rows, "total": len(rows),
		"accepted": accepted, "rejected": rejected,
	})
}

// registerSink wires the sink routes onto the echo-llm mux.
func registerSink(mux *http.ServeMux) {
	mux.HandleFunc("/sink/ok", handleSink(http.StatusOK))
	mux.HandleFunc("/sink/reject", handleSink(http.StatusBadRequest))
	mux.HandleFunc("/sink/deliveries", handleDeliveries)
}
