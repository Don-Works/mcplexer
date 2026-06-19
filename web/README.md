# MCPlexer Web Dashboard

This is the React SPA embedded into the MCPlexer Go binary.

## Stack

- React 19 and TypeScript
- Vite
- Tailwind CSS v4
- shadcn/ui-style primitives
- SSE streams for audit, approvals, notifications, and worker status

## Development

```bash
cd web
npm install
npm run dev
```

The Vite dev server runs on `http://localhost:5173` and proxies API calls to the Go backend.

For the backend:

```bash
task dev
```

## Build

```bash
cd web
npm run build
```

The root `task build`, `task build-p2p`, and `task web-build` commands also build the dashboard and refresh the embedded assets under `internal/web/dist`.
Those generated assets are intentionally ignored by git; only
`internal/web/dist/.gitkeep` is tracked so Go packages still compile before a
web build has run.

## Main Surfaces

- Setup: connect AI clients and add integrations.
- Workspace access: decide which tools each workspace can use.
- Monitor: dashboard, notifications, and audit history.
- Inbox: approvals and worker proposals waiting on a human.
- Automation: scheduled workers and delegated one-shot runs.
- Knowledge: Brain, Memory, Tasks, and Skills.
- Network: mesh, pairing, and linked workspaces.
- Settings: safety rules, backups, advanced credentials, and preferences.

## Verification

Run:

```bash
npm run build
```

For UI changes, also smoke-test the relevant routes through a real MCPlexer server (`task dev`) at desktop and mobile widths.
