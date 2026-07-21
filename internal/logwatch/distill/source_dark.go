package distill

import (
	"context"
	"errors"
	"fmt"

	"github.com/don-works/mcplexer/internal/logwatch/collect"
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
	reason collect.FailureReason,
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
	title, body := collectionFailureMessage(reason, hostName, source.Name, consecutiveFailures)
	return d.notifier.Notify(ctx, Notification{
		WorkspaceID:    source.WorkspaceID,
		Severity:       store.SeverityCritical,
		Title:          title,
		Body:           body,
		NewIncident:    true,
		RemoteHostName: hostName,
		RemoteHostAddr: host.SSHHost,
		SourceName:     source.Name,
		TemplateID:     "source-dark:" + source.ID + ":" + string(reason),
		IncidentID:     "source-dark:" + source.ID + ":" + episodeID,
	})
}

func collectionFailureMessage(
	reason collect.FailureReason, hostName, sourceName string, failures int,
) (string, string) {
	if reason == collect.FailureReasonHostKeyMismatch {
		return "SSH host identity changed on " + hostName + "/" + sourceName,
			"MCPlexer refused the connection because the presented SSH host key no longer matches the pinned identity. Verify the host out of band before explicitly re-pinning it."
	}
	return fmt.Sprintf("log collection unavailable on %s/%s", hostName, sourceName),
		fmt.Sprintf(
			"MCPlexer could not collect this source for %d consecutive scheduled pulls. Check host reachability, credentials, and the remote log-read boundary.",
			failures,
		)
}
