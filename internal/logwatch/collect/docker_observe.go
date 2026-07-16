package collect

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/sshx"
	"github.com/don-works/mcplexer/internal/store"
)

const dockerObservationMaxBytes = 256 * 1024

type PullResult struct {
	sshx.Result
	Docker *DockerObservation
}
type DockerObservation struct {
	Runtime         DockerRuntime
	RuntimeOK       bool
	RestartEvents   []DockerRestartEvent
	EventsAttempted bool
	EventsOK        bool
	CheckedThrough  time.Time
	PortExposures   []DockerPortExposure
	PortInventoryOK bool
}
type DockerRuntime struct {
	ID           string
	RestartCount int
	StartedAt    time.Time
}
type DockerRestartEvent struct {
	At          time.Time
	ContainerID string
}

func collectDockerObservation(
	ctx context.Context, client *sshx.Client, src *store.LogSource, logSince time.Time,
) *DockerObservation {
	if src.Kind == store.LogSourceKindJournald {
		return nil
	}
	obs := &DockerObservation{}
	if result, err := client.Run(ctx, sshx.DockerPortInventoryCommand(), dockerObservationMaxBytes); err == nil && !result.Truncated {
		obs.PortExposures, err = parseDockerPortInventory(result.Stdout)
		obs.PortInventoryOK = err == nil
	}
	if src.Kind != store.LogSourceKindDocker {
		return obs
	}
	if command, err := sshx.DockerStateCommand(src.Selector); err == nil {
		if result, runErr := client.Run(ctx, command, dockerObservationMaxBytes); runErr == nil && !result.Truncated {
			obs.Runtime, runErr = parseDockerRuntime(result.Stdout)
			obs.RuntimeOK = runErr == nil
		}
	}
	state := decodeCursorState(src.CursorHash)
	eventSince := state.eventSince(logSince)
	if state.EventsSince != "" {
		// Docker's --since boundary is inclusive. Advance the persisted event
		// watermark minimally so the same restart is not replayed every pull.
		eventSince = eventSince.Add(time.Nanosecond)
	}
	checkedThrough := time.Now().UTC()
	if eventSince.IsZero() || checkedThrough.Before(eventSince) {
		return obs
	}
	obs.EventsAttempted = true
	command, err := sshx.DockerRestartEventsCommand(src.Selector, eventSince, checkedThrough)
	if err != nil {
		return obs
	}
	result, err := client.Run(ctx, command, dockerObservationMaxBytes)
	if err != nil || result.Truncated {
		return obs
	}
	obs.RestartEvents, err = parseDockerRestartEvents(result.Stdout)
	obs.EventsOK = err == nil
	if obs.EventsOK {
		obs.CheckedThrough = checkedThrough
	}
	return obs
}

func parseDockerRuntime(out []byte) (DockerRuntime, error) {
	parts := strings.Split(strings.TrimSpace(string(out)), "|")
	if len(parts) != 3 || strings.TrimSpace(parts[0]) == "" {
		return DockerRuntime{}, fmt.Errorf("docker state: expected id|restart_count|started_at")
	}
	restarts, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || restarts < 0 {
		return DockerRuntime{}, fmt.Errorf("docker state: invalid restart count")
	}
	started, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(parts[2]))
	if err != nil {
		return DockerRuntime{}, fmt.Errorf("docker state: invalid started_at")
	}
	return DockerRuntime{ID: strings.TrimSpace(parts[0]), RestartCount: restarts, StartedAt: started.UTC()}, nil
}

func parseDockerRestartEvents(out []byte) ([]DockerRestartEvent, error) {
	var events []DockerRestartEvent
	for raw := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		parts := strings.Split(raw, "|")
		if len(parts) != 3 || strings.TrimSpace(parts[1]) != "restart" {
			return nil, fmt.Errorf("docker events: invalid restart record")
		}
		nanos, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("docker events: invalid timestamp")
		}
		events = append(events, DockerRestartEvent{
			At: time.Unix(0, nanos).UTC(), ContainerID: strings.TrimSpace(parts[2]),
		})
	}
	return events, nil
}

func (m *Manager) lifecycleLines(
	src *store.LogSource, previous sourceCursorState, obs *DockerObservation,
) ([]Line, sourceCursorState) {
	next := previous
	if obs.EventsOK {
		next.EventsSince = obs.CheckedThrough.UTC().Format(time.RFC3339Nano)
	}
	lines := lifecycleEvidence(src, previous, obs, m.now().UTC())
	if obs.RuntimeOK {
		next.RuntimeSeen = true
		next.RuntimeID = obs.Runtime.ID
		next.RestartCount = obs.Runtime.RestartCount
		next.StartedAt = obs.Runtime.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	return lines, next
}

func lifecycleEvidence(
	src *store.LogSource, previous sourceCursorState, obs *DockerObservation, now time.Time,
) []Line {
	if len(obs.RestartEvents) > 0 {
		last := obs.RestartEvents[len(obs.RestartEvents)-1]
		return []Line{verifiedRestartLine(src, obs, last.At, len(obs.RestartEvents))}
	}
	if !previous.RuntimeSeen || !obs.RuntimeOK {
		return nil
	}
	if previous.RuntimeID != obs.Runtime.ID {
		return []Line{{TS: now, Notify: true,
			IncidentID: "docker-replacement:" + src.ID + ":" + obs.Runtime.ID,
			Text: fmt.Sprintf("logwatch: docker container replacement observed — runtime identity changed from %s to %s; restart not inferred",
				previous.RuntimeID, obs.Runtime.ID)}}
	}
	if obs.Runtime.RestartCount > previous.RestartCount {
		return []Line{verifiedRestartLine(src, obs, now,
			obs.Runtime.RestartCount-previous.RestartCount)}
	}
	if !previous.startedAt().IsZero() && !previous.startedAt().Equal(obs.Runtime.StartedAt) {
		return []Line{{TS: now, Notify: true,
			IncidentID: "docker-start-transition:" + src.ID + ":" + obs.Runtime.StartedAt.Format(time.RFC3339Nano),
			Text: fmt.Sprintf("logwatch: docker start transition observed — StartedAt changed from %s to %s while RestartCount remained %d; restart not asserted",
				previous.StartedAt, obs.Runtime.StartedAt.Format(time.RFC3339Nano), obs.Runtime.RestartCount)}}
	}
	return nil
}

func verifiedRestartLine(
	src *store.LogSource, obs *DockerObservation, at time.Time, eventCount int,
) Line {
	restarts := -1
	if obs.RuntimeOK {
		restarts = obs.Runtime.RestartCount
	}
	return Line{TS: at.UTC(), Notify: true,
		IncidentID: fmt.Sprintf("docker-restart:%s:%d:%d", src.ID, at.UnixNano(), restarts),
		Text: fmt.Sprintf("logwatch: docker restart verified — restart_events=%d RestartCount=%d",
			eventCount, restarts)}
}
