---
name: extract
description: Validate a technical source document and extract structured data for downstream collateral generation
---

# Extract and Validate Source Document

Validate a technical source document and extract structured data for downstream collateral generation.

## Input

**Source document:** $ARGUMENTS (required — path to the technical document to extract from)
**Brand guidelines:** `config/brand-guidelines.md`

Read both files before proceeding. If either path is invalid or empty, stop and tell the user.

---

## Pipeline

### Step 1 — Validate

Scan the source document and check for:

1. **Contradictions** — numbers that conflict, features described differently in two places, inconsistent terminology
2. **Incompleteness** — claims without supporting data, features missing detail, comparisons without baselines
3. **Ambiguity** — vague referents, jargon used inconsistently, unclear scope
4. **Status flags** — identify every item marked "In Progress", "Planned", or with a PR number

Produce a validation summary:
- **Status:** PASS / PASS WITH WARNINGS / FAIL
- **Contradictions found:** count
- **Incomplete sections:** count
- **Ambiguities detected:** count
- **In Progress items:** list each with PR number if available
- **Planned items:** list each

**Gate rule:** If any contradictions are found, STOP and report them to the user. Do not proceed until contradictions are resolved. Warnings (incomplete/ambiguous) may proceed with a note.

### Step 2 — Extract

Create a structured extraction organised by the source document's top-level groups (e.g. component names, product areas).

For each group, extract:

1. **Feature inventory** — every feature with a one-line description and current status (Shipped / In Progress / Planned)
2. **Performance data** — any throughput, latency, scaling, or capacity numbers
3. **Compliance data** — regulatory frameworks, standards, audit capabilities
4. **Technical differentiators** — what makes this different from alternatives
5. **Integration points** — how components connect to each other and to external systems
6. **Authentication and identity** — auth methods, identity models, token management
7. **Operations and tooling** — deployment, monitoring, testing, CLI tools

For EVERY technical fact, structure it as:
- **WHAT:** The technical fact
- **WHY IT MATTERS:** Business impact for a non-technical buyer
- **PROOF:** Supporting evidence (benchmark, case study, spec reference)

### Step 3 — Identify Gaps

Flag anything that would weaken sales collateral:
- Features without business value translation
- Claims without proof points
- Missing competitive context (no "vs alternative" framing)
- Missing cost or TCO data
- No case study or deployment example

---

## Output

Write the extraction to `output/extraction.md`. Create the directory if needed.

The extraction file should be structured as:

```markdown
# Extraction: {Document Title}

**Source:** {file path}
**Extracted:** {date}
**Validation status:** {PASS / PASS WITH WARNINGS / FAIL}

## Validation Summary
{validation findings from Step 1}

## Feature Inventory
{grouped feature tables from Step 2}

## Structured Claims
{WHAT / WHY IT MATTERS / PROOF for each major fact}

## Gaps and Recommendations
{findings from Step 3}
```

Report:
- Validation status
- Total features extracted
- Gaps identified
- Recommendations for the user
