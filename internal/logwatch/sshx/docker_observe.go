package sshx

import (
	"fmt"
	"time"
)

// DockerStateCommand returns the minimal container state needed to verify a
// lifecycle transition. It deliberately does not render the full inspect
// object: config/env/labels can contain secrets and are outside monitoring's
// evidence boundary.
func DockerStateCommand(selector string) (string, error) {
	sel, err := quoteSelector(selector)
	if err != nil {
		return "", err
	}
	return "docker inspect --type container --format " +
		"'{{.Id}}|{{.RestartCount}}|{{.State.StartedAt}}' " + sel, nil
}

// DockerRestartEventsCommand returns completed restart events in a bounded
// time range. --until is mandatory: docker events is otherwise a live stream.
func DockerRestartEventsCommand(selector string, since, until time.Time) (string, error) {
	if since.IsZero() || until.IsZero() || until.Before(since) {
		return "", fmt.Errorf("sshx: docker event range must be non-zero and ordered")
	}
	sel, err := quoteSelector(selector)
	if err != nil {
		return "", err
	}
	sinceArg, err := quoteToken("since", since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return "", err
	}
	untilArg, err := quoteToken("until", until.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return "", err
	}
	return "docker events --since " + sinceArg + " --until " + untilArg +
		" --filter 'type=container' --filter container=" + sel +
		" --filter 'event=restart' --format " +
		"'{{.TimeNano}}|{{.Action}}|{{.Actor.ID}}'", nil
}

// DockerPortInventoryCommand lists only running-container id, name, and port
// summaries. Unlike `docker ps --format json`, it cannot disclose labels or
// command strings while still covering containers that are not log sources.
func DockerPortInventoryCommand() string {
	return "docker ps --no-trunc --format '{{.ID}}|{{.Names}}|{{.Ports}}'"
}
