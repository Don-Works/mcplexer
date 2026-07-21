package collect

import (
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestParseDockerRuntimeAndRestartEvents(t *testing.T) {
	runtime, err := parseDockerRuntime([]byte("abcdef012345|7|2026-07-16T10:02:03.123456789Z\n"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.ID != "abcdef012345" || runtime.RestartCount != 7 || runtime.StartedAt.Nanosecond() != 123456789 {
		t.Fatalf("runtime: %+v", runtime)
	}
	events, err := parseDockerRestartEvents([]byte("1784196123123456789|restart|abcdef012345\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ContainerID != "abcdef012345" || events[0].At.IsZero() {
		t.Fatalf("events: %+v", events)
	}
}

func TestParseDockerPortInventory_FlagsPublishedNonLoopbackOnly(t *testing.T) {
	out := strings.Join([]string{
		"aaa|admin-ui|0.0.0.0:8080->80/tcp, [::]:8080->80/tcp, 80/tcp",
		"bbb|database|127.0.0.1:5432->5432/tcp",
		"ccc|private-api|192.0.2.10:9443->443/tcp",
	}, "\n")
	exposures, err := parseDockerPortInventory([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(exposures) != 3 {
		t.Fatalf("exposures=%d: %+v", len(exposures), exposures)
	}
	for _, exposure := range exposures {
		if exposure.ContainerName == "database" {
			t.Fatalf("loopback binding was reported: %+v", exposure)
		}
	}
}

func TestPortExposureLines_AreHostScopedAndChangeTriggered(t *testing.T) {
	m := &Manager{hostPorts: map[string]string{}, now: func() time.Time {
		return time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	}}
	host := &store.RemoteHost{ID: "host-1"}
	obs := &DockerObservation{PortInventoryOK: true, PortExposures: []DockerPortExposure{{
		ContainerName: "admin-ui", HostIP: "0.0.0.0", HostPort: "8080",
		ContainerPort: "80", Protocol: "tcp",
	}}}
	first, persisted := m.portExposureLines(host, obs, "")
	if len(first) != 1 || !first[0].Notify ||
		!strings.Contains(first[0].Text, "external reachability not asserted") {
		t.Fatalf("first exposure: %+v", first)
	}
	if repeat, _ := m.portExposureLines(host, obs, persisted); len(repeat) != 0 {
		t.Fatalf("unchanged inventory repeated: %+v", repeat)
	}
	obs.PortExposures[0].HostPort = "9090"
	if changed, _ := m.portExposureLines(host, obs, persisted); len(changed) != 1 {
		t.Fatalf("changed inventory not surfaced: %+v", changed)
	}
}

func TestPortExposureLines_UsesPersistedStateAfterRestart(t *testing.T) {
	obs := &DockerObservation{PortInventoryOK: true, PortExposures: []DockerPortExposure{{
		ContainerName: "admin-ui", HostIP: "0.0.0.0", HostPort: "8080",
		ContainerPort: "80", Protocol: "tcp",
	}}}
	persisted := "ok:" + portFingerprint(obs.PortExposures)
	m := &Manager{hostPorts: map[string]string{}, now: time.Now}
	if lines, _ := m.portExposureLines(&store.RemoteHost{ID: "host-1"}, obs, persisted); len(lines) != 0 {
		t.Fatalf("unchanged persisted inventory re-alerted after restart: %+v", lines)
	}
	obs.PortExposures = nil
	if lines, _ := m.portExposureLines(&store.RemoteHost{ID: "host-1"}, obs, persisted); len(lines) != 1 || !strings.Contains(lines[0].Text, "no non-loopback") {
		t.Fatalf("closed exposure was not surfaced: %+v", lines)
	}
}
