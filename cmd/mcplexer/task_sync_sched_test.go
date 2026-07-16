package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
)

type schedulerDialer struct{ err error }

func (d *schedulerDialer) ConnectToPeer(context.Context, string, []p2p.TaskSyncHelloWorkspace) error {
	return d.err
}

type schedulerOutbox struct{ calls int }

func (o *schedulerOutbox) RetryPendingHomePublications(context.Context, string) (int, int, error) {
	o.calls++
	return 1, 1, nil
}

func TestTaskSyncSchedulerRetriesOutboxOnlyAfterAuthenticatedSync(t *testing.T) {
	outbox := &schedulerOutbox{}
	scheduler := &taskSyncScheduler{
		dialer: &schedulerDialer{}, outbox: outbox,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		peerBudget: time.Second,
	}
	if err := scheduler.syncPeer(context.Background(), "peer-home", []p2p.TaskSyncHelloWorkspace{{WorkspaceID: "remote"}}); err != nil {
		t.Fatal(err)
	}
	if outbox.calls != 1 {
		t.Fatalf("outbox calls = %d, want 1", outbox.calls)
	}
	scheduler.dialer = &schedulerDialer{err: errors.New("offline")}
	if err := scheduler.syncPeer(context.Background(), "peer-home", []p2p.TaskSyncHelloWorkspace{{WorkspaceID: "remote"}}); err == nil {
		t.Fatal("offline sync returned nil error")
	}
	if outbox.calls != 1 {
		t.Fatalf("offline sync retried outbox: calls=%d", outbox.calls)
	}
}
