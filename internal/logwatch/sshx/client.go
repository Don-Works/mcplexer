// client.go — bounded SSH dial + run (ADR 0007 §4). Every run carries
// a byte cap and inherits the caller's context deadline; the session
// is torn down at either limit. Truncation is reported, never silent.
package sshx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/don-works/mcplexer/internal/store"
)

// DialTimeout bounds the TCP+handshake phase of a dial.
const DialTimeout = 15 * time.Second

// Result is one bounded command run.
type Result struct {
	Output    []byte
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
// package) with a byte cap. The session dies with the context.
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

	stdout, err := sess.StdoutPipe()
	if err != nil {
		return res, fmt.Errorf("sshx: stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	sess.Stderr = &stderr

	if err := sess.Start(command); err != nil {
		return res, fmt.Errorf("sshx: start: %w", err)
	}

	// Kill the session when the context dies so a hung remote read
	// cannot outlive the wall-clock cap.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = sess.Close()
		case <-done:
		}
	}()

	// Read one byte past the cap so truncation is detectable.
	out, readErr := io.ReadAll(io.LimitReader(stdout, maxBytes+1))
	if int64(len(out)) > maxBytes {
		out = out[:maxBytes]
		res.Truncated = true
		_ = sess.Close() // stop the remote writer; Wait below will error, which we ignore for truncated reads
	}
	res.Output = out

	waitErr := sess.Wait()
	if ctx.Err() != nil {
		return res, fmt.Errorf("sshx: run aborted: %w", ctx.Err())
	}
	if readErr != nil {
		return res, fmt.Errorf("sshx: read: %w", readErr)
	}
	if waitErr != nil && !res.Truncated {
		return res, fmt.Errorf("sshx: %s: %w (stderr: %s)", firstToken(command), waitErr, truncateStr(stderr.String(), 512))
	}
	return res, nil
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

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
