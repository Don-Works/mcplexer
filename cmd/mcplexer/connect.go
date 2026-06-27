package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/don-works/mcplexer/internal/install"
)

// cmdConnect bridges stdin/stdout to the MCPlexer daemon's local IPC endpoint.
func cmdConnect(args []string) error {
	var socketPath string
	for _, arg := range args {
		if len(arg) > 9 && arg[:9] == "--socket=" {
			socketPath = arg[9:]
		}
	}
	if socketPath == "" {
		socketPath = os.Getenv("MCPLEXER_SOCKET_PATH")
	}
	if socketPath == "" {
		socketPath = install.DefaultSocketPath()
	}
	return connectDirect(socketPath)
}

// connectDirect dials the local IPC endpoint and bridges stdin/stdout to it.
// On disconnect it automatically reconnects, replaying the MCP init
// handshake so the client never sees the interruption. Signal handling
// is owned here so direct CLI use (`mcplexer connect`) gets SIGINT/SIGTERM
// semantics; the testable bridgeStdioToSocket helper below takes a
// caller-supplied ctx and reader/writer pair instead so unit tests can
// exercise the full pipeline without process spawning.
func connectDirect(socketPath string) error {
	ctx, cancel := signal.NotifyContext(
		context.Background(), syscall.SIGINT, syscall.SIGTERM,
	)
	defer cancel()
	return bridgeStdioToSocket(ctx, os.Stdin, os.Stdout, socketPath)
}

// bridgeStdioToSocket is the transport-agnostic core of connectDirect.
// Pumps `in` into the socket as MCP JSON-RPC frames and writes everything
// the socket sends back to `out`. Reconnects on socket drop, replays a
// previously observed `initialize` + `notifications/initialized` handshake
// (captured during the first connection) so the client never sees the gap.
//
// The capture is tolerant: we snoop for the initialize request as it flows
// through the first live bridge rather than requiring it to be the absolute
// first message the client writes before we even dial. This makes the shim
// compatible with a wider range of MCP clients (some send pings, probes or
// other frames around the official initialize).
//
// Returns nil on clean stdin EOF, ctx cancellation, or successful run.
// Returns a non-nil error only when the underlying dial keeps failing
// past ctx (cf. dialWithBackoff).
func bridgeStdioToSocket(ctx context.Context, in io.Reader, out io.Writer, socketPath string) error {
	cwd := os.Getenv("MCPLEXER_CLIENT_CWD")
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	// Pump stdin into a channel — single reader for the process lifetime.
	stdinCh := make(chan []byte, 64)
	go readLines(in, stdinCh)

	// out may be written from multiple goroutines (the init response is
	// emitted from this goroutine; bridgeSession writes responses from
	// its socket→out copy goroutine). Serialise to keep frames intact.
	outMu := &sync.Mutex{}
	safeOut := lockedWriter{mu: outMu, w: out}

	var initReq []byte   // captured "initialize" request (with roots injected) for replay on reconnect
	var initNotif []byte // captured "notifications/initialized" for replay on reconnect
	firstConnect := true

	for {
		if ctx.Err() != nil {
			return nil
		}

		conn, err := dialWithBackoff(ctx, socketPath)
		if err != nil {
			return err
		}

		connBuf := bufio.NewReaderSize(conn, 1024*1024)

		doReplay := !firstConnect && initReq != nil
		if doReplay {
			// Replay the captured initialize + initialized so the daemon
			// sees a fresh handshake for this new socket. The client already
			// received its initialize result on the first connect.
			if _, err := conn.Write(initReq); err != nil {
				_ = conn.Close()
				continue
			}
			// Consume (and discard) the fresh initialize response.
			if _, err := connBuf.ReadBytes('\n'); err != nil {
				_ = conn.Close()
				continue
			}
			if initNotif != nil {
				_, _ = conn.Write(initNotif)
			}
		}
		// On the very first connection we do *not* replay: we just bridge
		// and let the real initialize flow through. The bridge snoops the
		// client's initialize (and later initialized) so we have them for
		// any future reconnect. This removes the old assumption that the
		// absolute first message on stdin must be "initialize".

		// Bridge until the socket drops or ctx is cancelled. The bridge
		// will populate initReq/initNotif (with roots injection) the first
		// time it sees the real handshake from the client.
		bridgeErr := bridgeSessionOut(ctx, stdinCh, connBuf, conn, &initReq, &initNotif, &safeOut, cwd)
		_ = conn.Close()
		firstConnect = false

		if ctx.Err() != nil {
			return nil
		}
		if bridgeErr == errStdinClosed {
			return nil
		}

		// Reconnect notices go to stderr so stdout remains valid JSON-RPC.
		// dialWithBackoff retries quickly first, then caps at 2s.
		fmt.Fprintf(os.Stderr,
			"mcplexer: daemon restarting; reconnecting (init handshake will replay automatically)...\n")
	}
}

// lockedWriter serialises writes to a shared io.Writer (typically
// os.Stdout) so concurrent goroutines emitting full JSON-RPC frames
// don't interleave bytes.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

var errStdinClosed = fmt.Errorf("stdin closed")

// readLines reads newline-delimited messages from r into ch. Closes ch
// when r reaches EOF. Replaces the old readStdinLines, which hard-coded
// os.Stdin and blocked testing the connect.go transport core without a
// process boundary.
func readLines(r io.Reader, ch chan<- []byte) {
	defer close(ch)

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		msg := make([]byte, len(line))
		copy(msg, line)
		ch <- msg
	}
}

// dialWithBackoff retries connecting to the Unix socket with a fast initial
// ramp (20ms → 50 → 100 → 200 → 500 → 1s) then caps at 2s. Tight early
// retries matter because Claude Code's MCP manager gives up on a server that
// takes too long to come back after a daemon restart, and we want to be
// re-connected before it flags us failed.
func dialWithBackoff(ctx context.Context, socketPath string) (net.Conn, error) {
	schedule := []time.Duration{
		20 * time.Millisecond,
		50 * time.Millisecond,
		100 * time.Millisecond,
		200 * time.Millisecond,
		500 * time.Millisecond,
		1 * time.Second,
	}
	const maxBackoff = 2 * time.Second

	step := 0
	for {
		conn, err := dialLocalIPCContext(ctx, socketPath)
		if err == nil {
			return conn, nil
		}

		var wait time.Duration
		if step < len(schedule) {
			wait = schedule[step]
		} else {
			wait = maxBackoff
		}
		step++

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// bridgeSessionOut forwards messages between the stdin channel and
// socket until the socket disconnects, stdin closes, or ctx is cancelled.
// It opportunistically captures the first "initialize" request it sees
// (injecting roots) and the "notifications/initialized" for future reconnect
// replay. This lets the bridge work even when initialize is not the very
// first message the client sends.
// The destination writer is explicit (rather than hard-coded to
// os.Stdout) so the bridge can be exercised end-to-end from tests.
func bridgeSessionOut(
	ctx context.Context,
	stdinCh <-chan []byte,
	connBuf *bufio.Reader,
	conn net.Conn,
	initReq *[]byte,
	initNotif *[]byte,
	out io.Writer,
	cwd string,
) error {
	readDone := make(chan error, 1)
	sessionDone := make(chan struct{})

	// socket → out
	go func() {
		_, err := io.Copy(out, connBuf)
		readDone <- err
	}()

	// stdinCh → socket
	writerExited := make(chan error, 1)
	go func() {
		for {
			select {
			case <-sessionDone:
				writerExited <- nil
				return
			case msg, ok := <-stdinCh:
				if !ok {
					if uc, ok := conn.(*net.UnixConn); ok {
						_ = uc.CloseWrite()
					}
					writerExited <- errStdinClosed
					return
				}
				// Capture the first initialize request we see (for reconnect replay).
				// We inject roots here so the saved copy (and the live send on this
				// connection) carries the workspace root the same way the old pre-dial
				// path did. This also means initialize no longer has to be the very
				// first message the client ever sends.
				if *initReq == nil && isInitializeRequest(msg) {
					injected := append(maybeInjectRoots(msg, cwd), '\n')
					saved := make([]byte, len(injected))
					copy(saved, injected)
					*initReq = saved
					// Send the roots-injected version for this live connection.
					if _, err := conn.Write(injected); err != nil {
						writerExited <- err
						return
					}
					continue
				}
				if *initNotif == nil && isInitializedNotif(msg) {
					saved := make([]byte, len(msg)+1)
					copy(saved, msg)
					saved[len(saved)-1] = '\n'
					*initNotif = saved
				}
				data := make([]byte, len(msg)+1)
				copy(data, msg)
				data[len(data)-1] = '\n'
				if _, err := conn.Write(data); err != nil {
					writerExited <- err
					return
				}
			}
		}
	}()

	var result error
	select {
	case <-ctx.Done():
		result = ctx.Err()
	case err := <-readDone:
		// Socket dropped. Signal writer to stop.
		close(sessionDone)
		<-writerExited
		result = err
	case err := <-writerExited:
		result = err
		// On clean stdin EOF the writer has half-closed the socket; the
		// server can still send a final response. Wait briefly for the
		// reader to drain so the final message reaches stdout instead of
		// being cut off by the outer conn.Close().
		if err == errStdinClosed {
			select {
			case <-readDone:
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
			}
		}
	}
	return result
}

// isInitializedNotif returns true if msg is a "notifications/initialized"
// JSON-RPC notification — the final step of the MCP init handshake.
func isInitializedNotif(msg []byte) bool {
	var rpc struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(msg, &rpc); err != nil {
		return false
	}
	return rpc.Method == "notifications/initialized"
}

// isInitializeRequest returns true if msg is an "initialize" JSON-RPC request.
// Used by the stdio bridge to opportunistically capture the handshake for
// reconnect replay without assuming it is the very first message on stdin.
func isInitializeRequest(msg []byte) bool {
	var rpc struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(msg, &rpc); err != nil {
		return false
	}
	return rpc.Method == "initialize"
}

// maybeInjectRoots parses a JSON-RPC line; if it is an "initialize"
// request without roots, it injects [{"uri":"file://<cwd>"}].
// Returns the original line unchanged on any error or non-initialize.
func maybeInjectRoots(line []byte, cwd string) []byte {
	if cwd == "" {
		return line
	}

	var msg struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id,omitempty"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}
	if err := json.Unmarshal(line, &msg); err != nil || msg.Method != "initialize" {
		return line
	}

	var params map[string]json.RawMessage
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return line
	}

	// Don't overwrite existing roots.
	if _, ok := params["roots"]; ok {
		return line
	}

	root := map[string]string{"uri": "file://" + cwd}
	rootsJSON, err := json.Marshal([]map[string]string{root})
	if err != nil {
		return line
	}
	params["roots"] = rootsJSON

	msg.Params, err = json.Marshal(params)
	if err != nil {
		return line
	}

	out, err := json.Marshal(msg)
	if err != nil {
		return line
	}
	return out
}
