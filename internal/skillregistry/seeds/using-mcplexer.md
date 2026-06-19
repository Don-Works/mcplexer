---
name: using-mcplexer
description: Use when connected to the mcplexer gateway (mcpx__execute_code / mcpx__search_tools in your tool list) — the minimal operating contract. Discover via search, batch via code execution, fetch deeper playbooks from the skill registry on demand instead of preloading context.
---

# Using MCPlexer

The gateway exposes 4 top-level tools; EVERYTHING else (github, linear, task, mesh, memory, skill, ...) is reachable only through them.

1. `mcpx__search_tools` — find any callable. Start cheap with summary mode: `{queries:["create task"], namespaces:["task"]}`. Fetch one exact signature with `{tool:"task__create"}`. Use `detail:"full"` only for a narrow query/namespace when you need several signatures at once.
2. `mcpx__execute_code` — call tools as JavaScript: `const r = task.create({title:"..."}); print(r.id)`. Registry names are `ns__tool`; in JS use the DOT form `ns.tool(args)` — synchronous, no await. Batch related calls into ONE snippet. Tool results auto-unwrap: JSON text usually becomes objects, plain-text tools such as `mcpx.skill_get` return strings. Never `JSON.parse(result.content[0].text)`. `parallel([...])` returns null for failed entries (it does not throw). The sandbox is also a full JS environment for math/parsing/transforms, and `sleep(ms)` enables bounded poll loops.
3. `secret__prompt` / `secret__list_refs` — pass `secret://KEY` refs as tool args; plaintext never enters your context.

## Memory contract — mcplexer is your ONLY memory system (load-bearing)

**Do NOT use your harness-native memory system.** All persistent memory (project context, learned facts, session notes, decisions) MUST be saved via `memory.save({...})` inside `mcpx__execute_code`. Do not write to your harness-native memory files (checkpoint.md, MEMORY.md, notes.md, ~/.claude/projects/*/memory/*.md, ~/.codex/AGENTS.md memory sections, etc.). mcplexer memory is cross-harness, cross-machine, and persists across all sessions. Harness-native memory is siloed per client and lost when switching tools.

Memory is not optional housekeeping; it is a first-class step that brackets every session. The gateway now injects a recall nudge at session start and a capture nudge at session end (the `/v1/hooks/session` memory hook) — treat both as instructions, not chatter. Memory is mesh-shared, embedding-indexed, FTS5-searchable, dashboard-surfaced, and survives session end. Chat does not; "I'll remember" does not.

**RECALL BEFORE ACTING (the FIRST step).** Before you answer a project-specific question or start non-trivial work, run `memory.recall({query})` inside `mcpx__execute_code`. Pull the decisions, user preferences, project facts, and anti-patterns that past sessions saved. Skipping recall is how agents re-litigate settled decisions and re-introduce known anti-patterns. If the session-start digest already surfaced relevant rows, deepen them with a targeted `memory.recall` rather than ignoring them.

**CAPTURE AFTER (the LAST step).** Before you finish, run `memory.save({...})` for anything a future session needs that is NOT derivable from the repo: decisions with their rationale, user preferences not in code, project facts, and anti-patterns you hit. Do NOT save code (the repo is canonical), git history (commits are canonical), or one-off task progress (use task notes). Capture is the mirror of recall — knowledge that only lives in this session's context is lost the moment the session closes.

**Registry on demand (keeps your context clean):** when you need an unfamiliar workflow playbook, run `mcpx.skill_search({query:"..."})` once and fetch only what you need with `mcpx.skill_get({name})`. If you already know the skill name, skip search and fetch it directly. The deep playbooks live there: `mcplexer-features` (tool-family tour), `mcplexer-tasks` (the durable work ledger — create/claim/note/review tasks there, not in chat), `agent-mesh` (coordinating with other agents), `token-preserving-delegation` (handing bounded work to cheap models). Fetch, use, move on — never paste playbook bodies into long-lived context.

**Mesh in brief:** other agents are live on this gateway and on paired machines. `mesh.receive({})` checks your inbox and discovers peers (set a name + role on first call); `mesh.send({kind, content})` shares findings or asks questions. Every message you read resolves to exactly one of: ignore / reply / promote-to-task / do-it-and-reply.

**Errors teach:** gateway errors include corrected examples and did-you-mean suggestions — read them fully and fix in one step rather than brute-forcing variants.
