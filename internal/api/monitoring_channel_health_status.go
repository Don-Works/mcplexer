package api

// monitoring_channel_health_status.go — "is my alerting actually working?"
// answered in one call, without routing the answer through alerting.
//
// The channel endpoints expose per-route health, but an operator asking that
// question should not have to enumerate routes and reduce the answer
// themselves. This is also the deliberate NON-recursive escalation surface: a
// broken alert channel cannot be reliably announced through alert channels, so
// the product states it on a status endpoint the dashboard already polls
// instead of pretending a notification would arrive. See the comment on
// summarizeChannelHealth for why no incident is raised.

import (
	"context"

	"github.com/don-works/mcplexer/internal/store"
)

// channelHealthSummary is the reduced answer: counts by state plus the names
// of the routes that are actually broken, because "2 broken" without saying
// WHICH two is another thing the operator has to go and look up.
type channelHealthSummary struct {
	Total    int `json:"total"`
	Healthy  int `json:"healthy"`
	Degraded int `json:"degraded"`
	Broken   int `json:"broken"`
	Unknown  int `json:"unknown"`
	// BrokenNames is capped: a workspace with fifty dead routes has one
	// problem, not fifty, and the counts already carry the magnitude.
	BrokenNames []string `json:"broken_names,omitempty"`
	// AllBroken is the case with no path to the operator at all. It is
	// reported as a distinct fact rather than left to be inferred from
	// broken == total, because it is the state in which every notification
	// mechanism this product has is known to be failing, and the honest
	// thing is to say so plainly on a surface that does not depend on them.
	AllBroken bool `json:"all_broken"`
}

const maxBrokenNamesReported = 10

// summarizeChannelHealth reduces a workspace's routes to a health answer.
//
// It deliberately does NOT raise a monitoring incident for a broken route.
// Alerting about a dead alert channel through the alert channels is circular in
// the exact case that matters — the common causes are "the only warn+ route is
// dead" and "everything is dead" — and it is self-amplifying: the failed
// delivery of a "channel broken" incident is itself a channel failure, which
// re-raises it, which burns the workspace hourly notify cap, which then
// suppresses REAL incidents. That is the original six-day defect reproduced by
// a cleverer route, which is the outcome the throttle exists to prevent.
//
// So the escalation path is: the ERROR log line (unchanged), the channel row
// (queryable, survives restarts), and this summary (one call, no recursion).
// The limitation is stated rather than papered over — if every route is down,
// nothing this process does can reach the operator, and AllBroken is how the
// product says that out loud instead of implying a page is on its way.
func summarizeChannelHealth(
	ctx context.Context, st store.Store, workspaceID string,
) (*channelHealthSummary, error) {
	channels, err := st.ListMonitoringChannels(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := &channelHealthSummary{}
	for _, c := range channels {
		// Disabled routes are excluded entirely: an operator who turned a
		// channel off has not got a broken channel, and counting it would
		// make the summary cry wolf until they deleted the row.
		if !c.Enabled {
			continue
		}
		out.Total++
		switch c.HealthState() {
		case store.ChannelHealthHealthy:
			out.Healthy++
		case store.ChannelHealthDegraded:
			out.Degraded++
		case store.ChannelHealthBroken:
			out.Broken++
			if len(out.BrokenNames) < maxBrokenNamesReported {
				out.BrokenNames = append(out.BrokenNames, c.Name)
			}
		default:
			out.Unknown++
		}
	}
	out.AllBroken = out.Total > 0 && out.Broken == out.Total
	return out, nil
}
