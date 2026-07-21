package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type fakeDaemonRunner struct {
	calls atomic.Int32
	err   error
}

func (f *fakeDaemonRunner) RunNow(_ context.Context, _ string) error {
	f.calls.Add(1)
	return f.err
}

func TestRunJobMissingJob(t *testing.T) {
	st := newMemStore()
	exit, err := RunJob(context.Background(), "missing", st, nil)
	if exit != ExitNotFound || err == nil {
		t.Errorf("got exit=%d err=%v; want ExitNotFound + err", exit, err)
	}
}

func TestRunJobHandsOffToDaemon(t *testing.T) {
	st := newMemStore()
	nextAt := time.Now().Add(time.Hour).UTC()
	_ = st.CreateScheduledJob(context.Background(), &store.ScheduledJob{
		ID:        "j",
		Name:      "j",
		Kind:      KindInterval,
		Spec:      "1h",
		Command:   "/bin/true",
		Enabled:   true,
		NextRunAt: &nextAt,
	})
	d := &fakeDaemonRunner{}
	exit, err := RunJob(context.Background(), "j", st, d)
	if exit != ExitOK || err != nil {
		t.Fatalf("hand-off path: exit=%d err=%v", exit, err)
	}
	if d.calls.Load() != 1 {
		t.Errorf("daemon RunNow called %d times, want 1", d.calls.Load())
	}
}

func TestRunJobDirectExecFallthrough(t *testing.T) {
	st := newMemStore()
	_ = st.CreateScheduledJob(context.Background(), &store.ScheduledJob{
		ID:      "true",
		Name:    "true",
		Kind:    KindInterval,
		Spec:    "1h",
		Command: "/usr/bin/true",
		Enabled: true,
	})
	// Daemon errors out -> RunJob falls through to direct exec.
	d := &fakeDaemonRunner{err: errors.New("daemon down")}
	exit, err := RunJob(context.Background(), "true", st, d)
	if exit != ExitOK || err != nil {
		t.Fatalf("direct exec path: exit=%d err=%v", exit, err)
	}
}

func TestRunJobDirectExecNoDaemon(t *testing.T) {
	st := newMemStore()
	_ = st.CreateScheduledJob(context.Background(), &store.ScheduledJob{
		ID:      "true2",
		Name:    "true2",
		Kind:    KindInterval,
		Spec:    "1h",
		Command: "/usr/bin/true",
		Enabled: true,
	})
	exit, err := RunJob(context.Background(), "true2", st, nil)
	if exit != ExitOK || err != nil {
		t.Fatalf("nil daemon: exit=%d err=%v", exit, err)
	}
}
