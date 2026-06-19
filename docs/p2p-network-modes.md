# P2P Network Modes (M1.4)

How two mcplexer daemons behind ordinary home NATs find each other and stay
connected. Read this before debugging "why won't my peer connect" issues.

This is internal-facing — none of these knobs are exposed in the UI as
user-config. The only user-visible signal is the optional debug panel on
the Mesh page (collapsed by default).

## Connection types

mcplexer's embedded libp2p host can hold a peer connection in one of four
modes. The UI labels them, the `/api/p2p/peers` endpoint returns them, and
the audit log records them.

| Mode             | What it means                                                                                  | Typical case                                                              |
| ---------------- | ---------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------- |
| **Direct**       | A normal TCP or QUIC dial reached the peer's listen address.                                   | Both peers on the same LAN, or one has a public IP / forwarded port.       |
| **Hole-punched** | The peer was reached via a STUN-style synchronized dial coordinated through a relay (DCUtR).   | Two peers behind cone NATs (most home routers).                            |
| **Via-relay**    | All traffic flows through a circuit-v2 relay server. Slower, bandwidth-capped, but works.       | At least one peer is behind a symmetric NAT, CGNAT, or a hostile firewall. |
| **None**         | No connection (transient state, or the peer dropped off).                                      | —                                                                         |

The classification rules — see `internal/p2p/connmode_p2p.go::classifyConns`:

1. **Any conn over a circuit relay** (the libp2p `Stat.Limited` flag is
   set, or the multiaddr contains `/p2p-circuit`) wins → **Via-relay**.
2. Else, if any direct conn exists AND the holepunch tracker has recorded
   a successful DCUtR for this peer in the last 30 minutes → **Hole-punched**.
3. Else, direct conn(s) exist → **Direct**.
4. Else → **None**.

The hole-punch tag is sticky for `holePunchTTL` (30 minutes) so a peer
that completes a punch and then keeps the conn alive doesn't silently
drop back to "Direct".

## When each is used

libp2p picks automatically. The order is roughly:

1. Try **direct dial** first. Listens are TCP and QUIC-v1 on all interfaces
   (`/ip4/0.0.0.0/tcp/0` + `/udp/0/quic-v1`). If both peers happen to have
   reachable addresses (LAN, public IP, working UPnP/PCP) the conn lands
   in `Direct` and stays there.

2. If direct fails, **AutoNAT v2** decides whether *we* are publicly
   reachable. If yes, we wait for the other peer to dial us. If no, we
   need help.

3. With `EnableHolePunching` (DCUtR) on, the peer's relay coordinates a
   synchronized dial through whichever public addrs each side observes.
   Success rate on common cone-NATs is well over 70% in libp2p field
   measurements; failures are mostly symmetric NATs and certain CGNAT
   deployments. We track outcomes via a `holepunch.EventTracer`.

4. If hole-punch fails, we fall back to **circuit-relay v2** as a last
   resort. The relay forwards packets between the two peers and is the
   slowest and most bandwidth-constrained mode.

## Static relays

We use **three public IPFS bootstrap nodes** that double as circuit-v2
relays. Sourced from `ipfs/boxo` autoconf fallback (the same list Kubo
uses when its autoconf endpoint is unreachable):

```
/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN
/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa
/dnsaddr/va1.bootstrap.libp2p.io/p2p/12D3KooWKnDdG3iXw9eTFijk3EWSunZcFi54Zka4wmtqtt6rPxc8
```

These are operated by **Protocol Labs as a public good**. We are clients
only. **mcplexer never operates a relay server** — running a public relay
is a load and abuse-prevention burden we deliberately don't take on. If
you need higher relay reliability for production, deploy your own
circuit-v2 relay (see `libp2p/go-libp2p/p2p/protocol/circuitv2/relay`)
and override `Config.BootstrapRelays` with its multiaddr.

To override at the daemon: pass a non-nil, non-empty `BootstrapRelays`
slice to `p2p.Config`. Set it to `[]string{}` (empty but non-nil) to
disable static relays entirely while keeping the relay client transport
on for peers that supply circuit addresses directly.

## What to do if hole-punch fails

It does, sometimes. Failures usually mean:

- **Symmetric NAT**. Some routers (especially in carrier networks)
  randomize the source port for every destination, so the peer can't
  predict where to dial. There's no DCUtR fix — the connection falls
  back to relay, which is the right answer.

- **CGNAT**. Mobile carriers and some ISPs (e.g. shared-IP fibre
  packages) hide all subscribers behind a single public IP. UDP
  hole-punch usually still works on these; TCP often doesn't. We listen
  on both QUIC and TCP so the better path wins.

- **Strict firewall / corporate VPN**. Outbound UDP to arbitrary ports
  may be blocked. Falls back to relay (TCP).

- **Aggressive NAT timeouts**. Some routers drop UDP mappings within
  30 seconds of idle. The libp2p ping service keeps streams warm —
  no action needed on our side.

Diagnostics:

1. Open the debug panel on the Mesh page (collapsed by default at
   the bottom). Expand it to see per-peer modes refreshed every 5
   seconds. If a peer is in `via-relay`, hole-punch failed (or hasn't
   been attempted yet).

2. Check daemon logs for `p2p hole-punch failed` (debug level). The
   error message identifies the failure mode (timeout, no observed
   addresses, etc.).

3. `GET /api/p2p/identity` returns the local PeerID and listen
   multiaddrs. `GET /api/p2p/peers` returns the per-peer mode list.
   Both return 501 if the daemon was built without `-tags p2p`.

## DNS / firewall requirements

Minimum egress for hole-punching to work:

- **Outbound TCP** to any port (for the relay handshake and TCP fallback).
- **Outbound UDP** to any port (for QUIC + the actual hole-punch).
- **Outbound DNS** (UDP/TCP 53) to resolve `bootstrap.libp2p.io`. Any
  resolver works — we don't pin DoH.

We do NOT require any inbound ports. AutoNAT will detect that the host
is private and skip the "wait for incoming dial" step.

There is no central "mcplexer mesh server" — the only outbound endpoints
that matter are the three public relays above (resolved via DNS) plus
whichever peers your local daemon wants to talk to.

## Configuration knobs

In `internal/p2p/config.go`:

- `EnableHolePunch` — DCUtR on/off. Default on in production.
- `EnableRelayClient` — circuit-v2 client transport on/off. Default on.
- `EnableAutoNAT` — AutoNAT (reachability detection). Default on.
- `BootstrapRelays` — static relay multiaddrs. Default: `DefaultBootstrapRelays()`.
- `ConnMgrLowWater` / `ConnMgrHighWater` — connection-manager prune
  thresholds. Default 50 / 200. The manager only kicks in if the host
  is gossiping with hundreds of peers, which a typical desktop install
  never does.

## Related files

- `internal/p2p/host_p2p.go` — wiring of libp2p options.
- `internal/p2p/connmode_p2p.go` — `classifyConns`, `holePunchTracker`.
- `internal/p2p/connmode.go` — `ConnMode` and `PeerMode` types (build-tag-agnostic).
- `internal/api/p2p_handler.go` — `/api/p2p/identity` and `/api/p2p/peers`.
- `web/src/components/p2p-debug-panel.tsx` — collapsed debug panel.
