# Skill registry composition

Registry skills can reuse exact, text-only fragments without copying their
instructions. Composition is deliberately content-addressed: every dependency
names an exact scope, version, and SHA-256 hash, so publishing a newer version
cannot silently change an existing skill.

## Authoring an include

Declare the dependency in `SKILL.md` frontmatter and place one matching marker
in the Markdown body:

```yaml
---
name: compatibility-entry
description: Use when a legacy entry point should run the canonical workflow.
refinement: disabled
includes:
  - id: canonical
    skill: canonical-workflow
    scope: global
    version: 3
    content_hash: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
---

# Compatibility entry

Apply this entry point's mode override before the shared workflow.

<!-- mcpx:include canonical -->
```

`scope` is mandatory and is either:

- `global`: resolve the exact global row.
- `same`: resolve the exact workspace of the declaring row (or global when the
  declaring row is global).

An optional `section` selects a named fragment from the dependency. Sections
use non-nesting markers outside fenced code blocks:

```markdown
<!-- mcpx:section reporting -->
Shared reporting instructions.
<!-- mcpx:endsection -->
```

The including skill keeps its own frontmatter. Included skills contribute only
Markdown; their frontmatter and section markers are removed. Include and
section markers inside fenced code blocks are treated as literal examples.

## Publication and retrieval

Publish dependencies before dependants, then copy the dependency's returned
`version` and `content_hash` into the dependant. Publication renders the whole
graph before writing, so missing rows, mismatched hashes, cycles, malformed or
duplicate markers, and expansion-limit failures are rejected atomically.

`mcpx__skill_get` expands includes by default and returns the raw root,
expanded SHA-256, and edge provenance when composition was used. Pass
`expand_includes=false` to inspect the stored source. Materialisation, bundle
installation, and Worker prompts also consume the rendered body.

Deleting a pinned dependency is blocked while any active version refers to it.
Delete or republish dependants first. `mcplexer__audit_skill_registry` checks
composition for every active version, including non-head versions that remain
valid pin targets.

## V1 boundaries

- At most 16 includes may be declared by one skill, 32 edges may be expanded,
  nesting depth is capped at 8, and the rendered body is capped at 64 KiB.
- Included targets must be inline, text-only registry entries. Bundle, path,
  git, or other asset-bearing targets must be flattened or split into an
  explicit text-only fragment first.
- V1 hub, export/import, mesh, and P2P protocols transfer one root rather than
  a dependency closure. Composed roots therefore fail closed on explicit
  transfer and are omitted from discovery manifests until a closure-aware wire
  format exists.
- Legacy `@include` or free-form metadata is inert and is reported by the audit.
  Only typed `includes` plus `<!-- mcpx:include ID -->` markers are executable.

