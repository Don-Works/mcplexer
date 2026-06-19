---
name: marketing-review
description: Review generated marketing collateral for business translation, technical accuracy, and brand compliance
---

# Review Generated Collateral

Review all generated collateral for quality, accuracy, and brand compliance. This skill runs three review passes: business translation, technical accuracy, and brand compliance.

## Input

Read these files:
1. **All files in** `output/collateral/` — the documents to review
2. **Brand guidelines:** `config/brand-guidelines.md`
3. **Source document:** `$ARGUMENTS` (required) — the source of truth for fact-checking

---

## Review Passes

Run all three passes, then produce a consolidated report.

### Pass 1 — Business Translation

For every technical claim in the collateral, verify it has:

| Component | Check |
|-----------|-------|
| **WHAT** | Technical description present |
| **WHY IT MATTERS** | Business impact stated for a non-technical buyer |
| **PROOF** | Evidence cited (benchmark, spec reference, case study) |

Flag:
- Technical claims missing business translation
- Raw numbers without real-world context or comparison
- Jargon used without explanation
- Features described without benefits

### Pass 2 — Technical Accuracy

Check every factual claim against the source document:

- **Feature claims** — does the source document describe this capability?
- **Status claims** — is the feature actually shipped, in progress, or planned?
- **Performance numbers** — do they match the source document exactly?
- **Architecture descriptions** — are they technically correct?
- **Competitor comparisons** — are they fair and defensible?

Flag:
- Claims not supported by the source document
- Exaggerated or inflated capabilities
- Incorrect status labels
- Missing qualifications on Planned items

### Pass 3 — Brand Compliance

Check against brand guidelines:

| Check | Rule |
|-------|------|
| British English | colour, organisation, optimisation, behaviour, centralised |
| Banned words | apply the banned word list from `config/brand-guidelines.md` |
| Enterprise language | apply the enterprise terminology table from `config/brand-guidelines.md` |
| No overselling | only claim what the spec states |
| No emdashes | use full stops instead |
| No semicolons | split into two sentences |
| No bare tildes before numbers | `~70%` renders as strikethrough in PDF. Use "approx. 70%" or just "70%" |
| Oxford comma | always |
| Planned items labelled | never presented as shipped |
| Talk tracks sound human | read aloud test |
| Tables preferred | over long paragraphs |

---

## Output Format

Write review to `output/review-report.md`:

```markdown
# Collateral Review Report

**Reviewed:** {date}
**Source document:** {path}
**Files reviewed:** {list}

## Summary

| Category | Issues Found | Critical | Minor |
|----------|-------------|----------|-------|
| Business translation | X | X | X |
| Technical accuracy | X | X | X |
| Brand compliance | X | X | X |
| **Total** | **X** | **X** | **X** |

## Critical Issues (Must Fix)

### Issue 1: {File — Location}
**Current:** "{current text}"
**Problem:** {what is wrong}
**Fix:** "{suggested replacement}"

## Minor Issues (Should Fix)

### Issue 1: {File — Location}
...

## Missing Content

| Document | Missing Section or Element |
|----------|--------------------------|
| {file} | {what is missing} |

## Overall Assessment

**Ready for use:** Yes / No / With fixes

**Top priorities:**
1. {most important fix}
2. {second priority}
3. {third priority}
```

---

## After Review

If issues are found:
1. Report the review findings to the user
2. Ask whether to apply fixes automatically
3. If approved, apply all fixes and re-run the review to verify

---

## Output

Write to `output/review-report.md`. Report:
- Total issues found
- Critical vs minor breakdown
- Overall assessment (ready / not ready / ready with fixes)
