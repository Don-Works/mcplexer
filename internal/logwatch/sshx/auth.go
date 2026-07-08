// auth.go — SSH credential resolution (ADR 0007 §2). Two auth-scope
// types, both landing together: ssh_key (age-encrypted PEM in the
// secrets store, loaded in-memory only) and ssh_agent (socket
// passthrough, no key material held).
package sshx

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// AuthScope types + the secrets-store keys they read.
const (
	AuthScopeTypeSSHKey   = "ssh_key"
	AuthScopeTypeSSHAgent = "ssh_agent"

	// SecretKeyPrivateKey is the secrets-store key holding the PEM
	// private key inside an ssh_key scope.
	SecretKeyPrivateKey = "private_key"
	// SecretKeySocketPath optionally overrides SSH_AUTH_SOCK inside an
	// ssh_agent scope.
	SecretKeySocketPath = "socket_path"
)

// Credential is the resolved auth material for one dial. Exactly one
// field is set. PEM bytes live only for the duration of the dial and
// never serialize anywhere — the caller must not retain them.
type Credential struct {
	PrivateKeyPEM []byte
	AgentSocket   string
}

// authMethod converts the credential into an ssh.AuthMethod. The
// agent connection is returned so the caller can close it with the
// client; nil for key auth.
func (c Credential) authMethod() (ssh.AuthMethod, net.Conn, error) {
	if len(c.PrivateKeyPEM) > 0 {
		signer, err := ssh.ParsePrivateKey(c.PrivateKeyPEM)
		if err != nil {
			return nil, nil, fmt.Errorf("sshx: parse private key: %w", err)
		}
		return ssh.PublicKeys(signer), nil, nil
	}
	sock := c.AgentSocket
	if sock == "" {
		sock = os.Getenv("SSH_AUTH_SOCK")
	}
	if sock == "" {
		return nil, nil, fmt.Errorf("sshx: ssh_agent auth requires a socket (scope socket_path or SSH_AUTH_SOCK)")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, nil, fmt.Errorf("sshx: dial ssh agent: %w", err)
	}
	ag := agent.NewClient(conn)
	return ssh.PublicKeysCallback(ag.Signers), conn, nil
}
