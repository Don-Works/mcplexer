package collect

import (
	"context"
	"errors"
	"log/slog"
	"strconv"

	"github.com/don-works/mcplexer/internal/logwatch/sshx"
	"github.com/don-works/mcplexer/internal/store"
)

// HealthSink is the optional deterministic alarm path for collection
// failures. Distiller implements it; slim test sinks may omit it.
type HealthSink interface {
	NotifyCollectionFailure(
		ctx context.Context, src *store.LogSource, host *store.RemoteHost,
		consecutiveFailures int, episodeID string, reason FailureReason,
	) error
}

type FailureReason string

const (
	FailureReasonUnavailable     FailureReason = "collection_unavailable"
	FailureReasonHostKeyMismatch FailureReason = "host_key_mismatch"
)

type darkEpisode struct {
	id       string
	reason   FailureReason
	sent     bool
	inFlight bool
}

func (m *Manager) notifyCollectionFailure(
	ctx context.Context, src *store.LogSource, failures int, pullErr error,
) {
	health, ok := m.sink.(HealthSink)
	threshold, reason := failurePolicy(pullErr)
	if failures < threshold || !ok {
		return
	}
	episodeID, episodeReason, claimed := m.claimDarkEpisode(src.ID, reason)
	if !claimed {
		return
	}
	host, err := m.store.GetRemoteHost(ctx, src.RemoteHostID)
	if err != nil {
		host = &store.RemoteHost{ID: src.RemoteHostID, Name: src.RemoteHostID}
	}
	err = health.NotifyCollectionFailure(ctx, src, host, failures, episodeID, episodeReason)
	m.completeDarkAlert(src.ID, episodeID, err == nil)
	if err != nil {
		slog.Warn("logwatch: source-dark alert failed", "source", src.Name, "error", err)
	}
}

func failurePolicy(err error) (int, FailureReason) {
	var mismatch *sshx.HostKeyMismatchError
	if errors.As(err, &mismatch) {
		return 1, FailureReasonHostKeyMismatch
	}
	return 3, FailureReasonUnavailable
}

func (m *Manager) claimDarkEpisode(
	sourceID string, reason FailureReason,
) (string, FailureReason, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	episode, exists := m.dark[sourceID]
	if !exists {
		m.darkSeq++
		episode.id = strconv.FormatInt(m.now().UTC().UnixNano(), 36) + "-" +
			strconv.FormatUint(m.darkSeq, 36)
		episode.reason = reason
	}
	if episode.sent || episode.inFlight {
		return episode.id, episode.reason, false
	}
	episode.inFlight = true
	m.dark[sourceID] = episode
	return episode.id, episode.reason, true
}

func (m *Manager) completeDarkAlert(sourceID, episodeID string, sent bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	episode, exists := m.dark[sourceID]
	if !exists || episode.id != episodeID {
		return
	}
	episode.inFlight = false
	episode.sent = sent
	m.dark[sourceID] = episode
}

func (m *Manager) clearDarkEpisode(sourceID string) {
	m.mu.Lock()
	delete(m.dark, sourceID)
	m.mu.Unlock()
}
