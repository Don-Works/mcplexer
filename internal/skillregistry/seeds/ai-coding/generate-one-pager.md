---
name: generate-one-pager
description: Create a concise executive one-pager (400-500 words) from an extraction file for post-call follow-up
---

# Generate Executive One-Pager

Create a concise executive summary from an extraction file. Target: 400-500 words. This should be the document a salesperson sends after a first call.

## Input

Read these files:
1. **Extraction:** `output/extraction.md` (run `/extract` first if this does not exist)
2. **Brand guidelines:** `config/brand-guidelines.md`
3. **Source document:** `$ARGUMENTS` (required) — for reference

---

## Document Structure

Write to `output/collateral/one-pager.md`. Follow this structure:

### Key Terms

Short glossary table — maximum 12 terms. Include only terms that appear in the one-pager itself.

### What Is It

One sentence. Must be understandable without domain expertise.

### The Problem

2-3 sentences on the business pain. Focus on the consequences of inaction, not the technical gap.

### Key Benefits

Table with columns: Benefit | What It Actually Means | Why It Matters.

Maximum 7 rows. Choose the benefits with the strongest business impact. Every row must trace to the source document.

### Who It's For

Bullet list of target personas and organisation types. 4-5 entries maximum.

### How It Works

Numbered steps, maximum 6. Each step is one sentence. Describe the deployment and usage journey from the customer's perspective.

### Proof Points

Concrete evidence: test coverage numbers, standards supported, architecture facts, performance data. Only include what the source document explicitly states.

### Next Steps

Clear call to action. 2-3 numbered steps (e.g. technical briefing, proof of concept, contact info).

---

## Constraints

- **400-500 words** maximum (excluding glossary and metadata)
- **Prefer tables over paragraphs** — scanability is critical
- **No deep technical dives** — point to the service doc for detail
- **British English**, no banned words, no emdashes, Oxford comma
- **No bare tildes before numbers** — `~70%` renders as strikethrough in PDF. Write "approx. 70%" or just "70%"
- **No overselling** — only claim what the source document states
- **Planned items** should not appear on the one-pager (only show shipped features)

---

## Output

Write to `output/collateral/one-pager.md`. Report:
- File created with word count
- Whether it fits within the 400-500 word target
