package collect

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strings"
	"unicode"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/store"
)

type DockerPortExposure struct {
	ContainerID   string
	ContainerName string
	HostIP        string
	HostPort      string
	ContainerPort string
	Protocol      string
}

func parseDockerPortInventory(out []byte) ([]DockerPortExposure, error) {
	var exposures []DockerPortExposure
	for raw := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		parts := strings.SplitN(raw, "|", 3)
		if len(parts) != 3 || strings.TrimSpace(parts[0]) == "" {
			return nil, fmt.Errorf("docker ports: invalid inventory record")
		}
		for _, item := range strings.Split(parts[2], ",") {
			exposure, ok := parsePublishedPort(parts[0], parts[1], item)
			if ok && exposedBind(exposure.HostIP) {
				exposures = append(exposures, exposure)
			}
		}
	}
	sortPortExposures(exposures)
	return exposures, nil
}

func parsePublishedPort(id, name, raw string) (DockerPortExposure, bool) {
	left, right, ok := strings.Cut(strings.TrimSpace(raw), "->")
	if !ok {
		return DockerPortExposure{}, false
	}
	slash := strings.LastIndex(right, "/")
	colon := strings.LastIndex(left, ":")
	if slash <= 0 || colon < 0 || colon == len(left)-1 {
		return DockerPortExposure{}, false
	}
	hostIP := strings.Trim(strings.TrimSpace(left[:colon]), "[]")
	return DockerPortExposure{
		ContainerID: strings.TrimSpace(id), ContainerName: strings.TrimSpace(name),
		HostIP: hostIP, HostPort: strings.TrimSpace(left[colon+1:]),
		ContainerPort: strings.TrimSpace(right[:slash]),
		Protocol:      strings.ToLower(strings.TrimSpace(right[slash+1:])),
	}, true
}

func exposedBind(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" || host == "0.0.0.0" || host == "::" {
		return true
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

func sortPortExposures(exposures []DockerPortExposure) {
	sort.Slice(exposures, func(i, j int) bool {
		a, b := exposures[i], exposures[j]
		return fmt.Sprint(a.ContainerName, a.HostIP, a.HostPort, a.ContainerPort, a.Protocol) <
			fmt.Sprint(b.ContainerName, b.HostIP, b.HostPort, b.ContainerPort, b.Protocol)
	})
}

func portFingerprint(exposures []DockerPortExposure) string {
	var b strings.Builder
	for _, p := range exposures {
		fmt.Fprintf(&b, "%s\x00%s\x00%s\x00%s\x00%s\n",
			p.ContainerName, p.HostIP, p.HostPort, p.ContainerPort, p.Protocol)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

func (m *Manager) portExposureLines(
	host *store.RemoteHost, obs *DockerObservation, persisted string,
) ([]Line, string) {
	state := "unavailable"
	if obs.PortInventoryOK {
		state = "ok:" + portFingerprint(obs.PortExposures)
	}
	m.mu.Lock()
	previous := persisted
	if inMemory, seen := m.hostPorts[host.ID]; seen {
		previous = inMemory
	}
	m.hostPorts[host.ID] = state
	m.mu.Unlock()
	if previous == state {
		return nil, state
	}
	now := m.now().UTC()
	if !obs.PortInventoryOK {
		return []Line{{TS: now, Notify: true,
			IncidentID: "port-inventory-unavailable:" + host.ID,
			Text:       "logwatch: port exposure check unavailable — docker port inventory could not be read; exposure state unverified"}}, state
	}
	if len(obs.PortExposures) == 0 {
		if previous == "" {
			return nil, state
		}
		return []Line{{TS: now, Notify: true,
			IncidentID: "port-exposure:" + host.ID + ":" + portFingerprint(obs.PortExposures),
			Text:       "logwatch: published port exposure inventory changed — no non-loopback Docker bindings observed at check time"}}, state
	}
	fingerprint := portFingerprint(obs.PortExposures)
	lines := make([]Line, 0, len(obs.PortExposures))
	for _, p := range obs.PortExposures {
		// These fields come from `docker ps` on the remote host, so strip
		// control chars/newlines (a hostile container name must not inject
		// lines into the notification) and run the composed text through the
		// same redaction pass as pulled log lines.
		text := fmt.Sprintf("logwatch: published port exposure observed — container=%s bind_address=%s host_port=%s container_port=%s/%s; external reachability not asserted",
			sanitizeRemoteField(p.ContainerName), sanitizeRemoteField(p.HostIP),
			sanitizeRemoteField(p.HostPort), sanitizeRemoteField(p.ContainerPort),
			sanitizeRemoteField(p.Protocol))
		lines = append(lines, Line{TS: now, Notify: true,
			IncidentID: "port-exposure:" + host.ID + ":" + fingerprint,
			Text:       audit.RedactString(text, nil)})
	}
	return lines, state
}

// sanitizeRemoteField strips control characters (incl. newlines) from a
// remote-derived docker field and bounds its length, so it can be embedded in
// a single-line notification without injecting lines or terminal escapes.
func sanitizeRemoteField(s string) string {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if len(cleaned) > 200 {
		cleaned = cleaned[:200]
	}
	return cleaned
}
