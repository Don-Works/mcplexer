---
name: mcplexer-google-onboarding
description: Use when connecting Google Calendar + Gmail to mcplexer per-workspace — set up ONE shared GCP OAuth app, mint a per-account OAuth token, and wire each Google account to its own mcplexer workspace.
---

# Per-workspace Google Calendar + Gmail onboarding for mcplexer

Each Google account → a different mcplexer workspace, each with its own OAuth token file, all behind ONE shared GCP OAuth app. Multi-tenant mapping lives in mcplexer (auth scopes), never in GCP.

## Architecture

- **ONE GCP OAuth app** — Desktop-app client, **External** consent, kept in **Testing**. Consent is per-USER, so one client serves personal gmail + many orgs. NEVER create a client per company.
- **Two self-hosted stdio MCP servers**, registered ONCE each:
  - Calendar: `@cocal/google-calendar-mcp` (`npx -y @cocal/google-calendar-mcp`), namespace `gcal`
  - Gmail: `@gongrzhe/server-gmail-autoauth-mcp` (`npx -y @gongrzhe/server-gmail-autoauth-mcp`), namespace `gmail`
- **Isolation lever (code fact):** a stdio child's env comes ONLY from its route's `auth_scope` secrets (`internal/auth/injector.go:96-114` — each secret KEY becomes an env-var NAME, value→value). The process is keyed by `(server, auth_scope)`. So: **one server row + N `type:"env"` auth_scopes (one per account) + one route per (workspace → server, scope).** The auth_scope IS the account boundary.
- **Per-account knob:** each account gets its own OAuth *token file*. The *client creds* file (`gcp-oauth.keys.json`) is shared by all accounts.

## ⚠️ Critical gotchas (learned the hard way)

1. **Token files MUST live OUTSIDE `~/.mcplexer`.** When `settings.sandbox_downstreams=true`, downstream servers run under `sandbox-exec`, which **denies reads to the whole `~/.mcplexer` subtree** (`internal/sandbox/sandbox.go` DefaultDenyPaths). A token there → server crashes on boot → discovery fails with **`initialize: no initialize response`**. **Use `~/.config/mcplexer-google/`** (dir `0700`, files `0600`).
2. **Gmail `auth` hardcodes loopback port 3000** (`server.listen(3000)`). If anything (a Next.js/`next-server` dev server, Docker) holds 3000, auth fails `EADDRINUSE` and the OAuth code lands on *that* app. Fix: free port 3000, OR patch the npx-cached `dist/index.js` `server.listen(3000)`→ a free port and pass that callback as the 3rd CLI arg (`npx … auth http://localhost:3600/oauth2callback`). The Calendar server uses an ephemeral port — no conflict.
3. **Per-account consent MUST be done in a REAL browser** (system Chrome). cmux's WKWebView can't render Google's login JS (reads `about:blank`). The Calendar server serves a friendly `http://localhost:<port>` landing page; the Gmail server prints the consent URL.
4. **Provision via the dashboard REST API on `:13333`**, authenticated by the **session cookie** (load `http://localhost:13333/`, then `fetch` same-origin). The admin `mcplexer__*` MCP tools are CWD-gated to `~/.mcplexer`, AND there is no MCP tool to write scope env values — REST is the path.
5. **GCP project ID ≠ project name.** The console auto-generates an ID (e.g. `graphical-cairn-498408-j9`) that does NOT match the name you type. Always read the real ID (project picker / resource manager) and use it in `?project=` URLs.

## Step 1 — GCP OAuth app (ONCE, in a real browser)

At `https://console.cloud.google.com` (2026 "Google Auth Platform" UI: Overview/Branding/Audience/Data Access/Clients):

1. Project picker → **NEW PROJECT** → make active. **Note the real project ID.** (If your account is under a managed Workspace org and you hit "you need additional access", grant your user **Owner** at the org level in IAM & Admin — Org-Admin alone doesn't include resource access. Allow a few min to propagate.)
2. Enable APIs: visit `…/apis/library/calendar-json.googleapis.com` → **ENABLE**; `…/apis/library/gmail.googleapis.com` → **ENABLE**. (Use the real project ID in the URL.)
3. APIs & Services → OAuth consent screen → **Get started**. Branding: app name + support email → Next. Audience: **External** → Next. Contact email → agree → **Create**. (Defaults to **Testing** — leave it.)
4. **Data Access** → Add or remove scopes → manually add:
   `https://www.googleapis.com/auth/calendar`
   `https://www.googleapis.com/auth/gmail.modify`
   `https://www.googleapis.com/auth/gmail.settings.basic`
   `https://www.googleapis.com/auth/userinfo.email`
   → Add to table → Update → **Save**. (Use `calendar.events` + `gmail.send` instead to minimise future verification/CASA.)
5. **Audience → Test users → Add users** → every account that will authenticate (≤100). Save. **Do NOT Publish.**
6. **Clients → Create client → Desktop app** → Create → **Download JSON** → save as `~/.config/mcplexer-google/gcp-oauth.keys.json` (`0600`). One file, shared by both servers.

## Step 2 — mint a token per account (ONCE per account, real browser)

```bash
mkdir -p ~/.config/mcplexer-google/cal ~/.config/mcplexer-google/gmail
chmod 700 ~/.config/mcplexer-google ~/.config/mcplexer-google/cal ~/.config/mcplexer-google/gmail

# Calendar (vary TOKEN_PATH + ACCOUNT_MODE per account; <acct> matches ^[a-z0-9_-]{1,64}$)
GOOGLE_OAUTH_CREDENTIALS=~/.config/mcplexer-google/gcp-oauth.keys.json \
GOOGLE_CALENDAR_MCP_TOKEN_PATH=~/.config/mcplexer-google/cal/<acct>.json \
GOOGLE_ACCOUNT_MODE=<acct> \
npx -y @cocal/google-calendar-mcp auth
#  → open http://localhost:<port> in a REAL browser → Authenticate → pick the account →
#    "Google hasn't verified this app" → Advanced → Go to … (unsafe) → Allow.

# Gmail (SEQUENTIAL; free port 3000 first, or patch the cached listen port as in gotcha #2)
GMAIL_OAUTH_PATH=~/.config/mcplexer-google/gcp-oauth.keys.json \
GMAIL_CREDENTIALS_PATH=~/.config/mcplexer-google/gmail/<acct>.json \
npx -y @gongrzhe/server-gmail-autoauth-mcp auth   # add 'http://localhost:<freeport>/oauth2callback' if patched
chmod 600 ~/.config/mcplexer-google/gmail/<acct>.json   # gongrzhe writes 0644 — lock it
```

## Step 3 — provision in mcplexer (dashboard REST :13333, cookie-authed)

Load `http://localhost:13333/` in a browser context (sets the `mcplexer_session` cookie), then `fetch` same-origin. Per account:

- **Server rows — ONCE total** (shared across accounts), via `POST /api/v1/downstreams`:
  `{name:"google-calendar",transport:"stdio",command:"/opt/homebrew/bin/npx",args:["-y","@cocal/google-calendar-mcp"],tool_namespace:"gcal",discovery:"dynamic",restart_policy:"on-failure",max_instances:1,idle_timeout_sec:300}` and the gmail equivalent (`@gongrzhe/server-gmail-autoauth-mcp`, ns `gmail`). Use the **absolute** npx path — the launchd daemon's PATH differs.
- **Auth scopes — per account** (`POST /api/v1/auth-scopes`): `{name:"gcal-<acct>",type:"env"}` and `{name:"gmail-<acct>",type:"env"}`.
- **Secrets = env vars** (`PUT /api/v1/auth-scopes/<id>/secrets {key,value}`, one per key):
  - gcal scope: `GOOGLE_OAUTH_CREDENTIALS`, `GOOGLE_CALENDAR_MCP_TOKEN_PATH`, `GOOGLE_ACCOUNT_MODE`
  - gmail scope: `GMAIL_OAUTH_PATH`, `GMAIL_CREDENTIALS_PATH`
  (all absolute paths under `~/.config/mcplexer-google/`)
- **Routes — per (workspace → server, scope)** (`POST /api/v1/routes`): `{name,workspace_id,downstream_server_id,auth_scope_id,policy:"allow",path_glob:"**",tool_match:["*"]}` — one for calendar, one for gmail.

## Step 4 — verify (don't trust row creation)

- `POST /api/v1/downstreams/<server-id>/discover` → **200** + populated `capabilities_cache.tools` (13 calendar tools, 19 gmail tools) means spawn + env + token all wired. A **502 "no initialize response"** = the server crashed on boot → almost always the sandbox/`~/.mcplexer` path gotcha (#1).
- Live proof: call `gcal__list-events` / `gmail__search_emails` from a session in that workspace, or run the server directly with the account env and `tools/call` `list-calendars` / `list_email_labels` over stdio — you should get real calendars/labels back.

## Future upgrade

A native mcplexer Google OAuth *provider* (gateway-hosted callback + central token store via the existing `create_oauth_provider` + OAuth wizard) would replace the per-server loopback `auth` dance and the token-file juggling. Bigger build; the above gets accounts connected today.
