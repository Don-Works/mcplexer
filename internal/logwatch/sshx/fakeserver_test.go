package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"io"
	"net"
	"strconv"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/don-works/mcplexer/internal/store"
)

// execHandler plays one exec session: write to stdout/stderr and
// return an exit status. Neither stream is closed by the handler —
// the session teardown does that.
type execHandler func(stdout, stderr io.Writer) uint32

// fakeServer is an in-process SSH server for exec-session tests.
// Client-key identity is not gated (this fixture protects no real
// credential — auth.go's signer plumbing is exercised for real by
// dialing with a generated key); host-key TOFU/pinning itself is
// covered by TestHostKeyPinning against the real callback.
type fakeServer struct {
	addr    string
	hostKey ssh.PublicKey
}

func startFakeServer(t *testing.T, handler execHandler) *fakeServer {
	t.Helper()
	hostSigner := genSigner(t)
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, nil
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go acceptLoop(ln, cfg, handler)
	return &fakeServer{addr: ln.Addr().String(), hostKey: hostSigner.PublicKey()}
}

func acceptLoop(ln net.Listener, cfg *ssh.ServerConfig, handler execHandler) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed at test cleanup
		}
		go serveConn(conn, cfg, handler)
	}
}

func serveConn(conn net.Conn, cfg *ssh.ServerConfig, handler execHandler) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return // handshake aborted by the client — not a test failure
	}
	defer func() { _ = sconn.Close() }()
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only session supported")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go serveSession(ch, chReqs, handler)
	}
}

func serveSession(ch ssh.Channel, reqs <-chan *ssh.Request, handler execHandler) {
	defer func() { _ = ch.Close() }()
	for req := range reqs {
		if req.Type != "exec" {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		if req.WantReply {
			_ = req.Reply(true, nil)
		}
		status := handler(ch, ch.Stderr())
		var payload [4]byte
		binary.BigEndian.PutUint32(payload[:], status)
		_, _ = ch.SendRequest("exit-status", false, payload[:])
		return
	}
}

// dial connects a real sshx.Client to the fixture, pinning its host
// key — ADR 0007 bans InsecureIgnoreHostKey, including in tests.
func (fs *fakeServer) dial(t *testing.T) *Client {
	t.Helper()
	host, portStr, err := net.SplitHostPort(fs.addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	rh := &store.RemoteHost{
		SSHHost: host, SSHPort: port, SSHUser: "test",
		HostKeyPin: ssh.FingerprintSHA256(fs.hostKey),
	}
	c, err := Dial(t.Context(), rh, Credential{PrivateKeyPEM: genClientKeyPEM(t)})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

func genSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return signer
}

func genClientKeyPEM(t *testing.T) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen client key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal client key: %v", err)
	}
	return pem.EncodeToMemory(block)
}
