---
name: using-mcplexer
description: Use when connected to the mcplexer gateway (mcpx__execute_code / mcpx__search_tools in your tool list) — the minimal operating contract. Discover via search, batch via code execution, fetch deeper playbooks on demand, and ask the shared local code index first (index.context / index.search before reading the repo).
---

# Using MCPlexer

The gateway exposes 5 top-level tools; EVERYTHING else (github, linear, task, mesh, memory, skill, ...) is reachable only through them.

1. `mcpx__search_tools` — find any callable. Start cheap with summary mode: `{queries:["create task"], namespaces:["task"]}`. Fetch one exact signature with `{tool:"task__create"}`. Use `detail:"full"` only for a narrow query/namespace when you need several signatures at once.
2. `mcpx__execute_code` — call tools as JavaScript: `const r = task.create({title:"..."}); print(r.id)`. Registry names are `ns__tool`; in JS use the DOT form `ns.tool(args)` — synchronous, no await. Batch related calls into ONE snippet. Tool results auto-unwrap: JSON text usually becomes objects, plain-text tools such as `mcpx.skill_get` return strings. Never `JSON.parse(result.content[0].text)`. `parallel([...])` returns null for failed entries (it does not throw). The sandbox is also a full JS environment for math/parsing/transforms, and `sleep(ms)` enables bounded poll loops.
3. `secret__prompt` / `secret__list_refs` — pass `secret://KEY` refs as tool args; plaintext never enters your context.
4. `mcpx__retrieve` — expand a compression marker (see below) back to the exact original bytes.

## Compression markers — `[[ccr key=... bytes=N ...]]` (load-bearing)

The gateway compresses tool results and execute_code output before they reach your context. Anything it drops is stashed first and recoverable byte-exact: when you see a marker like `[[ccr key=abc123... bytes=8192 — call mcpx__retrieve(...)]]`, call `mcpx__retrieve({key})` (top-level or `mcpx.retrieve({key})` in a snippet) to get the omitted content. NEVER re-run a tool just to see omitted content — retrieve is exact, free of side effects, and each read renews the stash TTL.

Markers appear where the gateway: truncated an oversize result (head + marker + tail), omitted low-severity log lines (`N lower-severity lines omitted`), stripped ANSI escapes / collapsed progress-bar frames / collapsed repeated lines (exact counts stay inline), externalized an inline base64 blob, or capped a long print() stream (the full stream is one retrieve away). A result reading `[unchanged: byte-identical to the result <tool> returned at <time> earlier in this session]` means the new result equals one you already have — reuse the earlier content; retrieve only if it left your context.

## Values in the sandbox are EXACT (2026-07 contract)

Tool results consumed inside `mcpx__execute_code` are never pruned or reshaped: `null` fields, empty arrays (`.map` works), and pagination metadata (`next_cursor`, `has_more`, ...) all survive — follow cursors with confidence. `print()` renders true values (large arrays as tables). `compact(obj)` is the explicit opt-in that prunes nulls/empties and columnarizes for display — it keeps pagination keys. Caveat: JavaScript numbers are float64, so integers beyond 2^53 lose precision inside the sandbox — for exact big-int IDs, read them from the raw text of a direct tool result instead of doing sandbox arithmetic on them.

## Code index — ask the index BEFORE reading the repo (load-bearing)

The gateway ships a built-in shared local codebase indexer (the `index` namespace: symbol map, import graph, test ownership, git churn, hybrid source search). Bad context selection is the top failure mode for agents — so ask the index first, read files second. The three you reach for most, in order of a typical task:

- `index.context({query, budget_tokens})` — ORIENT. THE opening move for any "what's relevant to this task / where do I look" question: a ranked, token-budgeted pack of the right files (summaries, key symbols with line numbers, owning tests, recent commits, and source snippets).
- `index.search({query, kind})` — the SOURCE that implements a behavior. Hybrid retrieval: lexical FTS5 always runs, and an opt-in local embedding model adds semantic recall (the result's `mode` reads "hybrid" vs "lexical"). Hits are citation-ready (`file:line`) — read the cited slice instead of grepping. Reach for it when you need the actual implementation/behavior, not just a name.
- `index.symbols({query})` — the DECLARATION of a function/method/type/class/component; camelCase is word-split ("kv set" finds HandleKVSet). Use instead of grepping for a definition.
- `index.deps({file, direction:"importers"})` — blast radius before you edit a file; `"imports"` for what it pulls in.
- `index.tests_for({file})` — which tests own a file; run those before and after changing it.
- `index.map_failure({text})` — paste a failing test / panic / stack trace, get ranked candidate files with reasons. Start debugging here, not with grep.
- `index.summary({file})`, `index.recent_changes({path, days})`, `index.status({})`, `index.build({paths, force})`.

Queries auto-build the index on first use. The physical index is keyed by the exact canonical root, so multiple authorized logical/shared workspaces that resolve to the same checkout reuse its chunks and vectors. Different clones and worktrees remain isolated because their roots and source state can differ. Authorization is still enforced per query; embeddings are off by default and can only target an explicitly enabled loopback provider. `node_modules`, vendored/build/cache/generated/minified/credential paths never enter chunks or embedding requests. Run `index.build` after big edits or branch switches; `index.status` reports freshness, completeness, chunk totals, and embedding readiness.

## Memory contract — RECALL first, CAPTURE last (load-bearing)

Memory is not optional housekeeping; it is a first-class step that brackets every session. The gateway now injects a recall nudge at session start and a capture nudge at session end (the `/v1/hooks/session` memory hook) — treat both as instructions, not chatter. Memory is mesh-shared, embedding-indexed, FTS5-searchable, dashboard-surfaced, and survives session end. Chat does not; "I'll remember" does not.

**RECALL BEFORE ACTING (the FIRST step).** Before you answer a project-specific question or start non-trivial work, run `memory.recall({query})` inside `mcpx__execute_code`. Pull the decisions, user preferences, project facts, and anti-patterns that past sessions saved. Skipping recall is how agents re-litigate settled decisions and re-introduce known anti-patterns. If the session-start digest already surfaced relevant rows, deepen them with a targeted `memory.recall` rather than ignoring them.

**CAPTURE AFTER (the LAST step).** Before you finish, run `memory.save({...})` for anything a future session needs that is NOT derivable from the repo: decisions with their rationale, user preferences not in code, project facts, and anti-patterns you hit. Do NOT save code (the repo is canonical), git history (commits are canonical), or one-off task progress (use task notes). Capture is the mirror of recall — knowledge that only lives in this session's context is lost the moment the session closes.

**Registry on demand (keeps your context clean):** when you need an unfamiliar workflow playbook, run `mcpx.skill_search({query:"..."})` once and fetch only what you need with `mcpx.skill_get({name})`. If you already know the skill name, skip search and fetch it directly. The deep playbooks live there: `mcplexer-features` (tool-family tour), `mcplexer-tasks` (the durable work ledger — create/claim/note/review tasks there, not in chat), `agent-mesh` (coordinating with other agents), `token-preserving-delegation` (handing bounded work to cheap models). Fetch, use, move on — never paste playbook bodies into long-lived context.

**Mesh in brief:** other agents are live on this gateway and on paired machines. `mesh.receive({})` checks your inbox and discovers peers (set a name + role on first call); `mesh.send({kind, content})` shares findings or asks questions. Every message you read resolves to exactly one of: ignore / reply / promote-to-task / do-it-and-reply.

**Errors teach:** gateway errors include corrected examples and did-you-mean suggestions — read them fully and fix in one step rather than brute-forcing variants.
