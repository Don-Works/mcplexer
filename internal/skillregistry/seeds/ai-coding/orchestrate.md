---
name: orchestrate
description: Coordinate the full pipeline from source document to finished sales collateral
---

# Orchestrate Collateral Pipeline

Coordinate the full pipeline from source document to finished sales collateral.

## Usage

```
/orchestrate {source-doc-path}
```

Requires a source document path as argument.

---

## Workflows

### 1. Full Pipeline (default)

Run when creating collateral from scratch or regenerating everything.

```
Step 1: /extract {source-doc}           → output/extraction.md
Step 2: /generate-service-doc {source}  → output/collateral/service-doc.md
Step 3: /generate-battle-card {source}  → output/collateral/battle-card.md
Step 4: /generate-one-pager {source}    → output/collateral/one-pager.md
Step 5: /generate-faq {source}          → output/collateral/faq.md
Step 6: /review {source}               → output/review-report.md
Step 7: Apply fixes from review
Step 8: Report summary
```

### 2. Single Document

Run when updating just one document. Still requires extraction.

```
/orchestrate service-doc    → Runs extract + generate-service-doc + review
/orchestrate battle-card    → Runs extract + generate-battle-card + review
/orchestrate one-pager      → Runs extract + generate-one-pager + review
/orchestrate faq            → Runs extract + generate-faq + review
```

### 3. Review Only

Run when collateral already exists and you want to check quality.

```
/orchestrate review         → Runs review only
```

### 4. Client Proposal

Run when creating a proposal for a specific client.

```
/orchestrate proposal {client-name}  → Runs extract + generate-proposal
```

---

## Execution Protocol

### Before Starting

1. Read the user's request and determine which workflow to run
2. Check if `output/extraction.md` exists and is recent. If the source doc has changed, re-extract
3. Announce the plan: "Running {workflow}: {list of steps}"

### During Execution

For each step:
1. Announce: "Step {N}/{total}: Running {skill name}..."
2. Execute the skill
3. Report: "{skill name} complete. Output: {file path} ({line count} lines)"
4. If a step fails, report the error and ask the user how to proceed

### After Completion

1. Summarise what was created:

```markdown
## Pipeline Complete

| Document | Lines | Status |
|----------|-------|--------|
| extraction.md | X | Created |
| service-doc.md | X | Created |
| battle-card.md | X | Created |
| one-pager.md | X | Created |
| faq.md | X | Created |
| review-report.md | X | Created |

**Review findings:** X critical, X minor issues
**Fixes applied:** X/X
```

2. List any items that need human attention
3. Suggest next steps (e.g. "Run `/generate-web-page` to create a website page", "Run `/generate-proposal {client}` for a client-specific version")

---

## Reuse Rules

- If `output/extraction.md` exists and the source document has not changed, skip extraction
- If a collateral file already exists, overwrite it (the pipeline always generates fresh)
- The review step always runs on whatever exists in `output/collateral/`

---

## Error Handling

- If extraction fails validation (contradictions found), stop and report to user
- If a generation step fails, skip it and continue with the next. Report failures at the end
- If the review finds critical issues, apply fixes automatically and re-verify

---

## Notes

This orchestrator coordinates existing skills. Each skill is independent and can be run directly. The orchestrator adds:
- Sequencing (extraction before generation, review after generation)
- Progress reporting
- Summary and next-step suggestions
