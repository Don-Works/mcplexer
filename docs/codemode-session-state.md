# Code-mode session state (the `session` object)

Each `mcpx__execute_code` call runs in a **fresh** Goja VM, so variables built
in one call vanish before the next. The `session` object lets an agent build an
expensive dataset once and reuse it across calls **within the same MCP session**
— the transparent "my variable is just still there" ergonomic, no get/set
ceremony.

```js
// call 1 — expensive, done once
session.book = freeagent.buildCustomerBook();   // FreeAgent + 5y of calendar
session.meetings = matchMeetings(session.book);

// call 2 — same session, no re-fetch; session.* is still here
for (const c of session.book) writeNote(c, session.meetings[c.id]);
```

## How it works

Before each run the gateway rehydrates the `session` global from the previous
call's snapshot; after a **clean** run it snapshots `session`'s own enumerable,
JSON-serializable properties back into an in-memory map keyed by MCP session id.

Rules:

- **Ephemeral, in-memory, this session only.** Held in gateway memory, cleared
  on disconnect, lost on daemon restart. For durable, cross-session/restart
  state use the `kv` namespace (`kv.set` / `kv.get`, SQLite-backed).
- **JSON-serializable values only.** Objects, arrays, strings, numbers,
  booleans, null. Functions/closures are dropped and named in a warning.
- **Top-level `const`/`let` do NOT persist** — they are lexical, not properties
  of `session`. Assign to `session.x` instead. (This is a hard Goja constraint:
  a reused VM throws `SyntaxError: Identifier 'x' has already been declared` on
  the second top-level `const x`, so the VM is deliberately not reused; the
  `session` object is the supported persistence path.)
- **Full-snapshot replace** — `delete session.x` removes a key next call.
- **Nothing is persisted on error/timeout** (no half-state clobber).
- **Capped** by `code_mode_session_state_max_bytes` (default 4 MiB); an over-cap
  snapshot is rejected with a warning and the prior state is kept.

## `session` vs `kv`

| | `session` object | `kv` namespace |
|---|---|---|
| Scope | one MCP session | workspace |
| Lifetime | across calls; lost on disconnect/restart | durable (survives restart + other sessions) |
| Storage | in-memory | SQLite |
| Shape | live object (`session.x = ...`) | explicit `kv.set/get/list/delete` |
| Use for | reuse an expensive in-session result | conclusions worth keeping |

## Settings

| Setting | Env | Default |
|---|---|---|
| `code_mode_session_state_max_bytes` | `MCPLEXER_CODE_MODE_SESSION_STATE_MAX_BYTES` | 4 MiB |
