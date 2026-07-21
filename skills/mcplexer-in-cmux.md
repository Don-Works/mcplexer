---
name: mcplexer-in-cmux
description: Open and drive the mcplexer web UI inside cmux's built-in browser tab. The OS-level integration story between mcplexer and cmux until the native Swift tab (M3.2) ships.
---

# mcplexer-in-cmux

cmux's built-in browser drives mcplexer's web UI. mcplexer is **standalone-first**; cmux just shows the UI in one of its tabs. No process coupling, no shared build, both products keep their identity.

## Quick reference

```
# Open mcplexer dashboard in a cmux browser tab:
cmux-browser → http://127.0.0.1:13333

# Specific pages (deep-linkable, all have stable data-testid selectors):
http://127.0.0.1:13333/pairing            # pair another device
http://127.0.0.1:13333/create-mcp         # custom MCP wizard + OpenAPI import
http://127.0.0.1:13333/audit              # audit log
http://127.0.0.1:13333/audit?status=denied&since=2026-04-29   # filtered
http://127.0.0.1:13333/dashboard?range=24h
http://127.0.0.1:13333/descriptions?status=pending
```

## When to use

- User wants to manage mcplexer (install MCP, pair, share skills, approve secret prompts) without leaving cmux.
- The cmux integration story is requested — this is the supported integration until M3.2 native tab can build.

## When NOT to use

- Direct tool calls — go through the `mcplexer` MCP server.
- Reading config — `~/.mcplexer/mcplexer.yaml` on disk.
- Starting the daemon autonomously — ask the user.

## Why this exists

The full native cmux tab integration via WKWebView is still experimental. Until that resolves, this skill is the integration: cmux's existing browser already does everything the native tab would do, just in a Chromium frame.

The integration point: **mcplexer remains standalone**. This OS-level composition keeps that promise — no shared process, no shared build, perfect product independence.

## Driving the UI

Every actionable element has `data-testid="<noun>-<verb>"` (M5.1 a11y pass). Examples: `pair-show-code-btn`, `secret-submit`, `mcp-install-btn`, `nav-pairing`, `peer-revoke-<peerID>`.

Filter state is in query params for deep-linking. Tab state is in route segments. Modal opens are routes (`/pair/show`, `/pair/enter`).

## Examples

**Pair the local mcplexer with another machine:**
1. cmux-browser → `http://127.0.0.1:13333/pairing`
2. Click `pair-show-code-btn` → read the 6-digit code from the modal
3. SSH to the other machine, `curl -X POST .../api/p2p/pair/complete` with the code+QR payload
4. Refresh peers list — both sides should show each other

**Install a signed skill bundle:**
1. cmux-browser → `http://127.0.0.1:13333/skills` (or use CLI: `mcplexer skill install path/to/skill.mcskill`)
2. Read the capability review screen
3. Click `skill-install-confirm` if the signer is trusted

**Trigger a secret prompt during agent work:**
- Agent calls `secret__prompt({reason, label})` → mcplexer fires a notification
- User finds the open mcplexer tab in cmux, clicks the modal, types the secret, clicks `secret-submit`
- Agent receives the file path back, never the value
