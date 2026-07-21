# Contributing to MCPlexer

Thanks for helping improve MCPlexer. This project is a local-first developer tool, so useful contributions usually make setup clearer, routing safer, or the dashboard easier to reason about.

## Setup

Prerequisites:

- Go 1.25+
- Node 20.19+ or Node 22.12+
- Task from <https://taskfile.dev>

```bash
git clone https://github.com/don-works/mcplexer.git
cd mcplexer
task install
```

## Daily Development

```bash
task dev              # Go server in HTTP mode on :3333
cd web && npm run dev # frontend dev server with hot reload
task run              # build + start local daemon
```

## Before Opening a PR

```bash
task test
task lint
task public-hygiene
cd web && npm run build
```

Include a clear summary, verification notes, and screenshots for user-facing dashboard changes.
The generated dashboard bundle under `internal/web/dist` is ignored; do not
commit anything from that directory.

## Project Rules

- Configure MCPlexer through MCP tools, YAML, API, or UI. Do not edit the local database directly.
- Keep security-sensitive state under `~/.mcplexer/` and do not commit local secrets, tokens, logs, or generated private state.
- This is a public FOSS repository. Use neutral documentation/test fixtures;
  never commit real customer, staff, host, workspace, tailnet, or fleet
  identifiers—even on short-lived branches or in commit messages.
- Prefer small, focused changes with tests proportional to the blast radius.
- Preserve deny-first routing behavior and approval boundaries.
- For frontend work, keep the interface dense, explicit, and operator-friendly.

## Security Issues

Report vulnerabilities privately. See [SECURITY.md](SECURITY.md).
