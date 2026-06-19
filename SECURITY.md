# Security Policy

MCPlexer is a local-first MCP gateway. The security model is built around a few core rules:

- No cloud service is required for routing, audit logs, approvals, or secrets.
- In stdio mode, workspace policy is based on the kernel-reported working directory.
- Routing is deny-first: a tool call must match an allow route before it can run.
- Secrets are encrypted at rest with age and protected by local file permissions.
- The HTTP control plane uses a per-install API token stored at `~/.mcplexer/api-key`.
- Downstream process spawning validates commands and strips sensitive environment variables.
- Approval requests cannot be self-approved by the same session that created them.

## Supported Versions

Security fixes target the latest release on `main`. Older revisions are not maintained separately yet.

## Reporting Vulnerabilities

Do not open public issues for vulnerabilities.

Open a private security advisory at
`https://github.com/don-works/mcplexer/security/advisories/new` with:

- Affected version or commit
- Steps to reproduce
- Expected and actual behavior
- Impact and any suggested fix

We aim to acknowledge reports within 48 hours and coordinate disclosure once a fix is available.

## In Scope

- The `mcplexer` Go binary and embedded dashboard
- MCP stdio and HTTP transports
- Routing, approvals, and audit logging
- Downstream process spawning and command validation
- Secret storage and credential injection
- REST, SSE, and WebSocket endpoints

## Out of Scope

- Vulnerabilities in downstream MCP servers
- AI client bugs outside MCPlexer control
- Social engineering or physical attacks
