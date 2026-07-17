package collect

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A steady Docker pull is exclusive, so absence of the stored tail is expected
// and must not manufacture a discontinuity or restart claim.
func TestPull_ExclusiveCursorDoesNotAssertTailMismatch(t *testing.T) {
	runner := &fakeRunner{out: "2026-07-08T15:00:00Z fresh container banner\n"}
	m, _, sink := newFixture(runner)
	src := srcDocker()
	ts := time.Date(2026, 7, 8, 14, 0, 1, 1, time.UTC)
	src.CursorTS = &ts
	src.CursorHash = "deadbeefdeadbeef"

	if err := m.pullSource(context.Background(), src); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(sink.lines) != 1 || sink.lines[0].Text != "fresh container banner" {
		t.Fatalf("exclusive pull filed synthetic continuity evidence: %+v", sink.lines)
	}
	if !runner.gotSince.Equal(ts.Add(time.Nanosecond)) {
		t.Fatalf("exclusive boundary: got %v", runner.gotSince)
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
	if len(sink.lines) != 2 || !strings.Contains(sink.lines[0].Text, "docker restart verified") {
		t.Fatalf("verified restart evidence missing: %+v", sink.lines)
	}
	for _, line := range sink.lines {
		if strings.Contains(line.Text, "discontinuity") || strings.Contains(line.Text, "non-monotonic") {
			t.Fatalf("exclusive pull filed invalid tail-first evidence: %+v", sink.lines)
		}
	}
	state := decodeCursorState(fs.cursorH)
	if !state.RuntimeSeen || state.RestartCount != 1 || state.EventsSince == "" || state.PortState == "" {
		t.Fatalf("docker observation not persisted: %+v", state)
	}
}
