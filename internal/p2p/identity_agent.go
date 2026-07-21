package p2p

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// SignDeviceBindingWithSSHAgent proves possession without reading or storing
// private-key bytes. The daemon asks the existing OpenSSH agent to sign the
// domain-separated device-binding transcript with the pinned key.
func SignDeviceBindingWithSSHAgent(
	ctx context.Context,
	fingerprint string,
	challenge DeviceBindingChallenge,
) ([]byte, error) {
	socket := strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK"))
	if socket == "" {
		return nil, ErrSSHAgentUnavailable
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSSHAgentUnavailable, err)
	}
	defer conn.Close() //nolint:errcheck
	signers, err := agent.NewClient(conn).Signers()
	if err != nil {
		return nil, fmt.Errorf("%w: list keys: %v", ErrSSHAgentUnavailable, err)
	}
	for _, signer := range signers {
		if signer == nil || signer.PublicKey() == nil || ssh.FingerprintSHA256(signer.PublicKey()) != fingerprint {
			continue
		}
		return SignDeviceBinding(nil, signer, challenge)
	}
	return nil, fmt.Errorf("%w: key %s is not loaded", ErrSSHAgentUnavailable, fingerprint)
}
