package distill

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestNotifyCollectionFailureIsCriticalAndPropagatesDeliveryFailure(t *testing.T) {
	notifier := &captureNotifier{}
	distiller := NewDistiller(&fakeDistillStore{}, notifier)
	source := &store.LogSource{
		ID: "source-1", WorkspaceID: "ws", RemoteHostID: "host-1", Name: "api",
	}
	host := &store.RemoteHost{ID: "host-1", Name: "production", SSHHost: "10.0.0.1"}
	if err := distiller.NotifyCollectionFailure(
		context.Background(), source, host, 3, "episode-a",
	); err != nil {
		t.Fatal(err)
	}
	if len(notifier.notes) != 1 {
		t.Fatalf("notifications=%d", len(notifier.notes))
	}
	note := notifier.notes[0]
	if note.Severity != store.SeverityCritical || !note.NewIncident ||
		note.TemplateID != "source-dark:source-1:episode-a" {
		t.Fatalf("source-dark notification: %+v", note)
	}
	if strings.Contains(note.Body, "password") {
		t.Fatalf("source-dark body must contain guidance, not raw pull errors: %q", note.Body)
	}

	notifier.err = errors.New("no route")
	if err := distiller.NotifyCollectionFailure(
		context.Background(), source, host, 4, "episode-a",
	); err == nil {
		t.Fatal("delivery failure was swallowed")
	}
}
