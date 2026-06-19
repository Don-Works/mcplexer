package downstream

import (
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestIsOnDemandOnlyServer(t *testing.T) {
	cases := []struct {
		name string
		srv  store.DownstreamServer
		want bool
	}{
		{
			name: "mcp remote",
			srv: store.DownstreamServer{
				Transport: "stdio",
				Command:   "npx",
				Args:      json.RawMessage(`["mcp-remote","https://mcp.vercel.com"]`),
			},
			want: true,
		},
		{
			name: "playwright package",
			srv: store.DownstreamServer{
				Transport: "stdio",
				Command:   "npx",
				Args:      json.RawMessage(`["-y","@playwright/mcp@latest","--headless","--isolated"]`),
			},
			want: true,
		},
		{
			name: "playwright cli",
			srv: store.DownstreamServer{
				Transport: "stdio",
				Command:   "playwright-mcp",
			},
			want: true,
		},
		{
			name: "regular stdio",
			srv: store.DownstreamServer{
				Transport: "stdio",
				Command:   "mcp-server-fetch",
			},
			want: false,
		},
		{
			name: "http server",
			srv: store.DownstreamServer{
				Transport: "http",
				Command:   "playwright-mcp",
			},
			want: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsOnDemandOnlyServer(c.srv); got != c.want {
				t.Fatalf("IsOnDemandOnlyServer() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestIsAutoStartUnsafeServer(t *testing.T) {
	cases := []struct {
		name string
		srv  store.DownstreamServer
		want bool
	}{
		{
			name: "regular stdio",
			srv:  store.DownstreamServer{Transport: "stdio", Command: "mcp-server-fetch"},
			want: true,
		},
		{
			name: "playwright stdio",
			srv: store.DownstreamServer{
				Transport: "stdio",
				Command:   "npx",
				Args:      json.RawMessage(`["-y","@playwright/mcp@latest"]`),
			},
			want: true,
		},
		{
			name: "http",
			srv:  store.DownstreamServer{Transport: "http", Command: "mcp-server-fetch"},
			want: false,
		},
		{
			name: "internal",
			srv:  store.DownstreamServer{Transport: "internal"},
			want: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsAutoStartUnsafeServer(c.srv); got != c.want {
				t.Fatalf("IsAutoStartUnsafeServer() = %v, want %v", got, c.want)
			}
		})
	}
}
