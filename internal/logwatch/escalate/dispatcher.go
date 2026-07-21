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
	maxNotifiesPerHour  = 6
	// Lower-severity traffic cannot consume critical delivery capacity.
	maxCriticalNotifiesPerHour = 12
	// Human interruptions are intentionally tighter than durable Signal
	// history; every incident is recorded, but lock-screen storms are bounded.
	maxHumanPushesPerHour    = 6
	maxTrackedHumanIncidents = 20000
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
	now            func() time.Time
	retryPause     func(context.Context, time.Duration) error
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
		humanIncidents: map[string]humanIncidentState{},
		humanPerHour:   map[string][]time.Time{},
		now:            time.Now,
		retryPause:     pauseWithContext,
	}
}
