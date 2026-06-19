---
name: generate-faq
description: Create a categorised FAQ document from an extraction file
---

# Generate FAQ Document

Create a categorised FAQ document from an extraction file.

## Input

Read these files:
1. **Extraction:** `output/extraction.md` (run `/extract` first if this does not exist)
2. **Brand guidelines:** `config/brand-guidelines.md`
3. **Source document:** `$ARGUMENTS` (required) — for reference and fact-checking

---

## Document Structure

Write to `output/collateral/faq.md`. Follow this structure:

### Key Terms

Glossary table. Include terms that a non-technical reader might encounter in the answers. Keep to 15-20 terms maximum.

### FAQ Categories

Organise questions into categories that match the source document's structure. Typical categories:

1. **General** — what is it, who is it for, how is it deployed, does it modify the underlying system
2. **Per-component categories** — one category per major product component, covering how it works, what it supports, and performance
3. **Authentication and identity** — auth methods, identity flows, token management
4. **Access control** — permission models, hierarchy, granularity
5. **Compliance** — regulatory features, audit, reporting
6. **Technical requirements** — infrastructure, monitoring, scaling, roadmap
7. **Engagement** — how to get started, PoC process, support, trial options

Adapt these categories to match the actual product. Do not force categories that do not apply.

### Question Format

Each answer should be:
- **2-4 sentences** for straightforward questions
- **Up to 6 sentences** for complex topics that need technical explanation
- Include **cross-references** between related questions (e.g. "See also: [Authentication](#authentication)")
- Use parenthetical explanations for jargon on first use (e.g. "IVMS101 (the international standard format for travel rule data)")

### Roadmap Question

Include one question about the product roadmap. List only Planned items with their descriptions. Say "timeline to be confirmed" if no date is available.

### Engagement Section

Always end with questions about:
- How to get started
- What a proof of concept involves
- What support is included
- Whether a self-service trial exists

---

## Content Rules

1. **British English** throughout
2. **Banned words** — same list as brand guidelines
3. **No overselling** — only claim what the source document states
4. **Planned items** must be clearly labelled as planned
5. **No emdashes or semicolons** — restructure sentences instead
6. **No bare tildes before numbers** — `~70%` renders as strikethrough in PDF. Write "approx. 70%" or just "70%"
7. **Oxford comma** always
8. **Cross-references** between related questions are required
9. **Every answer must be self-contained** — a reader should understand the answer without reading other questions first

---

## Footer

Do not add "Related Documents", version metadata, "prepared by", or "confidential" boilerplate.

---

## Output

Write to `output/collateral/faq.md`. Report:
- File created with line count
- Number of categories and questions
- Any topics from the source doc not covered by a question
