package collect

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/sshx"
)

func TestPullOne_SourceDarkAlertsOnThirdFailureAndRearmsAfterRecovery(t *testing.T) {
	host, scope := testHostAndScope()
	storeFake := newConcurrencyStore(host, scope)
	runner := &concurrencyRunner{failIDs: map[string]bool{"s1": true}}
	sink := &syncSink{}
	manager := NewManager(storeFake, fakeSecrets{}, sink, runner)
	manager.now = func() time.Time { return time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC) }
	source := manySources(1)[0]
	source.ID = "s1"

	for previous := 0; previous < 4; previous++ {
		copy := *source
		copy.ConsecutiveFailures = previous
		manager.pullOne(context.Background(), &copy)
	}
	alerts := sink.darkAlerts()
	if len(alerts) != 1 || alerts[0].failures != 3 {
		t.Fatalf("alerts after first failure episode: %+v", alerts)
	}

	runner.failIDs["s1"] = false
	recovered := *source
	recovered.ConsecutiveFailures = 4
	manager.pullOne(context.Background(), &recovered)
	if got := storeFake.failureCount("s1"); got != 0 {
		t.Fatalf("successful pull did not reset failure count: %d", got)
	}

	runner.failIDs["s1"] = true
	for previous := 0; previous < 3; previous++ {
		copy := *source
		copy.ConsecutiveFailures = previous
		manager.pullOne(context.Background(), &copy)
	}
	alerts = sink.darkAlerts()
	if len(alerts) != 2 || alerts[0].episodeID == alerts[1].episodeID {
		t.Fatalf("recovered source did not create a fresh dark episode: %+v", alerts)
	}
}

func TestPullOne_HostKeyMismatchAlertsImmediately(t *testing.T) {
	host, scope := testHostAndScope()
	storeFake := newConcurrencyStore(host, scope)
	runner := &concurrencyRunner{}
	sink := &syncSink{}
	manager := NewManager(storeFake, fakeSecrets{}, sink, runner)
	source := manySources(1)[0]
	source.ID = "s1"
	manager.runner = &fakeRunner{err: &sshx.HostKeyMismatchError{
		Host: "prod:22", Pinned: "SHA256:old", Presented: "SHA256:new",
	}}
	manager.pullOne(context.Background(), source)
	alerts := sink.darkAlerts()
	if len(alerts) != 1 || alerts[0].failures != 1 || alerts[0].reason != FailureReasonHostKeyMismatch {
		t.Fatalf("host-key mismatch alert: %+v", alerts)
	}
}

// TestPullOne_HostKeyMismatchNotSuppressedByPriorOutage is the H8 regression:
// once an ordinary "collection unavailable" episode has been delivered, a
// subsequent host-key mismatch (the MITM/re-provision security signal) must
// still fire its own distinct alert — a reason change starts a fresh episode
// rather than being swallowed by the already-sent outage episode.
func TestPullOne_HostKeyMismatchNotSuppressedByPriorOutage(t *testing.T) {
	host, scope := testHostAndScope()
	storeFake := newConcurrencyStore(host, scope)
	runner := &concurrencyRunner{failIDs: map[string]bool{"s1": true}}
	sink := &syncSink{}
	manager := NewManager(storeFake, fakeSecrets{}, sink, runner)
	manager.now = func() time.Time { return time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC) }
	source := manySources(1)[0]
	source.ID = "s1"

	// Drive an ordinary outage episode to "sent".
	for previous := 0; previous < 3; previous++ {
		copy := *source
		copy.ConsecutiveFailures = previous
		manager.pullOne(context.Background(), &copy)
	}
	alerts := sink.darkAlerts()
	if len(alerts) != 1 || alerts[0].reason != FailureReasonUnavailable {
		t.Fatalf("expected one unavailable alert, got %+v", alerts)
	}

	// Host key changes: every pull now fails with a mismatch (threshold 1).
	manager.runner = &fakeRunner{err: &sshx.HostKeyMismatchError{
		Host: "prod:22", Pinned: "SHA256:old", Presented: "SHA256:new",
	}}
	mismatch := *source
	mismatch.ConsecutiveFailures = 3
	manager.pullOne(context.Background(), &mismatch)

	alerts = sink.darkAlerts()
	if len(alerts) != 2 {
		t.Fatalf("host-key mismatch alert was suppressed by the prior outage episode: %+v", alerts)
	}
	if alerts[1].reason != FailureReasonHostKeyMismatch {
		t.Fatalf("second alert reason = %q, want host_key_mismatch", alerts[1].reason)
	}
	if alerts[0].episodeID == alerts[1].episodeID {
		t.Fatalf("host-key mismatch must be a fresh episode, not the outage one")
	}
}

func TestPullOne_SourceDarkDeliveryFailureRetriesSameEpisode(t *testing.T) {
	host, scope := testHostAndScope()
	storeFake := newConcurrencyStore(host, scope)
	runner := &concurrencyRunner{failIDs: map[string]bool{"s1": true}}
	sink := &syncSink{healthErr: errors.New("pager unavailable")}
	manager := NewManager(storeFake, fakeSecrets{}, sink, runner)
	source := manySources(1)[0]
	source.ID = "s1"

	for previous := 2; previous < 4; previous++ {
		copy := *source
		copy.ConsecutiveFailures = previous
		manager.pullOne(context.Background(), &copy)
	}
	sink.mu.Lock()
	sink.healthErr = nil
	sink.mu.Unlock()
	copy := *source
	copy.ConsecutiveFailures = 4
	manager.pullOne(context.Background(), &copy)
	copy.ConsecutiveFailures = 5
	manager.pullOne(context.Background(), &copy)

	alerts := sink.darkAlerts()
	if len(alerts) != 3 {
		t.Fatalf("delivery attempts=%d want=3", len(alerts))
	}
	for _, alert := range alerts[1:] {
		if alert.episodeID != alerts[0].episodeID {
			t.Fatalf("retry changed episode id: %+v", alerts)
		}
	}
}

func TestPullOne_EmptySuccessfulPullResetsFailures(t *testing.T) {
	manager, storeFake, _ := newFixture(&fakeRunner{})
	source := srcDocker()
	source.ConsecutiveFailures = 2
	manager.dark[source.ID] = darkEpisode{id: "old", sent: true}
	manager.pullOne(context.Background(), source)
	if storeFake.failures != 0 {
		t.Fatalf("empty successful pull left failure count=%d", storeFake.failures)
	}
	if _, exists := manager.dark[source.ID]; exists {
		t.Fatal("successful empty pull did not re-arm source-dark detection")
	}
}
