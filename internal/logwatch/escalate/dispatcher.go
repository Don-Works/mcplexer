// Package escalate is the Monitoring notification dispatcher — the only
// outbound path for logwatch incidents.
package escalate

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/store"
)

const (
	perTemplateCooldown = time.Hour
	// perTemplateCooldownCritical is the cooldown for critical traffic. It must
	// stay strictly BELOW the persistence policy's tightest critical cadence
	// (monitoringRenotifyBaseCritical, 30m) — otherwise the dispatcher vetoes a
	// reminder the policy deliberately scheduled, and because the renotify sweep
	// advances the incident's backoff on a nil return, that reminder is consumed
	// rather than deferred. Modelling a 24h critical incident against the policy
	// showed the 1h cooldown swallowing the 30m and 4h reminders — the earliest
	// and the tier-1 escalation, precisely the two that matter most. 15m keeps
	// headroom for the 5m sweep tick and clock skew while still collapsing a
	// genuine sub-quarter-hour burst of the same incident.
	perTemplateCooldownCritical = 15 * time.Minute
	maxNotifiesPerHour          = 6
	// Lower-severity traffic cannot consume critical delivery capacity.
	maxCriticalNotifiesPerHour = 12
	// Human interruptions are intentionally tighter than durable Signal
	// history; every incident is recorded, but lock-screen storms are bounded.
	maxHumanPushesPerHour    = 6
	maxTrackedHumanIncidents = 20000

	// maxReminderDeliveryRetries bounds how many consecutive sweep ticks may be
	// told "this reminder failed" for one incident. At the sweep's 5m cadence
	// that is ~30 minutes of honest retrying against a route that might be
	// transiently down. Past that the route is broken rather than blipping, and
	// hammering it every 5m for the rest of the day tells the operator nothing
	// new; the budget is released so the incident's backoff advances and the
	// policy schedules a fresh reminder (at most 4h later for a critical) which
	// gets its own full set of attempts.
	maxReminderDeliveryRetries = 6
	maxTrackedReminderFailures = 20000
)

// Store is the dispatcher's slice of store.Store.
type Store interface {
	ListMonitoringChannels(ctx context.Context, workspaceID string) ([]*store.MonitoringChannel, error)
	GetWorkspace(ctx context.Context, id string) (*store.Workspace, error)
}

// Sender delivers one rendered message over one channel kind.
type Sender interface {
	Send(ctx context.Context, ch *store.MonitoringChannel, severity, message string) error
}

// HumanPublisher durably records critical incidents and can synchronously
// confirm acceptance by the out-of-browser push route.
type HumanPublisher interface {
	PublishDurable(ctx context.Context, evt notify.Event, interrupt bool) error
}

// Result reports where one notification went.
type Result struct {
	Delivered  []string `json:"delivered"`
	Failed     []string `json:"failed"`
	Suppressed string   `json:"suppressed"`
}

// Dispatcher owns envelope, throttle, retry, and fan-out state.
type Dispatcher struct {
	store       Store
	senders     map[string]Sender
	human       HumanPublisher
	gatewayHost string
	publicURL   string

	mu             sync.Mutex
	perTemplate    map[string]notifyMark
	perHour        map[string][]notifyMark
	humanIncidents map[string]humanIncidentState
	humanPerHour   map[string][]time.Time
	// reminderFailures counts consecutive failed reminder deliveries per
	// incident, bounding retries against a route that stays broken.
	reminderFailures map[string]int
	// channelHealth tracks each route's consecutive-failure run across
	// notifications, so a broken channel stays visible through suppression.
	channelHealth map[string]channelHealth
	// channelHealthRecorder persists that run onto the channel row. Optional:
	// nil keeps the in-memory behaviour, which is what the dispatcher's own
	// tests and any embedder without a store get. See channel_health_persist.go.
	channelHealthRecorder ChannelHealthRecorder
	now                   func() time.Time
	retryPause            func(context.Context, time.Duration) error
}

func (d *Dispatcher) RegisterSender(kind string, sender Sender) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.senders[kind] = sender
}

func (d *Dispatcher) RegisterHumanPublisher(publisher HumanPublisher) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.human = publisher
}

func NewDispatcher(st Store, senders map[string]Sender) *Dispatcher {
	host, _ := os.Hostname()
	if host == "" {
		host = "mcplexer"
	}
	return &Dispatcher{
		store: st, senders: senders, gatewayHost: host,
		publicURL: normalizePublicURL(firstNonEmpty(
			os.Getenv("MCPLEXER_PUBLIC_URL"), os.Getenv("MCPLEXER_EXTERNAL_URL"))),
		perTemplate: map[string]notifyMark{}, perHour: map[string][]notifyMark{},
		humanIncidents:   map[string]humanIncidentState{},
		humanPerHour:     map[string][]time.Time{},
		reminderFailures: map[string]int{},
		channelHealth:    map[string]channelHealth{},
		now:              time.Now,
		retryPause:       pauseWithContext,
	}
}
