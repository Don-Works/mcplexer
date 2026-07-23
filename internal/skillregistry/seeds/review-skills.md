---
name: review-skills
description: Use when reviewing one MCPlexer skill or the whole skills estate for admission, retention, revision, evidence, scope, hierarchy, duplication, default routing, or removal. Evaluates whether every workspace has just enough effective skills, distinguishes registry inheritance from harness-local discovery, and can perform authorised move-before-delete cleanup with rollback and verification.
---

# Review MCPlexer Skills

Review the exact active artifact and the effective inventory seen by real workspace sessions. Do not substitute repository reputation, author reputation, stars, or evidence about a similarly named skill.

Remain read-only unless the user explicitly authorises registry or local-file mutation.

## Select the review mode

Use single-skill mode for a proposed or existing head. Use estate mode when asked whether the registry is sensible, compact, reviewed, correctly scoped, or “just enough” per workspace.

For either mode, establish:

- name, version, scope, content hash, author, source type, source path, licence, and bundle provenance;
- intended trigger, excluded near-misses, task cohort, and claimed benefit;
- body size, bundled files, dependencies, owner, freshness boundary, and retirement condition;
- exact mutation authority.

## Model the real hierarchy

Registry resolution is most-specific workspace, then its parent chain, then global. Same-name nearest heads shadow parents and global regardless of version number. Search, get, list, and inventory operate on this effective session scope; an admin scope-head view shows all layers and is not what one workspace sees.

Global means searchable everywhere, not fully injected into every prompt. Different-name globals still compete during retrieval, so global catalog size and trigger quality matter.

Separately inventory harness-native system/plugin skills, ~/.claude/skills, ~/.codex/skills, and workspace-local directories. Registry heads shadow same-name local entries only inside MCPlexer inventory; the harness may still expose local or plugin skills independently.

## Classify each skill

Choose one class before judging it:

1. First-party operational playbook: private conventions, exact tools, architecture, safety boundaries, or a fragile procedure.
2. Deterministic utility: a tested script, template, schema, asset, or directly verifiable transformation.
3. Public specialist reference: narrow current knowledge that may not be reliably inferable.
4. Generic public guidance: broadly known habits, personas, checklists, or framework advice.

Do not require a causal benchmark merely to retain a correct private operating contract. Do not exempt public advice because it sounds sensible.

## Apply the admission gates

### Necessity

Identify the exact information or capability missing from the base model, project instructions, and active skills. Reject duplication of AGENTS.md, CLAUDE.md, using-mcplexer, or another head. Reject generic public guidance without exact-artifact outcome evidence.

### Trigger and context cost

Require one observable job and a narrow trigger. Prefer loading no skill when no trigger matches. Flag blanket defaults, persona prompts, comprehensive manuals, ambiguous aliases, and overlapping descriptions.

Judge both body cost and catalog cost. Large bodies are acceptable only when conditional detail cannot be split; large global catalogs are acceptable only when entries are genuinely cross-workspace and retrieval remains precise.

### Scope

Ask where the skill is actually valid:

- one workspace or account: scope there;
- several descendants: scope to their nearest shared parent;
- nearly everywhere: global may be justified;
- nowhere active yet: keep outside the active registry.

Treat a project name, customer path, production account, private email, brand, provider tenant, or product-specific command in a global skill as presumptive mis-scoping.

### Correctness, safety, and freshness

Verify commands, tool names, paths, schemas, invariants, and permissions against authoritative sources. Inspect bundled executables for filesystem, network, credentials, shell, and destructive behaviour. Require provenance and licence for imports, plus an owner and compatibility or expiry boundary for version-sensitive guidance.

### Evidence

For first-party playbooks, require factual correctness, owner review, tight scope, and deterministic validation where possible. Label them operating contracts, not proven performance improvements.

For deterministic utilities, run tests and representative outputs.

For public skills, require evidence on the exact artifact or materially identical content, matched to the intended model, harness, and task cohort. Prefer paired skill-versus-no-skill trials, deterministic scoring, repeated runs, cost measurement, and uncertainty. Treat anecdotes, popularity, author reputation, category-level benchmarks, and evidence for another skill as weak.

Direct repeated success from the user is applicable evidence for retention in that user's own estate. An explicit keep or do-not-remove decision protects the exact artifact and must be surfaced before any REMOVE disposition. Record the validator, date, scope, and version or hash. This can justify KEEP-VALIDATED for that user without pretending to prove a general causal lift; paired trials remain required before broad performance claims or default routing across users.

Require local paired evidence before default routing or making a performance claim for any class. Send promising unproven public candidates to evaluate-dev-skills outside the active registry.

### Maintainability

Require an owner, source of truth, update path, and retirement condition. Prefer a small first-party contract over a broad imported manual. Reject inert includes, missing sources, stale paths, unexplained shadows, and dangling skill references.

## Audit the estate

For estate mode, report at minimum:

- active global heads and their total/median body cost;
- each workspace’s effective head count, direct additions, inherited layers, and overrides;
- workspace coverage: which scopes have no direct delta and whether that is intentional;
- global entries with product, client, account, or environment-specific triggers;
- identical, diverged, and older shadows;
- oversized bodies and weak trigger descriptions;
- public or externally sourced heads without applicable evidence;
- missing provenance, owners, freshness boundaries, and retirement conditions;
- unmanaged local skills, same-name local copies, archives accidentally discoverable, and plugin/system exceptions;
- dangling references and aliases;
- usage telemetry, while clearly separating observation from causal evidence.

Do not use a magic count as the verdict. A workspace has “just enough” only when every inherited global is defensible there, every scoped addition carries non-derivable local value, no narrower valid scope exists, no accidental duplicate or shadow remains, and search near-misses stay clean.

Return one estate verdict: CLEAN, NEEDS-SCOPING, NEEDS-EVIDENCE, or BLOCKED.

## Return dispositions

For each skill use exactly one:

- KEEP-INTERNAL
- KEEP-VALIDATED
- REVISE
- RE-SCOPE
- EVALUATE-OFFLINE
- REMOVE

Report:

    Skill and version:
    Class:
    Claim:
    Unique non-derivable value:
    Current and recommended scope:
    Evidence and applicability:
    Context, safety, freshness, and duplication risks:
    Disposition:
    Required follow-up:

For estate reviews, add a compact migration table and counts before and after.

## Apply authorised cleanup

When mutation is authorised:

1. Create a portable backup and record its ID.
2. Recall durable decisions and capture every source head, scope, version, hash, provenance, bundle, inbound reference, and explicit user-protected status.
3. Publish the exact or revised destination first; preserve bundles and lineage.
4. Fetch and search from the destination scope before deleting the source.
5. Update routers, aliases, and references.
6. Soft-delete all active versions at the intended source scope; do not broaden deletion or remove a user-protected artifact without renewed confirmation.
7. Remove an identical child copy only after proving it inherits the intended parent.
8. Run the deterministic audit and re-count every effective workspace inventory.
9. Verify removed public or mis-scoped names no longer appear where prohibited.
10. Reconcile local copies separately and preserve harness-owned system/plugin skills.
11. Record the decisions, rationale, residual exceptions, and rollback ID.

Stop rather than guessing if a move changes credentials, workspace access, production authority, or a security boundary.
