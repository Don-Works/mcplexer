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
`file`) and the executor builds a fixed argv template:

- `docker`: `docker logs --timestamps --since <cursor> --until <now> <selector>`
- future kinds add their own fixed read-only templates (`journalctl -u`,
  `tail -c`).

Selectors are validated against `^[A-Za-z0-9._/-]+$` at CRUD time **and**
again at dial time. No shell is invoked on the remote side (`ssh` exec
channel with argv, never `sh -c`). Adding any mutating command shape
requires a new ADR — it is not a configuration or code-review-sized change.
`internal/gateway/cmdguard.go` protected-path rules extend to remote argv.

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

Every line passes the audit/sanitize redaction pass (bearer tokens, API
keys, secret-shaped strings) **before** it is written to the ring buffer.
Digests, templates, search results, and model prompts are downstream of
storage, so nothing unredacted can reach them.

### 6. Outbound notifications: daemon-side dispatch only

Channel credentials (Google Chat webhook URLs, WhatsApp MSISDN) live in
`monitoring_channels.config_json` as `secret://` refs. Resolution happens
inside `monitoring.notify` (daemon code). The built-in log-watch worker's
tool allowlist contains no channel tools, so no model can send anywhere
except through the dispatcher, which also stamps the deterministic envelope
(workspace name + gateway hostname + affected remote hostname).

### 7. Least privilege on the box (guidance, not enforcement)

Documented setup: a dedicated `logwatch` user in the `docker` group (or a
sudoers line scoped to `docker logs`). The feature functions with any
account that can run `docker logs`; the docs steer operators toward the
narrow one.

## Consequences

- A compromised gateway can *read* logs on watched boxes but cannot mutate
  them through this subsystem — the executor physically lacks the code path.
- Key rotation = update the auth scope; host rebuild = explicit re-pin.
- The no-generic-exec rule means future remediation features cannot quietly
  reuse this transport; they must go through their own ADR and design.
- Integration tests require a pinned fixture host and are CI-skippable.
