# MCPlexer

[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-3b82f6.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25%2B-3b82f6.svg)](https://go.dev)
[![Website](https://img.shields.io/badge/website-mcplexer.com-3b82f6.svg)](https://mcplexer.com)

Directory-scoped MCP routing and tool control. Like [direnv](https://direnv.net) for MCP.

Route, scope, and secure every AI tool call based on your working directory. Local-first. Auditable. Open source.

**[Website](https://mcplexer.com)** &middot; **[Issues](https://github.com/don-works/mcplexer/issues)**

## What is MCPlexer?

MCPlexer is an MCP gateway that sits between your AI client (Claude Desktop, Claude Code, etc.) and your downstream MCP servers. It multiplexes tool calls across servers with workspace-based routing, human-in-the-loop approvals, OAuth credential injection, and full audit logging.

Your working directory determines which policies apply — tamper-proof in stdio mode, because MCPlexer reads CWD directly from the kernel.

## Features

- **Directory-scoped routing** — workspaces bind to directory trees, CWD determines policies
- **Shared local code index** — citation-ready source search, symbols, dependency/test maps, and task context packs reused across authorized workspaces for the same repo; dependency/build trees are excluded
- **Tool approvals** — per-route approval requirements with SSE streaming to the dashboard
- **OAuth 2.0 + PKCE** — built-in flows with provider templates (GitHub, Linear, Google, ClickUp), automatic token refresh
- **Audit trail** — every tool call logged with workspace, route, auth scope, latency, and parameter redaction
- **Self-configurable** — 19 MCP tools via `mcplexer control-server` for AI-native configuration
- **Installable PWA** — dashboard installs from Chrome / Edge / Arc as a standalone desktop app, with OS notifications for approvals + mesh signals
- **Web dashboard** — real-time metrics, approval queue, audit stream, config editor
- **age encryption** — secrets encrypted at rest with [filippo.io/age](https://filippo.io/age), auto-generated keys
- **Dry run** — test routing decisions without execution via CLI or API
- **Pure Go** — single binary, zero CGO, runs anywhere Go compiles to

## Quick Start

### Install from a release

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/Don-Works/mcplexer/main/scripts/install-release.sh | bash
```

```powershell
# Windows PowerShell
irm https://raw.githubusercontent.com/Don-Works/mcplexer/main/scripts/install-release.ps1 | iex
```

The installer downloads the latest GitHub Release archive for your OS/arch,
verifies `checksums.txt`, installs the binary into `~/.mcplexer/bin`, and runs
`mcplexer setup`.

`mcplexer setup` configures detected MCP clients and starts the local daemon.
On macOS it can install a `launchd` agent. On Linux with `systemctl --user` it
can install `~/.config/systemd/user/mcplexer.service`. On other hosts it falls
back to the built-in background daemon.

Release archives are available for:

| OS | Architectures | Format |
| --- | --- | --- |
| macOS | amd64, arm64 | `.tar.gz` |
| Linux | amd64, arm64 | `.tar.gz` |
| Windows | amd64, arm64 | `.zip` |

Install a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/Don-Works/mcplexer/main/scripts/install-release.sh | bash -s -- --version v0.1.3
```

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/Don-Works/mcplexer/main/scripts/install-release.ps1))) -Version v0.1.3
```

### Build from source

**Prerequisites:** Go 1.25+, Node 20.19+ or Node 22.12+.

```bash
git clone https://github.com/don-works/mcplexer.git
cd mcplexer
task install
```

`task install` builds the daemon, installs the service manager integration where available (`launchd` on macOS, systemd user service on Linux), configures any detected MCP clients (Claude Desktop, Claude Code, Cursor, Windsurf, Codex, OpenCode, Gemini CLI), and opens the dashboard at <http://localhost:3333>.

**Install the dashboard as a desktop app:** in Chrome / Edge / Arc, click the install icon in the address bar (or "Install MCPlexer…" from the menu). The PWA runs in a standalone window with its own Dock icon, and fires OS notifications for approvals and high-priority mesh signals while open.

**Install the mobile PWA over Tailscale:** expose the daemon only on your tailnet and allow the Tailscale hostname as a browser origin:

```bash
MCPLEXER_HTTP_ADDR=0.0.0.0:3333 \
MCPLEXER_TRUSTED_HOSTS=my-mac.tailnet-name.ts.net \
mcplexer serve
```

Open `https://my-mac.tailnet-name.ts.net/app` from the phone, then install it from the browser menu. Use the HTTPS Tailscale hostname for mobile installability and notification support; the bare hostname belongs in `MCPLEXER_TRUSTED_HOSTS`.

### Or just the binary

```bash
go install github.com/don-works/mcplexer/cmd/mcplexer@latest
mcplexer setup
```

### Upgrading

```bash
task upgrade
```

In-place atomic swap of the daemon binary, then restart through the installed service manager (`launchd` or systemd user service) or the built-in daemon fallback. ~1-2s downtime. Reserve `task install` for first-time installs.

### Manual setup

```bash
# Initialize database and config
mcplexer init

# Run as MCP server (stdio mode for Claude Code)
mcplexer serve --mode=stdio

# Run with web UI on the default port
mcplexer serve --mode=http --addr=127.0.0.1:3333

# Run as background daemon with Unix socket
mcplexer daemon start --addr=127.0.0.1:3333 --socket=/tmp/mcplexer.sock
```

### API authentication

Every HTTP API call requires a token. The daemon generates one at first startup and writes it to `~/.mcplexer/api-key` with mode 0600. Two ways to use it:

- **Web UI** — the SPA receives the token automatically as a session cookie when you load the dashboard. No manual step.
- **CLI / scripts** — read the file and send `Authorization: Bearer $(cat ~/.mcplexer/api-key)` on every request.

Health (`/api/v1/health`) and OAuth callbacks are exempt; everything else returns 401 without a valid token.

### Network exposure

MCPlexer binds to `127.0.0.1:3333` by default — only accessible from the same machine. If you bind to `0.0.0.0` or a LAN address, be aware:

- **The API token is the only authentication.** Anyone who can reach the port can make fully privileged API calls if they have the token. There is no per-user auth, rate limiting, or CSRF protection beyond the token.
- **Use a reverse proxy for internet exposure.** Do not expose the daemon directly. Put it behind nginx, Caddy, or Tailscale with TLS termination:

```nginx
# nginx example
server {
    listen 443 ssl;
    server_name mcplexer.example.com;

    ssl_certificate     /etc/ssl/certs/mcplexer.pem;
    ssl_certificate_key /etc/ssl/private/mcplexer.key;

    location / {
        proxy_pass http://127.0.0.1:3333;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # WebSocket + SSE support
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 86400;
    }
}
```

```bash
# Caddy example (auto-TLS)
mcplexer.example.com {
    reverse_proxy localhost:3333
}
```

- **Set `MCPLEXER_TRUSTED_HOSTS`** to your external hostname so the Origin check passes for browser requests through the proxy.
- **Set `MCPLEXER_EXTERNAL_URL`** or `MCPLEXER_PUBLIC_URL` to the canonical HTTPS URL for OAuth callbacks and PWA installability.
- **Tailscale is the simplest secure option** for personal remote access — no TLS config needed, just bind to `0.0.0.0:3333` and use your tailnet hostname.

See [SECURITY.md](SECURITY.md) for the full security policy and vulnerability reporting process.

## Configuration

MCPlexer supports three configuration methods:

| Method | Use case |
|--------|----------|
| **Web UI + REST API** | Visual management, real-time dashboard. Installable as a PWA. |
| **YAML config** | Version-controlled, seeds database on startup |
| **MCP control server** | AI-native configuration from Claude or any MCP client |

### YAML config

Default location: `~/.mcplexer/mcplexer.yaml`

```yaml
downstream_servers:
  - id: github
    name: GitHub MCP
    transport: stdio
    command: npx
    args: ["-y", "@modelcontextprotocol/server-github"]
    tool_namespace: github
```

YAML-sourced items are auto-pruned when removed from the config file. Items created via API or UI persist independently.

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `MCPLEXER_MODE` | `stdio` | Transport mode: `stdio` or `http` |
| `MCPLEXER_HTTP_ADDR` | `127.0.0.1:3333` | HTTP listen address |
| `MCPLEXER_TRUSTED_HOSTS` | auto local hostnames | Extra browser Origin hostnames for LAN or Tailscale UI access |
| `MCPLEXER_DB_DSN` | `~/.mcplexer/mcplexer.db` | Database path |
| `MCPLEXER_CONFIG` | `~/.mcplexer/mcplexer.yaml` | Config file path |
| `MCPLEXER_AGE_KEY` | auto-generated | Path to age identity file |
| `MCPLEXER_API_TOKEN_PATH` | `~/.mcplexer/api-key` | Path to HTTP API auth token |
| `MCPLEXER_SOCKET_PATH` | — | Unix socket path for multi-client mode |
| `MCPLEXER_EXTERNAL_URL` | — | External URL for OAuth callbacks |
| `MCPLEXER_PUBLIC_URL` | `MCPLEXER_EXTERNAL_URL` | Canonical HTTPS browser/PWA URL |
| `MCPLEXER_WEB_PUSH_SUBJECT` | `MCPLEXER_PUBLIC_URL` | VAPID subject for standards-based browser push |
| `MCPLEXER_LOG_LEVEL` | `info` | Log level: debug, info, warn, error |
| `MCPLEXER_CODE_INDEX_EMBED_PROVIDER` | `none` | Optional semantic code search: `none`, `local`, or explicit loopback auto-detection with `auto` |
| `MCPLEXER_CODE_INDEX_EMBED_BASE_URL` | — | OpenAI-compatible loopback `/v1` URL used only for code embeddings |
| `MCPLEXER_CODE_INDEX_EMBED_MODEL` | — | Local code embedding model id; changing it triggers background vector backfill |
| `MCPLEXER_CODE_INDEX_EMBED_API_KEY` | — | Optional bearer token for the loopback code-embedding server; never reused from cloud/memory settings |

See [Code index](docs/code-index.md) for tools, sharing, exclusions, privacy, and local semantic-search setup.

## CLI Commands

```
mcplexer serve          Run MCP server (default: stdio mode)
mcplexer connect        Bridge stdio to the daemon's local IPC endpoint
mcplexer setup          One-command Claude Desktop / Claude Code integration
mcplexer init           Initialize database and default config
mcplexer status         Show workspaces, servers, auth scopes, sessions
mcplexer version        Show build version, or JSON with --json
mcplexer dry-run        Test routing rules without execution
mcplexer secret         Manage encrypted secrets (put/get/list/delete)
mcplexer daemon         Background process management (start/stop/status/logs/uninstall)
mcplexer skill          Manage skill packs (pack/share/install/registry)
mcplexer control-server Run MCP control protocol server
```

## How Routing Works

1. **CWD resolution** — in stdio mode, MCPlexer reads `os.Getwd()` to determine the client's working directory
2. **Workspace matching** — the most specific matching workspace wins (longest path prefix)
3. **Rule evaluation** — rules are sorted by path glob specificity, then tool specificity, then priority
4. **Deny-first** — deny rules stop the chain immediately
5. **Approval** — if the matching rule requires approval, the request is held until resolved via the dashboard
6. **Dispatch** — tool call is forwarded to the downstream server with injected credentials

## Project Structure

```
cmd/mcplexer/       Entry point, CLI subcommands, config loading
internal/
  store/            Store interface + domain models (DB-agnostic)
  store/sqlite/     SQLite implementation (pure Go, no CGO)
  gateway/          MCP server, JSON-RPC protocol, tool aggregation
  routing/          Route matching engine
  downstream/       Process lifecycle manager
  auth/             Credential injection
  secrets/          age encryption + secret storage
  audit/            Audit logging with redaction
  approval/         Tool call approval system
  config/           YAML config loader, validation, seeding
  api/              REST API handlers (/api/v1/)
  oauth/            OAuth 2.0 flow management
  control/          MCP control protocol server
  web/              go:embed for SPA static files
web/                React SPA source (Vite + TypeScript + Tailwind)
site/               Marketing website (Next.js, deployed to GitHub Pages)
```

## Task Commands

```bash
# Install / upgrade
task install          # build + daemon setup + MCP-client wiring + PWA
task upgrade          # in-place atomic swap + launchctl kickstart
task install-cli      # slim build + setup for headless boxes

# Development
task run              # build + start daemon locally
task dev              # run Go server in HTTP mode on :3333
cd web && npm run dev # web UI dev server with hot reload

# Build / test / verify
task build            # slim Go binary + web UI
task build-p2p        # p2p-enabled Go binary + web UI
task release-artifacts # macOS/Linux/Windows release archives + checksums
task test             # run Go tests
task lint             # go vet + golangci-lint
```

`make` remains as a compatibility shim, but `task <name>` is the canonical developer interface.

**Requirements:** Go 1.25+, Node 20.19+ or Node 22.12+.

## Tech Stack

- **Backend:** Go, SQLite ([modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite)), net/http
- **Frontend:** React 19, TypeScript, Vite, Tailwind CSS v4, shadcn/ui
- **Encryption:** [filippo.io/age](https://filippo.io/age) for secrets at rest
- **Config:** YAML ([gopkg.in/yaml.v3](https://pkg.go.dev/gopkg.in/yaml.v3))

## Ecosystem

MCPlexer is part of [Don Works](https://donworks.co.uk/?utm_source=mcplexer&utm_medium=readme&utm_campaign=donworks_oss) — open source by [Revitt](https://revitt.co/?utm_source=mcplexer&utm_medium=readme&utm_campaign=donworks_oss).

- **[brw](https://brw.donworks.co.uk/?utm_source=mcplexer&utm_medium=readme&utm_campaign=donworks_oss)** — semantic browser control for agents: a real, visible Chrome exposed over MCP and HTTP ([github.com/Don-Works/brw](https://github.com/Don-Works/brw)).
- **[Don Works](https://donworks.co.uk/?utm_source=mcplexer&utm_medium=readme&utm_campaign=donworks_oss)** — the umbrella brand for Revitt's open-source work ([github.com/Don-Works](https://github.com/Don-Works)).
- **[Revitt](https://revitt.co/?utm_source=mcplexer&utm_medium=readme&utm_campaign=donworks_oss)** — the parent company behind Don Works.

## License

MCPlexer is licensed under the GNU Affero General Public License v3.0 or
later (`AGPL-3.0-or-later`). See [LICENSE](LICENSE).

Commercial license exceptions are available from Don Works for organizations
that need non-AGPL terms.
