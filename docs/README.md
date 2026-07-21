# MCPlexer Documentation

This directory contains design notes, architecture records, and operational guides for MCPlexer.

## Start Here

| Document | Purpose |
| --- | --- |
| [mcplexer-features.md](mcplexer-features.md) | Broad feature and architecture overview |
| [mcp-spec-conformance.md](mcp-spec-conformance.md) | MCP protocol conformance notes |
| [harnesses.md](harnesses.md) | Unified setup notes for MCP-wired harnesses, server-prefixed clients, and Pi |
| [memory.md](memory.md) | Persistent memory design |
| [code-index.md](code-index.md) | Shared local code search, context packs, exclusions, and optional local embeddings |
| [data-workbench.md](data-workbench.md) | Scratch SQLite/RAG workbench tools |
| [token-preservation-delegation.md](token-preservation-delegation.md) | Delegation and worker context model |
| [nearly-autonomous-coding.md](nearly-autonomous-coding.md) | Safe near-autonomous coding loop using tasks, workers, mesh, and memory |
| [p2p-network-modes.md](p2p-network-modes.md) | Pairing, mesh, and peer networking modes |
| [ui-audit.md](ui-audit.md) | UI audit notes and improvement backlog |
| [ui-full-pass-2026-06-26.md](ui-full-pass-2026-06-26.md) | Full browser-route UI audit from the 2026-06-26 polish pass |

## Design Areas

- `adr/` - architecture decision records
- `design/` - focused design proposals
- `skill-format.md` and `skills-hub-deploy-runbook.md` - skill packaging and distribution
- `integrations.md` and addon-specific notes - third-party integration details

Some documents are active design notes rather than finished user docs. Treat them as implementation context unless the README or dashboard links to them as an end-user guide.
