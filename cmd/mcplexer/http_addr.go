package main

import (
	"net"
	"strings"
)

// httpURLFromAddr converts an HTTP listen address into a browser URL.
// Examples:
//
//	:8080            -> http://localhost:8080
//	127.0.0.1:8080   -> http://127.0.0.1:8080
//	[::1]:8080       -> http://[::1]:8080
func httpURLFromAddr(addr string) string {
	a := strings.TrimSpace(addr)
	if a == "" {
		return "http://localhost"
	}
	if strings.HasPrefix(a, "http://") || strings.HasPrefix(a, "https://") {
		return strings.TrimRight(a, "/")
	}

	host, port, err := net.SplitHostPort(a)
	if err == nil {
		if host == "" {
			host = "localhost"
		}
		return "http://" + net.JoinHostPort(host, port)
	}
	if strings.HasPrefix(a, ":") {
		return "http://localhost" + a
	}

	return "http://" + a
}
