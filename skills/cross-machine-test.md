---
name: cross-machine-test
description: End-to-end procedure for proving mcplexer's cross-machine libp2p mesh works between two macOS hosts on the same LAN. Run this when you want to verify pairing + mesh send/receive after touching internal/p2p, internal/mesh, or any pairing/discovery code.
---

# Cross-Machine Mesh Test

A reproducible procedure for proving the libp2p-backed cross-machine mesh works end-to-end between **this** mcplexer host and a second mcplexer host (default: `peer.local`).

The procedure is the same whether you're verifying a fresh build, post-merge regression-checking M1.x work, or demoing the seamless-pairing UX to someone.

---

## Pre-flight

1. **Both machines awake**, on the same LAN (or both reachable via libp2p relay if cross-network).
2. **SSH access** to the peer machine. Test with:
   ```
   ssh -o BatchMode=yes -o ConnectTimeout=5 user@peer.local "echo connected"
   ```
   If it fails with `Permission denied`, ask the user to add your local pubkey to the peer's `~/.ssh/authorized_keys`. Your pubkey is at `~/.ssh/id_ed25519.pub`.
3. **`mcplexer-p2p` binary built locally**. From the mcplexer repo:
   ```
   make build-p2p
   ```
   Produces `bin/mcplexer-p2p` (~52 MB on darwin/arm64).
4. **Same arch on both machines**. macOS arm64 binaries are NOT cross-compatible with intel. If the peer is intel, build with `GOOS=darwin GOARCH=amd64 make build-p2p` instead.

---

## Step 1 — Push the binary to the peer

```
scp bin/mcplexer-p2p user@peer.local:~/mcplexer-p2p
ssh user@peer.local "chmod +x ~/mcplexer-p2p"
```

---

## Step 2 — Start the peer's daemon

In a fresh shell or via SSH-with-detach. Use a distinct data dir + port so it doesn't collide with any existing install:

```
ssh user@peer.local "MCPLEXER_DATA_DIR=~/.mcplexer-test ~/mcplexer-p2p serve --p2p --addr=127.0.0.1:13334 > ~/mcplexer-test.log 2>&1 &"
```

Verify the daemon came up + announced its PeerID:

```
ssh user@peer.local "sleep 2 && grep -m1 'p2p host listening' ~/mcplexer-test.log"
```

Expected output line includes `peerID=12D3KooW…` and a list of multiaddrs.

---

## Step 3 — Start the local daemon

In a separate terminal locally, with its own data dir + the standard port:

```
MCPLEXER_DATA_DIR=~/.mcplexer-test ./bin/mcplexer-p2p serve --p2p --addr=127.0.0.1:13335 &
```

Or just run via the existing setup if you're using the installed Electron app.

---

## Step 4 — Initiate pairing from machine A → machine B

On the LOCAL machine (machine A), call the pair-start REST endpoint:

```
curl -s -X POST http://127.0.0.1:13335/api/p2p/pair/start | tee /tmp/pair.json
```

Response:
```json
{"code":"123456", "qr_data_url":"data:image/png;base64,...", "expires_at":"..."}
```

The 6-digit `code` is what the peer needs.

---

## Step 5 — Complete pairing on machine B

Pass the code to machine B:

```
ssh user@peer.local "curl -s -X POST -H 'Content-Type: application/json' \
  -d '{\"code\":\"123456\"}' \
  http://127.0.0.1:13334/api/p2p/pair/complete"
```

Expected: `{"success": true, "peer_id": "12D3KooW...", "display_name": "..."}`.

---

## Step 6 — Confirm both sides see each other as paired

```
# A's view of B
curl -s http://127.0.0.1:13335/api/p2p/peers | jq

# B's view of A
ssh user@peer.local "curl -s http://127.0.0.1:13334/api/p2p/peers | jq"
```

Each side should list one peer with the other's PeerID. mDNS auto-discovery (M1.3) should kick in within a few seconds and `connection_mode` should flip to `direct`.

---

## Step 7 — Cross-machine `mesh__send` (the actual proof)

Send a mesh message from machine A targeting machine B's PeerID. From an MCP client connected to machine A's daemon (or via the test harness):

```
mcp__mcplexer__mesh__send {
  to_peer: "<B's PeerID>",
  kind: "event",
  content: "Hello from machine A at <timestamp>"
}
```

On machine B, fetch unread mesh messages:

```
mcp__mcplexer__mesh__receive { filter: "new" }
```

The message from A must appear in B's response. **This is the success criterion.**

---

## Step 8 — Bidirectional + dedupe sanity check

1. Send 3 messages A→B in quick succession.
2. Receive on B — all 3 must show up exactly once (no duplicates from the libp2p ack/retry).
3. Reply from B→A with `kind: "reply"` and `reply_to: "<A's msg id>"`.
4. Receive on A — must see B's reply threaded correctly.
5. Restart machine A's daemon (`pkill mcplexer-p2p` on machine A, then re-run Step 3). Pairing should survive — `curl /api/p2p/peers` on both sides still shows the pair.
6. Send another A→B message after the restart — B receives it.

---

## Step 9 — Tear-down

```
# Kill both daemons
pkill -f "mcplexer-p2p serve" 2>/dev/null
ssh user@peer.local "pkill -f 'mcplexer-p2p serve'"

# Optional: clean test data dirs
rm -rf ~/.mcplexer-test
ssh user@peer.local "rm -rf ~/.mcplexer-test"
```

---

## Troubleshooting

- **`Permission denied (publickey)` on SSH** — pubkey isn't on the peer's authorized_keys. See Pre-flight #2.
- **`connection refused` on the peer's curl** — peer's daemon didn't bind, check the log for a port-already-in-use error and pick a different `--addr`.
- **mDNS not finding peers** — most home networks block mDNS across VLANs. Confirm both machines are on the same subnet (`ifconfig | grep inet`).
- **Pairing code rejected** — codes are single-use + 5-minute TTL. Generate a fresh one.
- **Mesh message not arriving** — verify the target is `to_peer` not `to_role` (cross-machine doesn't use roles). Check the receiving daemon's log for "envelope rejected" entries (paired-peer scope check).
- **Architecture mismatch (`bad CPU type`)** — see Pre-flight #4. Cross-build with `GOOS=darwin GOARCH=amd64`.

---

## What "passes" looks like

- Pairing completes in < 30 seconds without surfacing any port number, IP, or PeerID prefix to the human (the 6-digit code is the only thing the user sees).
- Cross-machine `mesh__send` round-trip latency is < 100 ms on a LAN.
- `connection_mode = "direct"` after mDNS kicks in (~2-5 seconds post-pair).
- Both daemons survive a restart on either side without re-pairing.

If all six bullets hit, E1 (cross-machine mesh) is functionally proven.
