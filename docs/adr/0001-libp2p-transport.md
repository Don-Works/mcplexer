# ADR 0001 — libp2p as the cross-machine transport for mcplexer

- Status: Accepted (spike R0.1)
- Date: 2026-04-30
- Branch: `spike/libp2p-embed`
- ClickUp: [R0.1 — Spike: embed go-libp2p in mcplexer daemon](https://app.clickup.com/t/86c9k55k0)

## Decision

We will embed `go-libp2p` directly in the mcplexer daemon as the transport
for cross-machine agent communication, gated behind a default-off
`MCPLEXER_P2P_ENABLED` flag.

## Context

The agent-mesh feature today is single-machine (SQLite + an in-process bus).
M1 expands it to cross-machine: an agent on laptop A must be able to reach
an agent on laptop B even when both are behind NAT, with end-to-end
authenticated identities. The R0 spike must validate that doing this with
`go-libp2p` is feasible in the existing daemon — specifically that:

1. We can co-exist with `modernc.org/sqlite` (no CGO conflicts).
2. The binary-size and memory cost is bounded.
3. Two daemons can discover and ping each other on a single host.
4. The whole subsystem can be disabled without behavior changes.

## Alternatives considered

### Plain mTLS over TCP

We pick a port, exchange certs out-of-band (or via our existing
mesh-registration channel), and run protobuf or HTTP/2 over TLS. **Rejected**
because it doesn't solve NAT — both sides need a routable address, which is
the problem we're trying to delegate. We'd end up reinventing AutoNAT and
hole-punching.

### mTLS + a self-hosted relay

Same as above plus a TURN-style relay we operate. **Rejected** because we'd
own the relay's availability, scaling, and abuse story. libp2p's circuit-v2
+ DCUtR gives us a relay-as-fallback flow with public bootstrap relays and
upgrades to direct connections automatically; we can still self-host relays
later as a deployment option without changing the protocol.

### Iroh (`iroh-net`)

Modern Rust QUIC-based mesh with a smaller dep surface and excellent NAT
traversal. **Rejected** because it's a Rust library — adoption would mean
either a CGO bridge (kills our no-CGO SQLite story) or an out-of-process
helper, both of which fight the "single static binary" distribution model
we already lean on for the desktop app and CLI. Worth revisiting if the Go
bindings mature.

## Consequences

### Cost (measured on darwin/arm64, Go 1.25.0)

| metric             | baseline | with libp2p (off) | with libp2p (on) |
|--------------------|---------:|------------------:|-----------------:|
| binary size        |  30.7 MB |           53.7 MB |          53.7 MB |
| dep packages       |      307 |               635 |              635 |
| RSS, post-listen   |  ~24 MB  |            ~30 MB |       ~38–40 MB  |
| cold start to bind |  ~150 ms |          ~95 ms¹  |          ~100 ms |

¹ The "off" cold start is faster because we skipped re-warming the SQLite
schema between runs; binary-cost-only (sqlite warm) is the relevant number.
The takeaway: enabling libp2p adds ~9–15 MB live RSS and zero detectable
startup latency.

### What gets easier

- **NAT traversal**: AutoNAT + DCUtR + circuit-v2 relay are wired up by
  flipping options on `libp2p.New` — no port-forwarding instructions, no
  TURN ops.
- **Identity**: Every host has a self-sovereign Ed25519 keypair and a
  `peer.ID` we can pin in the mesh DB; no PKI.
- **Multi-transport**: TCP and QUIC both up by default; QUIC alone gives us
  faster handshakes and 0-RTT for repeat dials.
- **Discovery**: mDNS gets us local-network discovery for free; DHT and
  rendezvous are available when we want internet-wide finding.

### What gets harder

- **Binary size**: +23 MB is real. Acceptable for a daemon (we ship a
  desktop app already), but worth tracking. If it becomes a problem we can
  build a `noP2P` build tag to trim it from CLI-only distributions.
- **Dep surface**: +328 transitive deps. Largest are `quic-go`, `pion/*`
  (WebRTC, only pulled because go-libp2p depends on it for WebTransport),
  `go.uber.org/fx`, and `prometheus/client_golang`. Upgrade discipline and
  Dependabot grouping become more important.
- **Lifecycle complexity**: The libp2p Host is its own goroutine tree with
  its own connection manager, GC, and event bus. Crashes/leaks here will
  not be obvious from our existing API tracing — we'll need to surface
  libp2p metrics into our `/api/health` and tray UI.
- **Observability gap**: libp2p uses `go.uber.org/zap`; we use `slog`. Logs
  arrive through two channels for now.

### Migration / rollback

The whole subsystem is behind `Config.P2PEnabled` (env: `MCPLEXER_P2P_ENABLED`,
flag: `--p2p`). Default off means existing behavior is preserved. Removing
the dependency is a `go mod edit -droprequire` + delete `internal/p2p/` if
we change our minds before M1 ships.

## Open questions (deferred to M1.1+)

1. **Identity-at-rest**: the spike writes `~/.mcplexer/p2p/identity.key` in
   cleartext. M1.1 must move it to the age-encrypted secrets store and
   surface a "rotate identity" UI flow.
2. **Bootstrap relays**: we use libp2p's public defaults for the spike. For
   production we want either (a) our own bootstrap-relay deployment or (b)
   an explicit allowlist in settings — likely both.
3. **Authorisation model**: a `peer.ID` proves *who* the remote is, not
   what they're allowed to do. We need an ACL layer (workspace-scoped,
   tied to the existing approvals system).
4. **Protocol versioning**: libp2p stream protocols are namespaced strings
   (`/mcplexer/mesh/1.0.0`). We need a versioning policy before we ship
   the first one.
5. **Connection-manager tuning**: limits, GC, and trim policy defaults are
   probably wrong for a single-user laptop daemon — needs benchmarking.
6. **Mobile / sandboxed environments**: macOS App Sandbox and iOS limit
   what mDNS / multicast we can do. The desktop app currently isn't
   sandboxed, but if we ever Notarize+Sandbox, we need to verify.
7. **NAT64 / IPv6-only networks**: not exercised in this spike.
8. **Binary trim**: investigate `noP2P` build tag for CLI-only releases if
   the +23 MB becomes a distribution problem.
9. **Telemetry**: surface libp2p's prometheus metrics in our `/api/health`
   and the desktop tray.
