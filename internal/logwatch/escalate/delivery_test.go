package escalate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type scriptedSender struct {
	errs     []error
	attempts int
}

func (s *scriptedSender) Send(context.Context, *store.MonitoringChannel, string, string) error {
	index := s.attempts
	s.attempts++
	if index < len(s.errs) {
		return s.errs[index]
	}
	return nil
}

func TestDispatcher_SeverityIncreaseBypassesCooldownAndMinorBudget(t *testing.T) {
	sender := &captureSender{}
	d, _ := newTestDispatcher([]*store.MonitoringChannel{{
		Name: "feed", Kind: store.ChannelKindMesh, MinSeverity: store.SeverityInfo,
		Enabled: true, WorkspaceID: "ws",
	}}, map[string]Sender{store.ChannelKindMesh: sender})

	if err := d.Notify(context.Background(), testNotification(store.SeverityError, "same")); err != nil {
		t.Fatal(err)
	}
	if err := d.Notify(context.Background(), testNotification(store.SeverityCritical, "same")); err != nil {
		t.Fatal(err)
	}
	if len(sender.got) != 2 {
		t.Fatalf("error→critical escalation must bypass cooldown: sends=%d", len(sender.got))
	}

	for i := 0; i < maxNotifiesPerHour-1; i++ {
		n := testNotification(store.SeverityError, "lower-"+string(rune('a'+i)))
		if err := d.Notify(context.Background(), n); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Notify(context.Background(), testNotification(store.SeverityCritical, "fresh-critical")); err != nil {
		t.Fatal(err)
	}
	if len(sender.got) != maxNotifiesPerHour+2 {
		t.Fatalf("lower-severity budget consumed critical capacity: sends=%d", len(sender.got))
	}
}

func TestDispatcher_RetriesOnlyTransientFailures(t *testing.T) {
	tests := []struct {
		name         string
		errs         []error
		wantAttempts int
		wantErr      bool
	}{
		{name: "transient recovers", errs: []error{
			transient(errors.New("temporary one")), transient(errors.New("temporary two")), nil,
		}, wantAttempts: 3},
		{name: "permanent stops", errs: []error{errors.New("bad credentials")}, wantAttempts: 1, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sender := &scriptedSender{errs: tc.errs}
			d, _ := newTestDispatcher([]*store.MonitoringChannel{{
				Name: "pager", Kind: store.ChannelKindMesh,
				MinSeverity: store.SeverityCritical, Enabled: true, WorkspaceID: "ws",
			}}, map[string]Sender{store.ChannelKindMesh: sender})
			d.retryPause = func(context.Context, time.Duration) error { return nil }
			n := testNotification(store.SeverityCritical, "retry")
			n.NewIncident = true
			err := d.Notify(context.Background(), n)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Notify error=%v wantErr=%v", err, tc.wantErr)
			}
			if sender.attempts != tc.wantAttempts {
				t.Fatalf("attempts=%d want=%d", sender.attempts, tc.wantAttempts)
			}
		})
	}
}

func TestDispatcher_CriticalFailureIsObservableAndHumanClaimRetries(t *testing.T) {
	d, _ := newTestDispatcher(nil, nil)
	human := &captureHumanPublisher{err: errors.New("push unavailable")}
	d.RegisterHumanPublisher(human)
	n := testNotification(store.SeverityCritical, "critical-retry")
	n.NewIncident = true

	err := d.Notify(context.Background(), n)
	if err == nil || !strings.Contains(err.Error(), "critical delivery not accepted") {
		t.Fatalf("first delivery error=%v", err)
	}
	human.err = nil
	if err := d.Notify(context.Background(), n); err != nil {
		t.Fatalf("retry after failed durable publish: %v", err)
	}
	if len(human.durable) != 2 {
		t.Fatalf("failed human claim was not released: attempts=%d", len(human.durable))
	}
}

func TestDispatcher_NoCriticalRouteFailsButMinorStaysQuiet(t *testing.T) {
	d, _ := newTestDispatcher(nil, nil)
	if err := d.Notify(context.Background(), testNotification(store.SeverityWarn, "warn")); err != nil {
		t.Fatalf("minor no-route event should remain quiet: %v", err)
	}
	errorIncident := testNotification(store.SeverityError, "error")
	errorIncident.NewIncident = true
	if err := d.Notify(context.Background(), errorIncident); err == nil {
		t.Fatal("new error incident without a configured route must report delivery failure")
	}
	critical := testNotification(store.SeverityCritical, "critical")
	critical.NewIncident = true
	err := d.Notify(context.Background(), critical)
	if err == nil || !strings.Contains(err.Error(), "no eligible delivery route") {
		t.Fatalf("critical no-route error=%v", err)
	}
}
