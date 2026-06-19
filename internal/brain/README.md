# internal/brain

Git-backed, Markdown-canonical state repository. The file tree is the source
of truth; SQLite is a derived, rebuildable index. See `docs/brain.md` and the
package doc in `brain.go` for the sync-engine architecture (indexer inbound,
serializer outbound, hash-CAS, self-write suppression).

## Obsidian vault direction (intended end-state)

The brain's destiny is an **Obsidian-style vault**: a folder of human-readable
Markdown a person can open in Obsidian (or any editor) and navigate without
tooling. That implies, incrementally:

- **Human filenames.** Record files are named by slugified title/name
  (`tasks/<ulid>-<slug>.md`, `memory/<slug>.md`, `crm/people/<slug>.md`),
  never by raw free-form text. The slug is also load-bearing security: a raw
  name join once let a memory named `…example/memory-repo (private)`
  create an unindexable subdirectory. `recordStem` (serializer_io.go) is the
  single stem derivation; `ValidateMemory`/`ValidatePerson` reject names
  containing `/`, `\`, or `..` on the inbound path.
- **Frontmatter as the schema.** YAML frontmatter (`schema: task/v1`, …)
  stays the structured layer — Obsidian renders it as Properties.
- **Wikilink-friendly cross-references.** `entities:` links should eventually
  render as `[[…]]` wikilinks in bodies so graph view and backlinks work.
- **Readable tree.** Workspace dirs are still raw UUIDs; the generated
  vault-root `INDEX.md` (vault_index.go, refreshed on every full reindex)
  maps each dir to its display name + record counts with relative links.
  A future pass may rename dirs to slugs via a proper migration — INDEX.md
  is the cheap, non-destructive bridge until then.
- **No surprises for hand-edits.** Flat record dirs are an invariant; the
  reindex sweep (indexer_repair.go) logs unexpected subdirectories loudly
  and repairs mis-pathed `.md` files back into the flat layout instead of
  silently skipping them.

This pass (v1.0 hardening) covers security + truthfulness + readability
basics; the full vault reframe (wikilinks, slug dirs) comes later.
