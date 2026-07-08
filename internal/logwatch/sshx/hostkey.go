// hostkey.go — TOFU host-key pinning (ADR 0007 §3). First successful
// dial records the SHA256 fingerprint; every later dial must match it
// exactly. InsecureIgnoreHostKey is banned including in tests.
package sshx

import (
	"crypto/subtle"
	"fmt"
	"net"

	"golang.org/x/crypto/ssh"
)

// HostKeyMismatchError hard-fails a dial whose host key does not match
// the recorded pin. It is never resolved automatically — the operator
// re-pins explicitly after a legitimate host rebuild.
type HostKeyMismatchError struct {
	Host     string
	Pinned   string
	Presented string
}

func (e *HostKeyMismatchError) Error() string {
	return fmt.Sprintf("sshx: host key mismatch for %s: pinned %s, presented %s — refusing to connect (repin explicitly if the host was rebuilt)",
		e.Host, e.Pinned, e.Presented)
}

// pinnedHostKeyCallback verifies against pin, or records the first-seen
// fingerprint through record when pin is empty (TOFU).
func pinnedHostKeyCallback(pin string, record func(fingerprint string)) ssh.HostKeyCallback {
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)
		if pin == "" {
			if record != nil {
				record(fp)
			}
			return nil
		}
		if subtle.ConstantTimeCompare([]byte(fp), []byte(pin)) != 1 {
			return &HostKeyMismatchError{Host: hostname, Pinned: pin, Presented: fp}
		}
		return nil
	}
}
