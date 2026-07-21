---
name: skill-creator
description: Use when the user wants to create a new skill, draft a SKILL.md, or improve an existing skill in the mcplexer skills registry. Covers naming conventions, frontmatter requirements, the "Use when..." trigger phrasing, body length, and how to publish via mcpx__skill_publish.
tags:
  - meta
  - registry
---

# Creating a skill

Skills are SKILL.md documents that teach an agent how to do a specific
task. They get loaded into the agent's context window when triggered,
so they need to be focused, terse, and well-named.

## Frontmatter

```yaml
---
name: my-skill          # lowercase + hyphens, ≤64 chars
description: Use when the user asks for X. Covers Y and Z.   # ≤1024 chars; include BOTH what + when
tags:
  - optional
  - free-form
---
```

The `description` is the retrieval key. Lead with "Use when…" so the
search ranks it for natural-language intent matches.

## Body

Markdown after the closing `---` fence. Aim for under 500 lines / 5000
tokens. Reference longer assets by relative path if you need them.

## Publish

If the skill already exists on disk, publish it directly:

```js
const result = mcpx.skill_publish({ source_path: "path/to/SKILL.md" });
print(JSON.stringify(result));
```

Passing a skill directory instead of the file bundles sidecars such as
`scripts/` and `reference/` automatically.

For inline publishing, use `body_b64` when the markdown contains lots of
quotes or backticks:

```js
const result = mcpx.skill_publish({ body_b64: "BASE64_ENCODED_SKILL_MD" });
print(JSON.stringify(result));
```

```js
const result = mcpx.skill_publish({
  name: "my-skill",
  body: "---\nname: my-skill\ndescription: ...\n---\n# Body...",
});
print(JSON.stringify(result));
```

Re-publishing the same body is idempotent (returns `action: "deduped"`).
Edit the body and re-publish to create v2.
