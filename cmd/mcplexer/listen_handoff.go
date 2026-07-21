package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	listenHandoffTimeout = 60 * time.Second
	listenRetryInitial   = 100 * time.Millisecond
	listenRetryMax       = time.Second
	unixProbeTimeout     = 150 * time.Millisecond
)

type listenHandoffConfig struct {
	Timeout      time.Duration
	InitialDelay time.Duration
	MaxDelay     time.Duration
	ProbeTimeout time.Duration
}

func defaultListenHandoffConfig() listenHandoffConfig {
	return listenHandoffConfig{
		Timeout:      listenHandoffTimeout,
		InitialDelay: listenRetryInitial,
		MaxDelay:     listenRetryMax,
		ProbeTimeout: unixProbeTimeout,
	}
}

func (c listenHandoffConfig) withDefaults() listenHandoffConfig {
	def := defaultListenHandoffConfig()
	if c.Timeout <= 0 {
		c.Timeout = def.Timeout
	}
	if c.InitialDelay <= 0 {
		c.InitialDelay = def.InitialDelay
	}
	if c.MaxDelay <= 0 {
		c.MaxDelay = def.MaxDelay
	}
	if c.ProbeTimeout <= 0 {
		c.ProbeTimeout = def.ProbeTimeout
	}
	if c.MaxDelay < c.InitialDelay {
		c.MaxDelay = c.InitialDelay
	}
	return c
}

func listenTCPWithHandoff(ctx context.Context, addr string, cfg listenHandoffConfig) (net.Listener, error) {
	cfg = cfg.withDefaults()
	waitCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	delay := cfg.InitialDelay
	started := time.Now()
	logged := false
	var lastErr error

	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			if logged {
				slog.Info("http address released; continuing startup",
					"addr", addr,
					"wait_ms", time.Since(started).Milliseconds())
			}
			return ln, nil
		}
		lastErr = err
		if !isAddrInUseError(err) {
			return nil, fmt.Errorf("listen tcp %s: %w", addr, err)
		}
		if !logged {
			slog.Info("http address in use; waiting for previous daemon to release it",
				"addr", addr,
				"timeout", cfg.Timeout.String())
			logged = true
		}
		if err := waitListenRetry(waitCtx, delay); err != nil {
			return nil, fmt.Errorf("listen tcp %s: %w; address still in use after %s", addr, lastErr, time.Since(started).Round(time.Millisecond))
		}
		delay = nextListenDelay(delay, cfg.MaxDelay)
	}
}

func listenUnixWithHandoff(ctx context.Context, path string, cfg listenHandoffConfig) (net.Listener, error) {
	if path == "" {
		return nil, fmt.Errorf("listen unix: empty socket path")
	}
	cfg = cfg.withDefaults()
	waitCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	delay := cfg.InitialDelay
	started := time.Now()
	logged := false
	var lastErr error

	for {
		ln, err := net.Listen("unix", path)
		if err == nil {
			if chmodErr := os.Chmod(path, 0600); chmodErr != nil {
				slog.Warn("chmod socket failed (continuing)", "path", path, "err", chmodErr)
			}
			if logged {
				slog.Info("unix socket released; continuing startup",
					"path", path,
					"wait_ms", time.Since(started).Milliseconds())
			}
			return ln, nil
		}
		lastErr = err
		if !isAddrInUseError(err) {
			return nil, fmt.Errorf("listen unix %s: %w", path, err)
		}

		live, probeErr := unixSocketHasListener(waitCtx, path, cfg.ProbeTimeout)
		if probeErr != nil {
			slog.Debug("unix socket ownership probe inconclusive; treating socket as live",
				"path", path,
				"err", probeErr)
		}
		if !live {
			if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
				return nil, fmt.Errorf("remove stale socket %s: %w", path, removeErr)
			}
			slog.Info("removed stale unix socket", "path", path)
			delay = cfg.InitialDelay
			continue
		}

		if !logged {
			slog.Info("unix socket in use; waiting for previous daemon to release it",
				"path", path,
				"timeout", cfg.Timeout.String())
			logged = true
		}
		if err := waitListenRetry(waitCtx, delay); err != nil {
			return nil, fmt.Errorf("listen unix %s: %w; socket still in use after %s", path, lastErr, time.Since(started).Round(time.Millisecond))
		}
		delay = nextListenDelay(delay, cfg.MaxDelay)
	}
}

func unixSocketHasListener(ctx context.Context, path string, timeout time.Duration) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return true, err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return false, nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(probeCtx, "unix", path)
	if err == nil {
		_ = conn.Close()
		return true, nil
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
		return false, nil
	}
	if probeCtx.Err() != nil {
		return true, probeCtx.Err()
	}
	return true, err
}

func waitListenRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nextListenDelay(delay, maxDelay time.Duration) time.Duration {
	delay *= 2
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

type handlerBox struct {
	h http.Handler
}

type swappableHTTPHandler struct {
	current atomic.Value
}

func newSwappableHTTPHandler(initial http.Handler) *swappableHTTPHandler {
	s := &swappableHTTPHandler{}
	s.Swap(initial)
	return s
}

func (s *swappableHTTPHandler) Swap(next http.Handler) {
	s.current.Store(&handlerBox{h: next})
}

func (s *swappableHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	box, _ := s.current.Load().(*handlerBox)
	if box == nil || box.h == nil {
		http.Error(w, "mcplexer daemon is starting", http.StatusServiceUnavailable)
		return
	}
	box.h.ServeHTTP(w, r)
}

func startingHTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", startingHealth)
	mux.HandleFunc("/healthz", startingHealth)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "mcplexer daemon is starting", http.StatusServiceUnavailable)
	})
	return mux
}

func startingHealth(w http.ResponseWriter, _ *http.Request) {
	resp := map[string]any{
		"status":         "starting",
		"readiness":      "starting",
		"version":        mcplexerVersion,
		"uptime_seconds": 0,
		"mode":           "http",
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(resp)
}
