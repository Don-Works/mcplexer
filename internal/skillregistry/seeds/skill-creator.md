---
name: skill-creator
description: Use when creating, importing, revising, publishing, re-scoping, or retiring an MCPlexer registry skill. Produces the smallest useful playbook, chooses the narrowest correct workspace scope, applies evidence and provenance gates, avoids duplicate or generic guidance, publishes safely, and verifies search, hierarchy, shadows, bundles, and local-copy drift.
---

# Create MCPlexer Skills

Create the smallest durable instruction set that supplies something the base model, repository instructions, and existing skills do not already provide.

A skill is successful when it triggers for one observable job, adds non-derivable value, stays out of unrelated work, and has a clear owner and retirement condition. More skills and more prose are not inherently better.

## Decide whether a skill should exist

Before drafting:

1. Recall prior decisions and inspect project instructions.
2. Search the registry by user intent, not only by a proposed name.
3. Inspect effective heads and the full inventory, including local skill directories.
4. Fetch the exact current version of any overlapping skill.
5. Run review-skills on the proposal or revision.

Create or retain a skill only when it provides at least one of:

- private operating knowledge, exact architecture, safety boundaries, or fragile sequencing;
- a tested script, template, schema, asset, or deterministic verifier;
- a precise tool integration or current specialist procedure the model cannot reliably infer;
- an explicitly user-validated artifact with repeated successful use and a recorded retain decision;
- a validated public artifact with evidence applicable to the intended model, harness, and task cohort.

Do not publish generic public engineering advice, personas, broad checklists, or documentation dumps merely because they sound sensible. Put promising unvalidated public artifacts through evaluate-dev-skills outside the active registry.

## Choose the narrowest correct scope

Registry resolution is ordered most-specific workspace, then parent workspaces, then global. For the same name, the nearest active head wins even when its version number is lower.

Use:

- current workspace for one project, client, product, account, or environment;
- the nearest shared parent for a playbook genuinely shared by its descendants;
- global only for gateway contracts, cross-workspace tooling, or capabilities that should be available almost everywhere.

A workspace skill does not leak into unrelated workspaces. A global skill is searchable everywhere, but its full body is fetched only on demand.

Treat harness-native system skills, plugins, ~/.claude/skills, ~/.codex/skills, and workspace-local skill directories as separate discovery layers. Registry publication does not remove those copies. Reconcile or explicitly retain them after publication.

If the correct parent is not the current workspace, use the exact-scope admin publisher only when authorised. Do not simulate hierarchy with duplicate global copies.

## Write the trigger contract

Use a short lower-case hyphenated name, no more than 64 characters. Prefer a verb-led job over a persona or topic.

Frontmatter must contain only name and description:

    ---
    name: verify-release
    description: Use when preparing a release candidate that must pass repository-specific signing, packaging, smoke-test, and rollback gates.
    ---

Make the description answer:

- When should this trigger?
- What exact job does it perform?
- What nearby task must not trigger it?
- Which environment, account, product, or version boundary matters?

Avoid vague descriptions such as “helps with development” or “best practices.”

## Design the body

Use imperative instructions. Keep the happy path obvious and put rules near the step they constrain.

A compact body normally contains:

1. outcome and scope;
2. authoritative sources and prerequisites;
3. ordered workflow;
4. safety and approval boundaries;
5. verification and stop conditions;
6. conditional links to scripts or references;
7. owner, freshness boundary, and retirement condition when non-obvious.

Assume the agent already knows general engineering. Delete explanations it can infer. Prefer one precise example to several variants. Keep SKILL.md below 500 lines and about 5,000 tokens; move detailed schemas and uncommon variants into one-level references.

Do not add README, changelog, installation guide, duplicated examples, or files that do not directly help execution.

Use scripts when the same fragile logic would otherwise be rewritten. Test every executable on a representative input. Use references for details loaded only when relevant and assets for files copied into outputs. Inspect every bundled executable for filesystem, network, credential, shell, and destructive behaviour.

## Record governance

For every proposed head, record during review:

- class: first-party operational, deterministic utility, public specialist, or generic public guidance;
- unique non-derivable value;
- intended scope and why;
- owner and source of truth;
- provenance, licence, compatibility, and expiry where relevant;
- verification evidence and its applicability;
- replacement and retirement condition.

First-party correctness makes an operating contract admissible; it does not prove a performance improvement. Public guidance requires evidence on the exact artifact or materially identical content before active admission. Popularity, reputation, and anecdotes are not substitutes.

Repeated direct success reported by the user is applicable evidence for retention in that user's estate, especially when the skill is used across real workspaces. Record who validated it, when, intended scope, and the exact artifact. This supports keeping the skill; it does not by itself support a general causal performance claim or default routing for other users. Treat an explicit do-not-remove decision as a hard constraint unless the user revokes it or a concrete safety issue requires escalation.

## Publish or revise safely

For an existing skill, fetch the exact body, content hash, scope, source, bundle, and current version before editing. Preserve sidecars and pass parent_version when creating a revision.

Publish to the current workspace unless global availability is justified:

    const result = mcpx.skill_publish({
      name: "verify-release",
      body: skillBody,
      parent_version: 2,
      scope: "workspace",
      author_hint: "release-owner"
    });
    print(result);

Use scope "global" only after review-skills returns KEEP-INTERNAL or KEEP-VALIDATED with a global-scope justification.

For an exact parent or child workspace migration, use mcplexer.publish_skill_registry from an authorised admin session. Move before delete: publish and verify the destination head, then soft-delete every active source version. Preserve bundle_b64 when moving bundled skills.

## Verify the result

After every publish, move, or removal:

1. Fetch the exact head and confirm name, scope, version, hash, and bundle.
2. Search with realistic trigger and near-miss prompts.
3. Inspect effective inventory from the affected workspace chain.
4. Run the deterministic registry audit.
5. Resolve accidental identical, diverged, or older shadows.
6. Check active skill bodies for dangling references.
7. Check local directories for unmanaged or duplicate copies.
8. Confirm unrelated workspaces cannot see a scoped skill.
9. Record invocation telemetry when available; do not mistake observational success for a no-skill comparison.

Do not call the estate reviewed while unresolved provenance, public-evidence, source, safety, shadow, or local-drift findings remain.

## Definition of done

A skill is done only when it is necessary, narrowly triggered, correctly scoped, compact, safe, provenance-complete, verifiable, discoverable for its intended users, absent from unrelated workspaces, and owned through retirement.
