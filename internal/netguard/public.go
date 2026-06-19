package netguard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const AllowPrivateFetchEnv = "MCPLEXER_ALLOW_PRIVATE_FETCH"

// NewPublicHTTPClient returns an HTTP client that refuses requests and
// redirects to non-public hosts. DNS results are validated and pinned into a
// custom DialContext so the transport cannot re-resolve a hostname to a private
// address between validation and dial (DNS rebinding / TOCTOU).
func NewPublicHTTPClient(timeout time.Duration) *http.Client {
	pinned := &pinnedResolver{}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = pinned.DialContext
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			pinnedCtx, err := PinPublicHosts(req.Context(), req.URL.Hostname())
			if err != nil {
				return fmt.Errorf("redirect: %w", err)
			}
			*req = *req.WithContext(pinnedCtx)
			return nil
		},
	}
}

// pinnedResolver implements a DialContext that resolves the host using a
// pre-validated set of public IPs stored in the request context by
// PinPublicHosts. If no pinned IPs are found on the context, it falls back
// to a fresh LookupIPAddr + public check so that every dial is safe even if
// the caller forgot to call AssertPublicHost first (defense-in-depth).
type pinnedResolver struct{}

func (p *pinnedResolver) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if allowPrivateFetch() {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("netguard: split host:port %q: %w", addr, err)
	}

	var ips []net.IPAddr
	if pinned := ctx.Value(pinnedIPsKey); pinned != nil {
		if typed, ok := pinned.([]net.IPAddr); ok && len(typed) > 0 {
			ips = typed
		}
	}

	if len(ips) == 0 {
		resolved, err := resolvePublicIPs(ctx, host)
		if err != nil {
			return nil, err
		}
		ips = resolved
	}

	var lastErr error
	for _, ip := range ips {
		dialAddr := net.JoinHostPort(ip.String(), port)
		var d net.Dialer
		conn, err := d.DialContext(ctx, network, dialAddr)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("netguard: no addresses for %q", host)
}

type contextKey struct{}

var pinnedIPsKey = contextKey{}

// PinPublicHosts validates that host resolves only to public IPs and returns
// a new context carrying those validated IPs. The pinned IPs are used by
// NewPublicHTTPClient's custom DialContext so the Transport cannot re-resolve
// to a different (private) address.
func PinPublicHosts(ctx context.Context, host string) (context.Context, error) {
	if allowPrivateFetch() {
		return ctx, nil
	}
	addrs, err := resolvePublicIPs(ctx, host)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, pinnedIPsKey, addrs), nil
}

// AssertPublicHost rejects host strings that resolve to loopback,
// link-local, or private IPs. Both the literal IP form and DNS names are
// checked. Set MCPLEXER_ALLOW_PRIVATE_FETCH=1 to bypass during tests.
func AssertPublicHost(ctx context.Context, host string) error {
	_, err := resolvePublicIPs(ctx, host)
	return err
}

func resolvePublicIPs(ctx context.Context, host string) ([]net.IPAddr, error) {
	if host == "" {
		return nil, fmt.Errorf("missing host")
	}
	if allowPrivateFetch() {
		return nil, nil
	}
	if strings.EqualFold(host, "localhost") {
		return nil, fmt.Errorf("host %q is not a public address", host)
	}
	resolver := net.DefaultResolver
	addrs, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("resolve %q: no addresses", host)
	}
	for _, a := range addrs {
		if !IsPublicIP(a.IP) {
			return nil, fmt.Errorf("host %q resolved to non-public address %s", host, a.IP)
		}
	}
	return addrs, nil
}

// IsPublicIP reports whether the IP is routable over the public internet.
// Loopback (127.0.0.0/8, ::1), link-local (169.254/16, fe80::/10), private
// RFC1918 (10/8, 172.16/12, 192.168/16), CGNAT (100.64/10), and ULA
// (fc00::/7) are all rejected.
func IsPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() ||
		ip.IsPrivate() {
		return false
	}
	// CGNAT (RFC6598) is not covered by net.IP.IsPrivate.
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return false
		}
	}
	return true
}

func allowPrivateFetch() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(AllowPrivateFetchEnv))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
