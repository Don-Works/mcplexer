// mesh.go — `mcplexer mesh <subcommand>` CLI surface. Currently hosts
// `mesh wait`, an event-driven long-poll the agent backgrounds (via
// Bash run_in_background) so it can go DORMANT until a mesh message
// targets it. The command long-polls the daemon's /api/v1/mesh/wait
// endpoint; on a targeted match it prints the JSON body and EXITS,
// which wakes the agent. Across server-side timeouts (HTTP 204) it
// auto-reconnects, so ONE backgrounded command stays dormant
// indefinitely until a real message arrives.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// cmdMesh dispatches `mcplexer mesh <subcommand>`.
func cmdMesh(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: mcplexer mesh <wait> [args...]")
	}
	switch args[0] {
	case "wait":
		return cmdMeshWait(args[1:])
	default:
		return fmt.Errorf("unknown mesh subcommand: %s\nusage: mcplexer mesh <wait> [args...]", args[0])
	}
}

// meshWaitFlags holds the parsed flag state for `mcplexer mesh wait`.
type meshWaitFlags struct {
	agent            string
	role             string
	tags             string
	kind             string
	from             string
	includeRole      bool
	includeBroadcast bool
	consume          bool
	timeout          int
	once             bool
	httpAddr         string
}

// errMeshUsage signals a flag-parse / usage problem (exit 1 via run()).
var errMeshUsage = errors.New("usage: mcplexer mesh wait --agent <name> [--role r] [--tags csv] [--kind csv] [--from peer] [--include-role] [--include-broadcast] [--consume] [--timeout sec] [--once] [--json] [--http-addr addr]")

// parseMeshWaitFlags hand-rolls flag parsing in the repo's CLI style.
func parseMeshWaitFlags(args []string) (*meshWaitFlags, error) {
	f := &meshWaitFlags{timeout: 1800}
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s requires a value", a)
			}
			i++
			return args[i], nil
		}
		var err error
		switch a {
		case "--agent":
			f.agent, err = next()
		case "--role":
			f.role, err = next()
		case "--tags":
			f.tags, err = next()
		case "--kind":
			f.kind, err = next()
		case "--from":
			f.from, err = next()
		case "--include-role":
			f.includeRole = true
		case "--include-broadcast":
			f.includeBroadcast = true
		case "--consume":
			f.consume = true
		case "--once":
			f.once = true
		case "--json":
			// reserved; default output is already the raw 200 body.
		case "--http-addr":
			f.httpAddr, err = next()
		case "--timeout":
			var v string
			if v, err = next(); err == nil {
				f.timeout, err = strconv.Atoi(v)
			}
		default:
			return nil, fmt.Errorf("unknown flag: %s\n%v", a, errMeshUsage)
		}
		if err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(f.agent) == "" {
		return nil, errMeshUsage
	}
	return f, nil
}

// cmdMeshWait parses flags and runs the long-poll loop (or a single poll
// under --once). It installs a SIGINT/SIGTERM handler that cancels the
// context for a clean exit-0.
func cmdMeshWait(args []string) error {
	f, err := parseMeshWaitFlags(args)
	if err != nil {
		return err
	}
	endpoint, token, err := meshWaitEndpoint(f)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return runMeshWaitLoop(ctx, endpoint, token, f.once)
}

// meshWaitEndpoint builds the fully-qualified /api/v1/mesh/wait URL with
// its query string and reads the Bearer token. Base URL + token path are
// reused from loadConfig()/httpURLFromAddr() — the same auth + addr
// resolution the rest of the CLI uses (MCPLEXER_HTTP_ADDR, the api-key
// file at APITokenPath). --http-addr overrides the base addr.
func meshWaitEndpoint(f *meshWaitFlags) (string, string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return "", "", err
	}
	addr := cfg.HTTPAddr
	if strings.TrimSpace(f.httpAddr) != "" {
		addr = f.httpAddr
	}
	base := strings.TrimRight(httpURLFromAddr(addr), "/")

	tokenBytes, err := os.ReadFile(cfg.APITokenPath)
	if err != nil {
		return "", "", fmt.Errorf("read api key (%s): %w", cfg.APITokenPath, err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return "", "", fmt.Errorf("api key file empty: %s", cfg.APITokenPath)
	}

	q := url.Values{}
	q.Set("agent", f.agent)
	setIf := func(k, v string) {
		if strings.TrimSpace(v) != "" {
			q.Set(k, v)
		}
	}
	setIf("role", f.role)
	setIf("tags", f.tags)
	setIf("kind", f.kind)
	setIf("from", f.from)
	if f.includeRole {
		q.Set("include_role", "true")
	}
	if f.includeBroadcast {
		q.Set("include_broadcast", "true")
	}
	if f.consume {
		q.Set("consume", "true")
	}
	q.Set("timeout", strconv.Itoa(f.timeout))
	return base + "/api/v1/mesh/wait?" + q.Encode(), token, nil
}

// runMeshWaitLoop drives the long-poll. Under --once it issues a single
// GET; otherwise it loops: 200 → print + exit 0, 204 → re-poll
// immediately (dormant reconnect), connection error → sleep 2s + retry,
// 400 → exit 2, ctx cancelled → exit 0.
func runMeshWaitLoop(ctx context.Context, endpoint, token string, once bool) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		body, status, err := meshWaitPoll(ctx, endpoint, token)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if once {
				fmt.Fprintf(os.Stderr, "mesh wait: %v\n", err)
				os.Exit(1)
			}
			time.Sleep(2 * time.Second)
			continue
		}
		switch status {
		case http.StatusOK:
			fmt.Println(strings.TrimRight(body, "\n"))
			return nil
		case http.StatusNoContent:
			if once {
				fmt.Println(`{"timed_out":true}`)
				return nil
			}
			continue
		case http.StatusBadRequest:
			fmt.Fprintln(os.Stderr, strings.TrimSpace(body))
			os.Exit(2)
		default:
			if once {
				fmt.Fprintf(os.Stderr, "mesh wait: unexpected status %d: %s\n", status, strings.TrimSpace(body))
				os.Exit(1)
			}
			time.Sleep(2 * time.Second)
		}
	}
}

// meshWaitPoll issues one long-poll GET. It returns the response body,
// the HTTP status code, and any transport error. The request context is
// the cancellable parent — no per-request timeout, since the server holds
// the connection open for its own --timeout window.
func meshWaitPoll(ctx context.Context, endpoint, token string) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("daemon unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", resp.StatusCode, fmt.Errorf("read body: %w", err)
	}
	return string(b), resp.StatusCode, nil
}
