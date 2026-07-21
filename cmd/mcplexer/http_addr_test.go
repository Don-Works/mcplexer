package main

import "testing"

func TestHTTPURLFromAddr(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: "http://localhost"},
		{name: "port only", in: ":8080", want: "http://localhost:8080"},
		{name: "ipv4", in: "127.0.0.1:8080", want: "http://127.0.0.1:8080"},
		{name: "localhost", in: "localhost:3333", want: "http://localhost:3333"},
		{name: "ipv6", in: "[::1]:8080", want: "http://[::1]:8080"},
		{name: "already url", in: "http://127.0.0.1:8080", want: "http://127.0.0.1:8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := httpURLFromAddr(tt.in); got != tt.want {
				t.Fatalf("httpURLFromAddr(%q)=%q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
