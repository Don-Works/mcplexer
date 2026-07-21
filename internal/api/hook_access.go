package api

import (
	"net"
	"net/http"
	"strings"
)

// loopbackHookMiddleware keeps the shell and session hook bridges local to
// the machine. Their installed curl shims always target 127.0.0.1, so a
// non-loopback caller is never legitimate even when the dashboard listener is
// deliberately exposed on a LAN or tailnet interface.
//
// Deliberately inspect RemoteAddr only. Forwarding headers are caller
// controlled unless a trusted-proxy boundary is configured, and these hook
// routes are not intended to be reverse proxied.
func loopbackHookMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackRemoteAddr(r.RemoteAddr) ||
			!isLoopbackHostname(requestHostname(r)) || hasForwardingHeaders(r) {
			writeError(w, http.StatusForbidden, "hook endpoint is loopback-only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hasForwardingHeaders(r *http.Request) bool {
	return r.Header.Get("Forwarded") != "" ||
		r.Header.Get("X-Forwarded-For") != "" ||
		r.Header.Get("X-Forwarded-Host") != "" ||
		r.Header.Get("X-Forwarded-Proto") != ""
}

func isLoopbackRemoteAddr(remoteAddr string) bool {
	host := strings.TrimSpace(remoteAddr)
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
