# ADR 0007: Remote-Exec SSH Security Model (Monitoring / logwatch)

## Status

Accepted (2026-07-08). Ratified in Q&A with the operator; see
`docs/design/remote-log-intelligence.md` §7 and §9.

## Context

The Monitoring feature (design: `docs/design/remote-log-intelligence.md`)
pulls docker logs off remote production machines over SSH from inside the
mcplexer daemon. This is the first time the daemon initiates outbound
command execution on machines it does not manage. The blast radius of a
compromised or confused gateway must stay at zero for watched boxes, and
credentials must follow the existing rule that plaintext never enters any
model context.

## Decision

### 1. Read-only by construction — no generic exec path

The SSH layer (`internal/logwatch/sshx`) exposes no API that accepts a
command string. Callers select a *kind* (`docker`, later `journald` /
`file`) and the executor builds a fixed command template:

- `docker`: `docker logs --timestamps --since <cursor> <selector>`
- future kinds add their own fixed read-only templates (`journalctl -u`,
  `tail -c`).

Selectors are validated against
`^[A-Za-z0-9._/][A-Za-z0-9._/-]*$` at CRUD time **and** again at dial time,
then single-quoted. SSH's exec request carries one command string which the
remote login shell interprets; the protocol does not provide a true argv exec.
The safety boundary is therefore the literal per-kind template plus strict
validation and quoting—not an absence of a shell. Adding any mutating command
shape requires a new ADR.

### 2. Credentials: two auth-scope types, both landing together

- `ssh_key`: private key material age-encrypted in the existing secrets
  store. At dial time the key is decrypted **in memory only** and handed to
  a `golang.org/x/crypto/ssh` signer; it is never written to disk, never
  logged, never serialized into tool results, and never crosses into worker
  or model context.
- `ssh_agent`: passthrough to a running agent socket (`SSH_AUTH_SOCK` or an
  explicit socket path) for installs where an agent is available. The
  daemon holds no key material in this mode.

Both resolve through the AuthScope indirection like every other credential
in mcplexer; log-source config references a scope id, never a key.

### 3. Host identity: TOFU pinning, hard-fail on change

First successful dial records the host public key fingerprint on the
`remote_hosts` row. Every subsequent dial verifies against the pin; a
mismatch fails the source hard (no retry-through) and raises a `critical`
notification through the monitoring dispatcher. `InsecureIgnoreHostKey` is
banned, including in tests (integration tests pin the fixture host).
Re-pinning after a legitimate host rebuild is an explicit operator action
(admin tool / Monitoring UI), never automatic.

### 4. Bounded reads

Per pull: byte cap (default 4 MiB) and wall-clock cap (default 30 s); the
SSH session is closed at the deadline. Truncation is recorded as a
synthetic template so silent gaps cannot masquerade as quiet logs.

### 5. Redaction before persistence

Every line passes the audit value-pattern redaction pass (recognised bearer
tokens, API keys, webhook URLs, JWTs, and private-key blocks) **before** it is
written to the ring buffer. Digests, templates, search results, and model
prompts are downstream of that pass. Pattern redaction is defence in depth,
not a guarantee for arbitrary application-specific secret formats: watched
services must avoid logging secrets, gateway DB access remains sensitive, and
operators must test representative log corpora before enabling model triage.

### 6. Outbound notifications: daemon-side dispatch only

Channel credentials (Google Chat webhook URLs, WhatsApp MSISDN) live in
`monitoring_channels.config_json` as `secret://` refs. Resolution happens
inside `monitoring.notify` (daemon code). The built-in log-watch worker's
tool allowlist contains no channel tools, so no model can send anywhere
except through the dispatcher, which also stamps the deterministic envelope
(workspace name + gateway hostname + affected remote hostname).

### 7. Least privilege on the box (guidance, not enforcement)

Membership in the Docker daemon's group is **root-equivalent**: that user can
normally start a privileged container and mount the host filesystem. It is a
functional setup, not a least-privilege one, and production reviews must record
that risk explicitly.

The preferred production setup is a dedicated, non-login `logwatch` account
whose SSH key is restricted with an `authorized_keys` forced command. A
root-owned wrapper must reject anything except the exact supported log-read
grammars and then invoke Docker; the account must not receive unrestricted
Docker-socket access. Restrict the key further with `no-port-forwarding`,
`no-agent-forwarding`, `no-X11-forwarding`, and `no-pty`. A rootless Docker
daemon dedicated to the watched workload is another acceptable boundary.

A bare sudoers wildcard such as “allow `docker logs *`” is not sufficient:
argument matching and future Docker flags can reopen command or daemon access.
Until the forced-command/rootless boundary is deployed and tested, treat the
remote credential as host-root-equivalent.

## Consequences

- The current command builder offers no configurable mutation path, but the
  remote credential's true blast radius depends on the box-side restriction.
- A Docker-group credential is host-root-equivalent even though MCPlexer only
  constructs log-read commands.
- Key rotation = update the auth scope; host rebuild = explicit re-pin.
- The no-generic-exec rule means future remediation features cannot quietly
  reuse this transport; they must go through their own ADR and design.
- Integration tests require a pinned fixture host and are CI-skippable.
