package main

import (
	"net"
	"strings"
	"testing"
)

// TestLocalIPHostsExcludesLoopback pins the CORS-IP fix at the config layer:
// the machine's own non-loopback IPs are offered as trusted hosts (so a task
// link or bookmark that uses the box's IP passes the origin check), while
// loopback and link-local are never included (loopback is already allowed
// unconditionally; link-local is noise). Every entry is a canonical IP string.
func TestLocalIPHostsExcludesLoopback(t *testing.T) {
	for _, h := range localIPHosts() {
		ip := net.ParseIP(h)
		if ip == nil {
			t.Errorf("localIPHosts returned %q which is not a valid IP", h)
			continue
		}
		if ip.IsLoopback() {
			t.Errorf("localIPHosts leaked loopback address %q", h)
		}
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			t.Errorf("localIPHosts leaked link-local address %q", h)
		}
		if ip.String() != h {
			t.Errorf("localIPHosts returned non-canonical form %q (want %q)", h, ip.String())
		}
	}
}

// TestTrustedHostsMergeIncludesLocalIPs checks the wiring: local IPs are folded
// into the trusted-host union the CORS layer consumes.
func TestTrustedHostsMergeIncludesLocalIPs(t *testing.T) {
	ips := localIPHosts()
	if len(ips) == 0 {
		t.Skip("no non-loopback IPs on this host; nothing to assert")
	}
	merged := mergeTrustedHosts(localHostnames(), localIPHosts())
	set := make(map[string]struct{}, len(merged))
	for _, h := range merged {
		set[strings.ToLower(h)] = struct{}{}
	}
	for _, ip := range ips {
		if _, ok := set[strings.ToLower(ip)]; !ok {
			t.Errorf("merged trusted hosts missing local IP %q (%v)", ip, merged)
		}
	}
}
