// Package runner — safe_url.go is the SSRF guard for the user-
// supplied URL fields on output channels. Webhooks and Slack incoming
// hooks both accept any URL; without this guard a worker could be
// pointed at 127.0.0.1, 169.254.169.254 (cloud metadata endpoints),
// private RFC1918 ranges, or local *.local / *.internal hostnames —
// every one of which lets a compromised operator probe the daemon's
// own host environment.
//
// ClickUp + GitHub channels are intentionally NOT routed through this
// guard: their URLs are fixed by channel type (api.clickup.com /
// api.github.com), so SSRF surface is much narrower. If we extend
// those channels to accept user-supplied API endpoints, route them
// here too.
package runner

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// isSafeOutboundURL validates rawurl as a public-internet HTTP(S)
// destination. Returns nil when safe, an error explaining the block
// otherwise. Designed to be cheap to call per emission — no DNS
// lookups beyond the implicit one already on the eventual Do().
//
// Block categories:
//   - non-http/https scheme
//   - loopback (127.0.0.0/8, ::1)
//   - link-local (169.254.0.0/16, fe80::/10)
//   - RFC1918 (10/8, 172.16/12, 192.168/16)
//   - unique local (fc00::/7)
//   - unspecified (0.0.0.0, ::)
//   - .local / .internal hostnames (mDNS / homelab convention)
//
// Hostnames that resolve to public IPs but happen to share a name with
// a blocked TLD (.local) are still rejected: an operator with a
// production host literally called example.local is making us choose
// between a bad SSRF and a niche legit case; the SSRF risk wins.
func isSafeOutboundURL(rawurl string) error {
	u, err := url.Parse(strings.TrimSpace(rawurl))
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if err := checkScheme(u); err != nil {
		return err
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("url has no host")
	}
	if err := checkHostname(host); err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip != nil {
		return checkIP(ip)
	}
	return nil
}

// checkScheme rejects non-http(s) URLs (file://, gopher://, ftp://, etc.).
func checkScheme(u *url.URL) error {
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("scheme %q not allowed (only http/https)", u.Scheme)
	}
}

// checkHostname rejects hostnames in the .local / .internal mDNS /
// homelab namespaces and explicit loopback aliases. Case-insensitive.
func checkHostname(host string) error {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" {
		return errors.New("loopback hostname not allowed (localhost)")
	}
	if strings.HasSuffix(h, ".local") {
		return errors.New(".local hostnames not allowed (mDNS / private network)")
	}
	if strings.HasSuffix(h, ".internal") {
		return errors.New(".internal hostnames not allowed (cloud internal)")
	}
	return nil
}

// checkIP rejects loopback / link-local / private / unspecified IPs.
// Both IPv4 and IPv6 ranges are checked via the standard net helpers
// plus a hand-rolled fc00::/7 ULA check (net.IP has no IsULA).
func checkIP(ip net.IP) error {
	if ip.IsLoopback() {
		return errors.New("loopback IP not allowed")
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return errors.New("link-local IP not allowed")
	}
	if ip.IsUnspecified() {
		return errors.New("unspecified IP (0.0.0.0 / ::) not allowed")
	}
	if ip.IsPrivate() {
		return errors.New("private (RFC1918 / ULA) IP not allowed")
	}
	return nil
}
