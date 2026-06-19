---
name: skill-search
description: Use when an agent needs to find a skill matching its current task — "I need to do X, is there a skill for it?". Explains how to call mcpx__skill_search with a natural-language query, interpret the ranked results, and fetch the full body with mcpx__skill_get.
tags:
  - meta
  - registry
  - retrieval
---

# Asking the registry for a skill

The mcplexer skills registry indexes every published SKILL.md by its
description + body. Ask in natural language; the registry returns the
top matches by TF-IDF cosine similarity over the head version of each
skill.

## Search

```js
const hits = mcpx.skill_search({
  query: "I need help with browser automation",
  limit: 5,
});
print(JSON.stringify(hits));
```

Returns `[{name, version, description, score}]` ordered by score.
A score < 0.05 usually means no real match — fall back to listing the
catalog with `mcpx.skill_list({})`.

## Fetch the body

```js
const body = mcpx.skill_get({ name: "cmux-browser" });
print(body);
```

`version` defaults to `"latest"`. Pass `"stable"` to fetch the admin-
curated stable revision, or an integer to pin.

## Iterate

Find a skill that's *almost* right, then improve it:

```js
const current = mcpx.skill_get({ name: "skill-name" });
const improved = current.replace("old phrase", "better phrase");
mcpx.skill_publish({
  name: "skill-name",
  body: improved,
  parent_version: 1,
});
```

This creates v2 and leaves v1 immutable for rollback.
