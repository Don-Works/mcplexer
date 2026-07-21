package runner_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

func TestRunnerConcurrencyPolicySkipCoversEveryDispatchPath(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.ConcurrencyPolicy = "skip"
	createWorker(t, db, w)

	adapter := &blockingAdapter{done: make(chan struct{})}
	r := makeRunner(t, db, adapter, &fakeDispatcher{}, &fakeMesh{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := r.RunWithOpts(context.Background(), w.ID, runner.RunOpts{TriggerKind: "mesh"})
		firstDone <- err
	}()
	waitForRunningRun(t, db, w.ID)

	// Manual/mesh callers reach Runner directly; they must be rejected just
	// like a scheduler tick, before a second worker_runs row is created.
	secondID, err := r.RunWithOpts(context.Background(), w.ID, runner.RunOpts{TriggerKind: "manual"})
	if secondID != "" || !errors.Is(err, runner.ErrWorkerConcurrent) {
		t.Fatalf("second run id=%q err=%v, want ErrWorkerConcurrent", secondID, err)
	}
	if running, err := db.CountRunningWorkerRuns(context.Background(), w.ID); err != nil || running != 1 {
		t.Fatalf("running rows=%d err=%v, want exactly one", running, err)
	}

	close(adapter.done)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first run did not finish")
	}
	runs, err := db.ListWorkerRuns(context.Background(), w.ID, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("persisted runs=%d err=%v, want one", len(runs), err)
	}
}

func TestRunnerConcurrencyPolicyQueueStillAllowsParallelRuns(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.ConcurrencyPolicy = "queue"
	createWorker(t, db, w)

	adapter := &blockingAdapter{done: make(chan struct{})}
	r := runner.New(runner.Deps{
		Store: db, Dispatcher: &fakeDispatcher{}, Mesh: &fakeMesh{}, Secrets: &fakeSecrets{},
		Adapter: func(_ models.Config) (models.ModelAdapter, error) { return adapter, nil },
	})
	done := make(chan error, 2)
	for _, trigger := range []string{"mesh", "manual"} {
		trigger := trigger
		go func() {
			_, err := r.RunWithOpts(context.Background(), w.ID, runner.RunOpts{TriggerKind: trigger})
			done <- err
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		running, err := db.CountRunningWorkerRuns(context.Background(), w.ID)
		if err != nil {
			t.Fatal(err)
		}
		if running == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue policy reached %d parallel runs, want 2", running)
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(adapter.done)
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("queued run did not finish")
		}
	}
}
