package distill

import (
	"context"
	"errors"
	"fmt"

	"github.com/don-works/mcplexer/internal/store"
)

// NotifyCollectionFailure implements collect.HealthSink. The first two pull
// failures remain operational noise; Manager calls this from the third failure
// onward and retries the same episode until a route accepts it.
func (d *Distiller) NotifyCollectionFailure(
	ctx context.Context,
	source *store.LogSource,
	host *store.RemoteHost,
	consecutiveFailures int,
	episodeID string,
) error {
	if d.notifier == nil {
		return errors.New("distill: source-dark notifier is not configured")
	}
	if source == nil {
		return errors.New("distill: source-dark source is nil")
	}
	if host == nil {
		host = &store.RemoteHost{}
	}
	hostName := host.Name
	if hostName == "" {
		hostName = source.RemoteHostID
	}
	return d.notifier.Notify(ctx, Notification{
		WorkspaceID: source.WorkspaceID,
		Severity:    store.SeverityCritical,
		Title:       fmt.Sprintf("log collection unavailable on %s/%s", hostName, source.Name),
		Body: fmt.Sprintf(
			"MCPlexer could not collect this source for %d consecutive scheduled pulls. Check host reachability, credentials, and the remote log-read boundary.",
			consecutiveFailures,
		),
		NewIncident:    true,
		RemoteHostName: hostName,
		RemoteHostAddr: host.SSHHost,
		SourceName:     source.Name,
		TemplateID:     "source-dark:" + source.ID + ":" + episodeID,
	})
}
