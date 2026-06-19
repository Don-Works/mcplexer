# The MCPlexer Brain

> Git-backed, Markdown-canonical state. Your tasks, memories, workspace
> config, and skills become editable `.md` files in a git repo; the gateway
> indexes them into SQLite for fast querying. The *same* state is what you
> open in VSCode, what the dashboard renders, what `task__*`/`memory__*`
> query, and what `git diff`/`blame`/`revert` can act on.

**Status:** opt-in, off by default. When the flag is off MCPlexer behaves
exactly as today (single `mcplexer.db`, no files, no git) — byte-for-byte.

---

## How it works

### Files are canonical; SQLite is a derived index

The brain repo (default `~/mcplexer-brain/`, a *sibling* of `~/.mcplexer/`
so the DB-lockdown hook is untouched) is the source of truth for
Brain-canonical entities:

| Entity | Where it lives in the repo |
|---|---|
| Task | `workspaces/<ws>/tasks/<ulid>-<slug>.md` |
| Task notes | inline in the task `.md` under a `## Notes` section |
| Memory note | `workspaces/<ws>/memory/<name>.md` |
| Memory fact | `workspaces/<ws>/memory/facts/<name>.md` (bi-temporal frontmatter) |
| Workspace | `workspaces/<ws>/workspace.md` |
| Per-workspace config | `workspaces/<ws>/config/{routes,downstream,…}.yaml` (`source=brain`) |
| Skills | `global/skills/<name>/v<N>/SKILL.md` (one-way export) |
| Secrets | `global/secrets/scopes.enc.yaml` (SOPS+age, value-only) |

SQLite is a **gitignored, rebuildable index** — a pure function of the file
tree. High-churn / ephemeral data (audit, sessions, mesh, peers,
embeddings, runs, telemetry) stays DB-only and is never serialized.

### The sync engine (two directions)

- **Inbound (file → index):** an `fsnotify` watcher (parent-dir watch,
  300 ms debounce, `Chmod` dropped) detects a save, computes the file's
  SHA-256, skips it if unchanged, else parses + **validates** the
  frontmatter (Astro/Zod discipline — a file that fails validation is *not*
  indexed; the failure surfaces in the dashboard's Brain tile rather than
  letting the index lie), and upserts through the existing `Store` methods
  so FTS5 triggers + bi-temporal logic fire unchanged.
- **Outbound (index → file):** when an agent tool mutates a Brain-canonical
  entity, the serializer writes the file back, guarded by a content-hash
  CAS gate (it never clobbers a concurrent human edit — a mismatch writes a
  `.conflict` sidecar and records an error), an atomic temp+rename, and
  self-write suppression (so the resulting fsnotify event is recognised and
  skipped — no reindex loop).

### Git backplane

Mutations + sync shell out to the system `git` binary (go-git can't do
non-fast-forward merge/rebase/gc). The daemon **autocommits locally on
idle** (debounced, scoped `git add` of daemon-owned paths only, machine
identity `MCPlexer Daemon <daemon@mcplexer.local>` so your own commits stay
distinct). **Push is manual** (deploy-hygiene) — via the dashboard Brain
tile or `mcplexer__brain_push`, which runs `git pull --rebase --autostash`
then `git push`; rebase conflicts are surfaced, never auto-resolved.

---

## Editing files

You have three equally-canonical front-ends:

1. **VSCode (power user):** open the brain repo, edit any `.md`/`.yaml`
   directly. Change `status: doing` to `status: review` in a task's
   frontmatter, save — within ~500 ms the gateway reparses and updates the
   row; the next `task__list` reflects it. The dashboard tile has an
   "Open in VSCode" button.
2. **Claude / MCP:** `task__*`, `memory__*`, `mcpx__execute_code` query the
   index and mutate via Store methods → the same files. Bulk edits,
   cross-workspace queries — it's just SQL + files underneath.
3. **Dashboard (non-technical):** browse + edit typed records in forms; saves
   write through the same serializer + hash-CAS. (Notion-like editor lands
   in a later milestone; the status tile at **/brain** ships now.)

**Frontmatter rules:** the gateway owns the structured fields (emitted from a
Go struct so key order is deterministic → clean diffs). Edit the values, not
the key layout. `id` is immutable and must equal the filename prefix. `status`
is validated against the workspace's `task-status.yaml` vocab. A file that
fails validation is left un-indexed and shows up under "Validation errors" in
the Brain tile — fix the frontmatter and save again.

---

## Enabling the Brain (one-click, parity-verified, reversible)

1. **Enable the flag.** Set `MCPLEXER_BRAIN_ENABLED=1` (launchd plist
   `EnvironmentVariables`, or `settings.brain_enabled`) and restart the
   daemon (`launchctl kickstart -k gui/$UID/com.mcplexer.daemon`).
2. **Init the repo.** From a terminal under `~/.mcplexer`, run the CWD-gated
   admin tool **`mcplexer__brain_init`** — it takes a `backup.Create`
   snapshot *first* (so the whole operation is rollback-able), scaffolds the
   repo layout (`.gitignore`/`.gitattributes`/`brain.json`/`README.md` + dir
   skeleton, idempotent — never clobbers existing files), then `git init` +
   an initial commit.
3. **Import with verification.** Run **`mcplexer__brain_import`** — it
   serializes every Brain-canonical DB row (workspaces, tasks, memories,
   skills) to its file, reindexes from the resulting tree, and **asserts
   row-count + content-hash parity** against the live DB. If anything
   mismatches it **aborts** (reports the drift, leaves the DB authoritative,
   does *not* enable anything). Only a `parity_ok: true` report blesses the
   import.
4. **Migrate secrets (optional).** `mcplexer__brain_migrate_secrets`
   re-encrypts auth-scope values + OAuth client secrets into the SOPS+age
   `scopes.enc.yaml` (value-only, round-trip verified). DB blobs are left in
   place for dual-read fallback.
5. **Confirm.** Open the dashboard **/brain** tile or run
   `mcplexer__brain_verify` — zero drift means the index faithfully reflects
   the files.

During rollout, Brain-canonical writes go to **both** the file *and* the DB
row (the DB row is now "the index"). Secrets dual-read: the SOPS file is
tried first, falling back to the age-DB blob.

---

## Rollback / disabling

Nothing is destructive. To turn the Brain off:

- **`mcplexer__brain_disable`** flips `settings.brain_enabled=false`
  (preserving every other settings key). On the next daemon restart the
  gateway resumes reading the authoritative DB exactly as before. The brain
  repo is **left on disk** (and on its remote) as a readable archive.
- Because the import was parity-verified, no state is lost on rollback — the
  DB was authoritative the whole time.
- To roll back the *DB itself* (e.g. an import went wrong before you trusted
  it), restore the snapshot `brain_init` took first: `mcplexer__list_backups`
  → `mcplexer__restore_backup` (or the dashboard Backups page). A
  pre-restore snapshot is taken automatically so even the restore is
  reversible.
- To re-enable later: flip the flag back on, restart, and the startup
  reindex rebuilds the index from the files on disk.

---

## Admin tools (CWD-gated under `~/.mcplexer`)

| Tool | What it does |
|---|---|
| `mcplexer__brain_init` | Snapshot + scaffold + git init (idempotent) |
| `mcplexer__brain_import` | Parity-verified one-way DB→files import; aborts on mismatch |
| `mcplexer__brain_verify` | Re-derive rows from files, diff vs DB, report drift |
| `mcplexer__brain_status` | git ahead/behind/dirty + branch + last commit |
| `mcplexer__brain_push` | `pull --rebase --autostash` then `push`; surfaces conflicts |
| `mcplexer__brain_migrate_secrets` | Re-encrypt secrets into SOPS+age (value-only) |
| `mcplexer__brain_disable` | Flip the flag off; leave the repo on disk |

## Dashboard

The **/brain** tile shows the enable flag, the repo path (with an
**Open in VSCode** link), git status (branch / clean-or-dirty /
ahead-behind), a manual **Push** button, a **Verify (check drift)** action,
and the list of files that failed frontmatter validation.

---

## Further Reading

This page is the public overview for the Brain feature. Implementation details
live in the `internal/brain/` package and related tests.
