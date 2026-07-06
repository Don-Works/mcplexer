package downstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/don-works/mcplexer/internal/sandbox"
)

// ErrResponseDesync is returned (wrapped) by readResponse when a JSON-RPC
// response arrives with an id that does not match the request we just
// wrote. This signals the stdio stream has drifted out of lock-step —
// typically a prior request's late response surfacing after its caller
// timed out. The instance must be evicted rather than reused: a desynced
// stream would otherwise answer each caller with a different request's
// result (a cross-call / cross-scope data leak).
var ErrResponseDesync = errors.New("downstream response id mismatch (stream desync)")

// Instance manages a single downstream MCP server process.
type Instance struct {
	key     InstanceKey
	command string
	args    []string
	env     []string

	idleTimeout time.Duration
	idleTimer   *time.Timer

	onNotify func(method string, params json.RawMessage) // called when downstream sends a notification

	wrapper        *sandbox.CommandWrapper
	wrapperCleanup func()

	restartPolicy string // "never" | "on-failure" | "always"

	restartAttempt int
	restartWait    chan struct{}

	mu    sync.Mutex
	state InstanceState
	cmd   *exec.Cmd
	stdin io.WriteCloser
	queue *requestQueue
	reqID atomic.Int64

	cancel context.CancelFunc
	done   chan struct{}
}

var (
	MinRestartBackoff = 10 * time.Second
	MaxRestartBackoff = 5 * time.Minute
)

// newInstance creates a new stopped instance. wrapper may be nil to
// disable sandboxing for this instance.
func newInstance(
	key InstanceKey, command string, args, env []string,
	idleTimeout time.Duration, wrapper *sandbox.CommandWrapper,
	restartPolicy string,
) *Instance {
	return &Instance{
		key:           key,
		command:       command,
		args:          args,
		env:           env,
		idleTimeout:   idleTimeout,
		state:         StateStopped,
		done:          make(chan struct{}),
		queue:         newRequestQueue(64),
		wrapper:       wrapper,
		restartPolicy: restartPolicy,
	}
}

func (inst *Instance) start(ctx context.Context) error {
	inst.mu.Lock()
	defer inst.mu.Unlock()

	if inst.state != StateStopped && inst.state != StateRestarting {
		return fmt.Errorf("cannot start instance in state %s", inst.state)
	}
	if inst.state == StateRestarting {
		inst.queue = newRequestQueue(64)
	}
	inst.state = StateStarting

	if err := ValidateCommand(inst.command, inst.args); err != nil {
		inst.state = StateStopped
		return fmt.Errorf("downstream %s: %w", inst.key.ServerID, err)
	}

	childCtx, cancel := context.WithCancel(ctx)
	inst.cancel = cancel

	// Resolve the command to an absolute path using the augmented PATH
	// (not the daemon's minimal launchd PATH). Go's exec.Command uses
	// os.Getenv("PATH") for LookPath, which may not include directories
	// like /opt/homebrew/bin that we add via MergeEnv/augmentPath.
	cmdPath := inst.command
	if !filepath.IsAbs(cmdPath) {
		if resolved, err := lookPathInEnv(cmdPath, inst.env); err == nil {
			cmdPath = resolved
		}
	}

	// Sandbox wrap (M2 real impl). When wrapper is nil or disabled,
	// Wrap is the identity transform — the program/args we hand to
	// exec.CommandContext are unchanged and cleanup is a no-op. When
	// active on darwin, this becomes
	//   sandbox-exec -f <profile> <original program> <original args...>
	// so credential paths under ~/.ssh, ~/.aws, ~/.docker/config.json
	// are inaccessible to the downstream MCP server. Cleanup fires
	// from stop() so the per-spawn profile tempfile gets removed.
	progPath, progArgs := cmdPath, inst.args
	cleanup := func() {}
	if inst.wrapper != nil {
		progPath, progArgs, cleanup = inst.wrapper.Wrap(cmdPath, inst.args)
	}
	inst.wrapperCleanup = cleanup

	cmd := exec.CommandContext(childCtx, progPath, progArgs...)
	cmd.Env = inst.env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		inst.state = StateStopped
		return fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		inst.state = StateStopped
		return fmt.Errorf("stdout pipe: %w", err)
	}

	cmd.Stderr = &stderrLogger{serverID: inst.key.ServerID}

	if err := cmd.Start(); err != nil {
		cancel()
		inst.state = StateStopped
		return fmt.Errorf("start process: %w", err)
	}

	inst.cmd = cmd
	inst.stdin = stdin
	inst.done = make(chan struct{})

	// One scanner reads stdout for the instance's whole lifetime — the
	// handshake and the request loop MUST share it. Two scanners on one pipe
	// would let the handshake buffer (and then discard) bytes past the
	// initialize line, desyncing every subsequent response.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	// Perform MCP initialize handshake with timeout.
	initCtx, initCancel := context.WithTimeout(childCtx, 30*time.Second)
	if err := inst.initialize(initCtx, stdin, scanner); err != nil {
		initCancel()
		_ = cmd.Process.Kill()
		cancel()
		inst.state = StateStopped
		return fmt.Errorf("initialize: %w", err)
	}
	initCancel()

	inst.state = StateReady

	// Start the processing loop and monitor goroutines.
	go inst.processLoop(scanner)
	go inst.monitorProcess(cmd)

	return nil
}

func (inst *Instance) initialize(ctx context.Context, stdin io.Writer, scanner *bufio.Scanner) error {
	initReq := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params: json.RawMessage(`{
			"protocolVersion": "2025-03-26",
			"capabilities": {},
			"clientInfo": {"name": "mcplexer", "version": "0.1.0"}
		}`),
	}
	if err := writeJSONLine(stdin, initReq); err != nil {
		return fmt.Errorf("write initialize: %w", err)
	}

	// Read the initialize response, tolerating launcher preamble. A cold
	// `uvx`/`npx` spawn can print resolution/progress lines to stdout before
	// the wrapped server starts speaking JSON-RPC; blindly taking the first
	// line as the response would desync the stream for the instance's whole
	// life. Skip non-JSON and non-matching lines until the id==1 response.
	ch := make(chan error, 1)
	go func() {
		for scanner.Scan() {
			line := scanner.Bytes()
			var resp jsonRPCResponse
			if err := json.Unmarshal(line, &resp); err != nil || resp.ID == nil {
				// Preamble noise or a pre-init notification — skip it.
				inst.logInitPreamble(line)
				continue
			}
			if !responseIDMatches(resp.ID, 1) {
				inst.logInitPreamble(line)
				continue
			}
			ch <- nil
			return
		}
		ch <- fmt.Errorf("no initialize response")
	}()

	select {
	case <-ctx.Done():
		return fmt.Errorf("initialize timed out: %w", ctx.Err())
	case err := <-ch:
		if err != nil {
			return err
		}
	}

	// Send initialized notification.
	notif := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	return writeJSONLine(stdin, notif)
}

// logInitPreamble records a non-response line seen during the handshake so a
// misbehaving launcher is diagnosable, capped to avoid flooding logs.
func (inst *Instance) logInitPreamble(line []byte) {
	const maxLen = 200
	s := string(line)
	s = s[:min(len(s), maxLen)]
	slog.Debug("downstream init preamble skipped", "server", inst.key.ServerID, "line", s)
}

func (inst *Instance) processLoop(scanner *bufio.Scanner) {
	defer close(inst.done)

	for {
		req, ok := inst.queue.dequeue()
		if !ok {
			return
		}

		inst.mu.Lock()
		inst.state = StateBusy
		inst.mu.Unlock()

		result, err := inst.handleRequest(req, scanner)

		req.Result <- response{Data: result, Err: err}

		inst.mu.Lock()
		inst.state = StateIdle
		inst.resetIdleTimer()
		inst.mu.Unlock()
	}
}

func (inst *Instance) handleRequest(
	req request, scanner *bufio.Scanner,
) (json.RawMessage, error) {
	rpcReq := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(fmt.Sprintf(`%d`, req.ID)),
		Method:  req.Method,
		Params:  req.Params,
	}

	inst.mu.Lock()
	w := inst.stdin
	inst.mu.Unlock()

	if err := writeJSONLine(w, rpcReq); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	return inst.readResponse(scanner, req.ID)
}

// readResponse scans lines until finding the JSON-RPC response whose id
// matches expectID (the id we just wrote for this request).
//
// Any interleaved notifications (no id) are forwarded via onNotify and
// skipped. A response whose id does NOT match expectID is a stale or
// out-of-band message — typically the late response of a PRIOR request
// that was abandoned when its per-call deadline fired (see
// Manager.Call's timeout branch). Returning it here would hand one
// caller another request's result (a cross-call / cross-scope data
// leak). We treat a mismatched id as a hard desync error so the caller
// fails loudly and the instance is evicted rather than silently
// answering with the wrong payload.
func (inst *Instance) readResponse(scanner *bufio.Scanner, expectID int) (json.RawMessage, error) {
	for {
		if !scanner.Scan() {
			return nil, fmt.Errorf("no response from downstream")
		}

		var rpcResp jsonRPCResponse
		if err := json.Unmarshal(scanner.Bytes(), &rpcResp); err != nil {
			// A non-JSON line on the response stream means the stream is
			// corrupt/desynced. Wrap ErrResponseDesync so the Manager evicts
			// this instance and the next call lazy-starts a fresh process,
			// instead of leaving it poisoned until idle timeout.
			return nil, fmt.Errorf("%w: unmarshal response: %v", ErrResponseDesync, err)
		}

		// No id means this is a notification, not a response.
		if rpcResp.ID == nil {
			inst.forwardNotification(scanner.Bytes())
			continue
		}

		// Guard against stream desync: the response id MUST match the
		// request id we just wrote. A mismatch means a prior request's
		// late response is still in the stream (its caller timed out and
		// moved on). Surfacing it as this call's result would leak one
		// request's data into another's reply, so fail hard instead.
		if !responseIDMatches(rpcResp.ID, expectID) {
			return nil, fmt.Errorf(
				"%w: got response id %s, expected %d",
				ErrResponseDesync, string(rpcResp.ID), expectID)
		}

		if rpcResp.Error != nil {
			return nil, fmt.Errorf("downstream error %d: %s",
				rpcResp.Error.Code, rpcResp.Error.Message)
		}

		return rpcResp.Result, nil
	}
}

// responseIDMatches reports whether a JSON-RPC response id (raw JSON)
// equals the integer request id we wrote. handleRequest always writes the
// id as a bare JSON number (fmt.Sprintf("%d", req.ID)), so we compare
// against that canonical form. Some downstreams echo the id as a string
// ("5") rather than a number (5); accept either rendering to avoid false
// desync errors against spec-loose servers.
func responseIDMatches(rawID json.RawMessage, expectID int) bool {
	want := fmt.Sprintf("%d", expectID)
	got := strings.TrimSpace(string(rawID))
	if got == want {
		return true
	}
	// Tolerate a string-encoded id, e.g. "5".
	return got == fmt.Sprintf("%q", want)
}

// forwardNotification extracts the method and params from a JSON-RPC
// notification and calls onNotify if set.
func (inst *Instance) forwardNotification(data []byte) {
	if inst.onNotify == nil {
		return
	}
	var notif struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params,omitempty"`
	}
	if err := json.Unmarshal(data, &notif); err != nil || notif.Method == "" {
		return
	}
	slog.Debug("downstream notification",
		"server", inst.key.ServerID, "method", notif.Method)
	inst.onNotify(notif.Method, notif.Params)
}

func (inst *Instance) getState() InstanceState {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.state
}

func (inst *Instance) waitRestartDone() <-chan struct{} {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if inst.restartWait != nil {
		return inst.restartWait
	}
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (inst *Instance) closeRestartWaitLocked() {
	if inst.restartWait == nil {
		return
	}
	select {
	case <-inst.restartWait:
	default:
		close(inst.restartWait)
	}
}

// shouldRestart reports whether the process should be restarted after
// exiting with the given code, based on the configured restart_policy.
// A negative exitCode means the process did not exit on its own (signal,
// failed to start, etc.) and is treated as a failure for on-failure.
//
// This is the CRASH-recovery layer (process exited). The operational
// stuck-detection layer (process alive but not responding) lives in
// HealthTracker.RecordFailure / MarkReload — see health.go for the
// two-layer retry model.
func (inst *Instance) shouldRestart(exitCode int) bool {
	switch inst.restartPolicy {
	case "always":
		return true
	case "on-failure":
		return exitCode != 0
	default: // "never" or unknown
		return false
	}
}

// exitCodeFrom extracts the numeric exit code from cmd.Wait()'s error.
// Returns 0 on nil error (clean exit), positive integer for ExitError,
// -1 for any other error (signal kill, failed to start, etc.).
func exitCodeFrom(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return status.ExitStatus()
		}
	}
	return -1
}

func (inst *Instance) requestQueue() *requestQueue {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.queue
}

// Call sends a request via the queue and waits for the response.
// If the caller's context is cancelled before the downstream replies, send a
// best-effort notifications/cancelled message for the in-flight request before
// returning. Manager.Call will still evict timed-out instances to avoid reusing
// a possibly desynced stdio stream.
func (inst *Instance) Call(
	ctx context.Context, method string, params json.RawMessage,
) (json.RawMessage, error) {
	if s := inst.getState(); s == StateRestarting {
		return nil, fmt.Errorf("instance %s is restarting", inst.key.ServerID)
	}

	resultCh := make(chan response, 1)
	id := int(inst.reqID.Add(1))
	queue := inst.requestQueue()

	if !queue.enqueue(request{
		ID:     id,
		Method: method,
		Params: params,
		Result: resultCh,
	}) {
		return nil, fmt.Errorf("instance %s is stopped", inst.key.ServerID)
	}

	select {
	case <-ctx.Done():
		inst.sendCancelled(id, ctx.Err().Error())
		return nil, ctx.Err()
	case resp := <-resultCh:
		return resp.Data, resp.Err
	}
}

func (inst *Instance) sendCancelled(requestID int, reason string) {
	params, err := json.Marshal(map[string]any{
		"requestId": requestID,
		"reason":    reason,
	})
	if err != nil {
		return
	}
	notif := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/cancelled",
		Params:  params,
	}

	inst.mu.Lock()
	w := inst.stdin
	inst.mu.Unlock()
	if w == nil {
		return
	}
	_ = writeJSONLine(w, notif)
}

// ListTools sends a tools/list request to the downstream instance.
func (inst *Instance) ListTools(ctx context.Context) (json.RawMessage, error) {
	if s := inst.getState(); s == StateRestarting {
		return nil, fmt.Errorf("instance %s is restarting", inst.key.ServerID)
	}

	resultCh := make(chan response, 1)
	id := int(inst.reqID.Add(1))
	queue := inst.requestQueue()

	if !queue.enqueue(request{
		ID:     id,
		Method: "tools/list",
		Params: json.RawMessage(`{}`),
		Result: resultCh,
	}) {
		return nil, fmt.Errorf("instance %s is stopped", inst.key.ServerID)
	}

	select {
	case <-ctx.Done():
		inst.sendCancelled(id, ctx.Err().Error())
		return nil, ctx.Err()
	case resp := <-resultCh:
		return resp.Data, resp.Err
	}
}

func (inst *Instance) monitorProcess(cmd *exec.Cmd) {
	err := cmd.Wait()

	inst.mu.Lock()

	if inst.state == StateStopping {
		inst.mu.Unlock()
		return
	}

	exitCode := exitCodeFrom(err)
	if err != nil {
		slog.Error("downstream process crashed",
			"server", inst.key.ServerID, "error", err, "exit_code", exitCode)
	}

	if inst.idleTimer != nil {
		inst.idleTimer.Stop()
	}

	if !inst.shouldRestart(exitCode) {
		inst.state = StateStopped
		inst.restartAttempt = 0
		inst.queue.close()
		inst.mu.Unlock()
		return
	}

	inst.restartAttempt++
	restartAttempt := inst.restartAttempt
	backoff := inst.computeRestartBackoff()
	inst.state = StateRestarting
	inst.restartWait = make(chan struct{})
	inst.queue.close()
	done := inst.done
	inst.mu.Unlock()

	<-done

	slog.Info("restarting downstream per restart_policy",
		"server", inst.key.ServerID,
		"restart_policy", inst.restartPolicy,
		"exit_code", exitCode,
		"attempt", restartAttempt,
		"backoff", backoff)

	go func() {
		if backoff > 0 {
			t := time.NewTimer(backoff)
			select {
			case <-t.C:
			case <-inst.waitUntilStopped():
				t.Stop()
				inst.mu.Lock()
				inst.state = StateStopped
				inst.closeRestartWaitLocked()
				inst.mu.Unlock()
				return
			}
		}

		startErr := inst.start(context.Background())
		inst.mu.Lock()
		if startErr != nil {
			inst.queue.close()
			inst.state = StateStopped
			slog.Error("failed to restart downstream",
				"server", inst.key.ServerID, "error", startErr)
		}
		inst.closeRestartWaitLocked()
		inst.mu.Unlock()
	}()
}

func (inst *Instance) computeRestartBackoff() time.Duration {
	if inst.restartAttempt <= 1 {
		return 0
	}
	shift := uint(inst.restartAttempt - 2)
	if shift > 10 {
		shift = 10
	}
	d := MinRestartBackoff * time.Duration(1<<shift)
	if d > MaxRestartBackoff {
		d = MaxRestartBackoff
	}
	return d
}

func (inst *Instance) waitUntilStopped() <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		for {
			s := inst.getState()
			if s == StateStopping || s == StateStopped {
				close(ch)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	return ch
}

func (inst *Instance) stop() {
	inst.mu.Lock()
	if inst.state == StateStopped || inst.state == StateStopping {
		inst.mu.Unlock()
		return
	}
	inst.state = StateStopping
	if inst.idleTimer != nil {
		inst.idleTimer.Stop()
	}
	cancel := inst.cancel
	cmd := inst.cmd
	queue := inst.queue
	inst.mu.Unlock()

	queue.close()
	if cancel != nil {
		cancel()
	}

	select {
	case <-inst.done:
	case <-time.After(5 * time.Second):
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}

	inst.mu.Lock()
	inst.state = StateStopped
	inst.restartAttempt = 0
	cleanup := inst.wrapperCleanup
	inst.wrapperCleanup = nil
	inst.closeRestartWaitLocked()
	inst.mu.Unlock()
	if cleanup != nil {
		cleanup()
	}
}

func (inst *Instance) resetIdleTimer() {
	if inst.idleTimeout <= 0 {
		return
	}
	if inst.idleTimer != nil {
		inst.idleTimer.Stop()
	}
	inst.idleTimer = time.AfterFunc(inst.idleTimeout, func() {
		slog.Info("idle timeout, stopping instance",
			"server", inst.key.ServerID)
		inst.stop()
	})
}

// lookPathInEnv resolves a command name to its absolute path using the PATH
// from the given environment slice (not the current process's PATH). This is
// needed because Go's exec.Command uses os.Getenv("PATH") for resolution,
// which may be a minimal launchd PATH missing directories like /opt/homebrew/bin.
func lookPathInEnv(cmd string, env []string) (string, error) {
	var pathVal string
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			pathVal = e[5:]
			break
		}
	}
	if pathVal == "" {
		return "", fmt.Errorf("no PATH in env")
	}
	for _, dir := range filepath.SplitList(pathVal) {
		candidate := filepath.Join(dir, cmd)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s not found in augmented PATH", cmd)
}

// stderrLogger is an io.Writer that logs each line from a downstream process's
// stderr via slog. Partial lines are buffered until a newline is seen.
type stderrLogger struct {
	serverID string
	buf      []byte
}

func (l *stderrLogger) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		idx := bytes.IndexByte(l.buf, '\n')
		if idx < 0 {
			break
		}
		line := string(l.buf[:idx])
		l.buf = l.buf[idx+1:]
		if line = strings.TrimSpace(line); line != "" {
			slog.Warn("downstream stderr",
				"server", l.serverID, "line", line)
		}
	}
	return len(p), nil
}
