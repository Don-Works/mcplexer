package collect

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A tail mismatch cannot manufacture a restart claim without Docker evidence.
func TestPull_CursorMismatchReportsObservationNotCause(t *testing.T) {
	runner := &fakeRunner{out: "2026-07-08T15:00:00Z fresh container banner\n"}
	m, _, sink := newFixture(runner)
	src := srcDocker()
	ts := time.Date(2026, 7, 8, 14, 0, 1, 1, time.UTC)
	src.CursorTS = &ts
	src.CursorHash = "deadbeefdeadbeef"

	if err := m.pullSource(context.Background(), src); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(sink.lines) != 2 || !strings.HasPrefix(sink.lines[0].Text, "logwatch: log cursor discontinuity observed") {
		t.Fatalf("expected factual cursor event first, got %+v", sink.lines)
	}
	if strings.Contains(strings.ToLower(sink.lines[0].Text), "restarted") {
		t.Fatalf("cursor mismatch asserted an unverified restart: %q", sink.lines[0].Text)
	}
}

func TestPull_DockerRestartRequiresExplicitEvidence(t *testing.T) {
	runner := &fakeRunner{
		out: "2026-07-08T15:00:00Z fresh banner\n",
		docker: &DockerObservation{
			Runtime: DockerRuntime{ID: "bbbbbbbbbbbb", RestartCount: 1,
				StartedAt: time.Date(2026, 7, 8, 14, 59, 0, 0, time.UTC)},
			RuntimeOK: true, PortInventoryOK: true,
			RestartEvents: []DockerRestartEvent{{
				At: time.Date(2026, 7, 8, 14, 59, 0, 0, time.UTC), ContainerID: "bbbbbbbbbbbb",
			}},
			EventsAttempted: true, EventsOK: true,
			CheckedThrough: time.Date(2026, 7, 8, 15, 0, 1, 0, time.UTC),
		},
	}
	m, fs, sink := newFixture(runner)
	src := srcDocker()
	ts := time.Date(2026, 7, 8, 14, 0, 1, 0, time.UTC)
	src.CursorTS = &ts
	src.CursorHash = sourceCursorState{Version: 2, TailHash: "deadbeefdeadbeef",
		RuntimeSeen: true, RuntimeID: "bbbbbbbbbbbb", RestartCount: 0,
		StartedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)}.encode()

	if err := m.pullSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	if len(sink.lines) != 3 || !strings.Contains(sink.lines[0].Text, "docker restart verified") {
		t.Fatalf("verified restart evidence missing: %+v", sink.lines)
	}
	if !strings.Contains(sink.lines[1].Text, "log cursor discontinuity") {
		t.Fatalf("independent cursor observation was suppressed: %+v", sink.lines)
	}
	state := decodeCursorState(fs.cursorH)
	if !state.RuntimeSeen || state.RestartCount != 1 || state.EventsSince == "" || state.PortState == "" {
		t.Fatalf("docker observation not persisted: %+v", state)
	}
}
