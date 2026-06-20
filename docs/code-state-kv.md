# Code-mode KV state (`kv__*`)

Each `mcpx__execute_code` call runs in a **fresh goja VM**, so any JavaScript
variable you build in one call — a parsed dataset, a set of matched records, a
computed index — vanishes when that call returns. `globalThis.__X = ...` does not
survive to the next call.

The `kv` namespace is a workspace-scoped key/value store for arbitrary JSON
values that **does** survive across calls. Build an expensive dataset once, then
rehydrate it as a plain JS value in later calls instead of recomputing it.

```js
// Call 1 — build once (the expensive part: API pulls, joins, dedupe).
const customers = freeagent.list_contacts({ /* ... */ }).map(enrich);
kv.set({ key: "customers-2026", value: customers });

// Call 2 (or 10) — rehydrate instantly, no re-pull.
const customers = kv.get({ key: "customers-2026" });
// ... write notes, build a dashboard, compute gaps — all reading the same data.
```

## Tools

| Tool | Purpose |
| --- | --- |
| `kv__set({key, value, ttl_minutes?, pinned?})` | Store a JSON value. Returns `{ok, key, bytes, ttl_expires_at, pinned}`. |
| `kv__get({key})` | Return the value verbatim, or `null` if absent/expired. |
| `kv__list({prefix?, include_expired?, limit?, offset?})` | List key metadata (no values) + `total_bytes`. |
| `kv__delete({key})` | Remove a key. Idempotent (`deleted: false` when absent). |

## Semantics & limits

- **Scope:** the current workspace (override with `workspace_id`). `source_session_id`
  is recorded for provenance.
- **TTL:** default 120 minutes. Set `pinned: true` (with no `ttl_minutes`) for no
  expiry, or `ttl_minutes: 0` + `pinned: true`. Expired keys read back as `null`.
- **Caps:** 1 MiB per value; 256 keys and 16 MiB total per workspace. Over a cap,
  `kv__set` returns an error telling you to delete some keys first.
- **`kv__get` returns the value directly** so the sandbox auto-unwraps it back to
  the original JS value: `const data = kv.get({key}) || rebuild()`.

## When to use what

- **`kv`** — opaque JSON object graphs you want back as-is. Build-once-reuse-many.
- **`data`** (data workbench) — tabular/document scratch you want to **query** with
  SQL or search with FTS5.
- **`memory`** — durable, embedding-indexed conclusions that should outlive the
  session. `kv` is scratch; promote real findings with `memory__save`.

> Why not just reuse the goja VM so `globalThis` persists? Because re-declaring a
> top-level `const`/`let` on a reused VM throws `SyntaxError: already declared`,
> and agents write `const X = ...` atop nearly every snippet. VM reuse also breaks
> per-call isolation and is not concurrency-safe. An explicit, auditable kv store
> avoids all of that.
