// client.go — bounded SSH dial + run (ADR 0007 §4). Every run carries
// a byte cap and inherits the caller's context deadline; the session
// is torn down at either limit. Truncation is reported, never silent.
package sshx

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/store"
)

// DialTimeout bounds the TCP+handshake phase of a dial.
const DialTimeout = 15 * time.Second

// Result is one bounded command run. Stdout and Stderr share ONE
// maxBytes budget (see captureStreams) — Docker preserves stream
// separation for docker/compose/swarm logs, so an app's stderr-origin
// lines land in Stderr, not Stdout.
type Result struct {
	Stdout    []byte
	Stderr    []byte
	Truncated bool
	// NewPin is set when the dial TOFU-recorded a fingerprint (host
	// had no pin). The caller persists it via SetRemoteHostPin.
	NewPin string
}

// Client wraps one authenticated ssh connection.
type Client struct {
	ssh       *ssh.Client
	agentConn net.Conn
	newPin    string
}

// Dial connects to host with cred, enforcing the host-key pin. When
// the host has no pin yet the first-seen fingerprint is recorded on
// the returned client (TOFU) for the caller to persist.
func Dial(ctx context.Context, host *store.RemoteHost, cred Credential) (*Client, error) {
	method, agentConn, err := cred.authMethod()
	if err != nil {
		return nil, err
	}
	c := &Client{agentConn: agentConn}
	cfg := &ssh.ClientConfig{
		User:            host.SSHUser,
		Auth:            []ssh.AuthMethod{method},
		HostKeyCallback: pinnedHostKeyCallback(host.HostKeyPin, func(fp string) { c.newPin = fp }),
		Timeout:         DialTimeout,
	}
	addr := net.JoinHostPort(host.SSHHost, strconv.Itoa(host.SSHPort))
	d := net.Dialer{Timeout: DialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("sshx: dial %s: %w", addr, err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		c.Close()
		return nil, fmt.Errorf("sshx: handshake %s: %w", addr, err)
	}
	c.ssh = ssh.NewClient(sshConn, chans, reqs)
	return c, nil
}

// NewPin returns the TOFU-recorded fingerprint from this dial, or ""
// when the host was already pinned.
func (c *Client) NewPin() string { return c.newPin }

// Run executes one pre-built command (from a fixed builder in this
// package) with a byte cap shared across stdout and stderr. The
// session dies with the context.
func (c *Client) Run(ctx context.Context, command string, maxBytes int64) (Result, error) {
	res := Result{NewPin: c.newPin}
	if maxBytes <= 0 {
		return res, fmt.Errorf("sshx: maxBytes must be positive")
	}
	sess, err := c.ssh.NewSession()
	if err != nil {
		return res, fmt.Errorf("sshx: new session: %w", err)
	}
	defer func() { _ = sess.Close() }()
	stdout, stderr, err := sessionPipes(sess)
	if err != nil {
		return res, err
	}
	if err := sess.Start(command); err != nil {
		return res, fmt.Errorf("sshx: start: %w", err)
	}
	stopWatch := context.AfterFunc(ctx, func() { _ = sess.Close() })
	defer stopWatch()
	so, se, truncated, readErr := captureStreams(stdout, stderr, maxBytes, func() { _ = sess.Close() })
	res.Stdout, res.Stderr, res.Truncated = so, se, truncated
	waitErr := sess.Wait()
	return finishRun(ctx, command, res, readErr, waitErr)
}

func sessionPipes(session *ssh.Session) (io.Reader, io.Reader, error) {
	stdout, err := session.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("sshx: stdout pipe: %w", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("sshx: stderr pipe: %w", err)
	}
	return stdout, stderr, nil
}

func finishRun(
	ctx context.Context, command string, result Result, readErr, waitErr error,
) (Result, error) {
	if ctx.Err() != nil {
		return result, fmt.Errorf("sshx: run aborted: %w", ctx.Err())
	}
	if readErr != nil {
		return result, fmt.Errorf("sshx: read: %w", readErr)
	}
	if waitErr != nil && !result.Truncated {
		return result, fmt.Errorf("sshx: %s: %w (%s)",
			firstToken(command), waitErr, diagnostic(result.Stdout, result.Stderr))
	}
	return result, nil
}

// diagnostic returns a small, redacted tail of the run's captured
// output for a non-zero-exit error — never the full command or
// output (ADR 0007 §5: nothing unredacted reaches logs). stderr is
// preferred since that's where remote tools put failure messages;
// stdout is a fallback when stderr is empty.
func diagnostic(stdout, stderr []byte) string {
	s := string(stderr)
	if s == "" {
		s = string(stdout)
	}
	s = audit.RedactString(s, nil)
	const n = 256
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// Close tears down the ssh and agent connections.
func (c *Client) Close() {
	if c.ssh != nil {
		_ = c.ssh.Close()
	}
	if c.agentConn != nil {
		_ = c.agentConn.Close()
	}
}

func firstToken(s string) string {
	for i := range len(s) {
		if s[i] == ' ' {
			return s[:i]
		}
	}
	return s
}
