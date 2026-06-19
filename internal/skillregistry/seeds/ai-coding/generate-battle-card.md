---
name: generate-battle-card
description: Create a competitive positioning battle card from an extraction file
---

# Generate Competitive Battle Card

Create a competitive positioning battle card from an extraction file.

## Input

Read these files:
1. **Extraction:** `output/extraction.md` (run `/extract` first if this does not exist)
2. **Brand guidelines:** `config/brand-guidelines.md`
3. **Source document:** `$ARGUMENTS` (required) — for reference and fact-checking

---

## Document Structure

Write to `output/collateral/battle-card.md`. Follow this structure:

### Key Terms

Glossary table of technical terms. Shorter than the service doc glossary — include only terms that appear in competitive context. Term | Plain-English explanation.

### Quick Reference Table

Comparison matrix. Columns: Feature | Our Product | Alternative 1 | Alternative 2 | Alternative 3.

Identify the 3-4 most relevant competitor categories from the source document's differentiators. Rows should cover the product's strongest capabilities.

### When We Win

Three subsections:
- **Customer situations** — organisational contexts where we have the advantage
- **Technical triggers** — specific technical needs that make us the right fit
- **Business triggers** — business events that create urgency

### When We Lose

Honest assessment of situations where we are not the right fit. Be direct. Walking away early is better than a failed deployment.

### Competitor Weaknesses

For each competitor category:
- **Their approach** — what they typically do
- **Reality / limitation** — the gap between their approach and what the customer needs
- **Counter questions** — questions to ask the prospect that expose this gap
- **Talk track** — conversational script for the salesperson

### Objection Handling

For each common objection:
- The objection as a heading
- Full response paragraph with proof points
- WHAT / WHY IT MATTERS / PROOF structure

### Killer Questions

Three categories:
- **Pain discovery** — questions that reveal the prospect's current challenges
- **Urgency creation** — questions that make the timeline feel pressing
- **Competitor disqualification** — questions that expose what alternatives cannot do

### Landmines to Plant

Questions and topics to introduce early in evaluations that create problems for competitors. Each landmine: the question to ask, why it matters, and what the expected answer gap is for competitors.

### Red Flags

Signals that the prospect is not a good fit. Direct advice on when to walk away. Be honest — this builds trust with the sales team.

### Cost Comparison

Build vs Buy table. Columns: Cost Component | DIY Build (Estimated) | Our Product.

Include engineering costs, ongoing maintenance, specialist knowledge, testing, time to production. Add a note about assumptions.

---

## Content Rules

1. **British English** throughout
2. **Banned words** — same list as brand guidelines
3. **No overselling** — only claim what the source document supports
4. **No unfair competitor claims** — state limitations factually, never with contempt
5. **Talk tracks must sound human** — conversational, not scripted
6. **Status accuracy** — Planned items must be labelled if mentioned
7. **No emdashes or semicolons** in marketing copy
8. **No bare tildes before numbers** — `~70%` renders as strikethrough in PDF. Write "approx. 70%" or just "70%"
9. **Oxford comma** always

---

## Output

Write to `output/collateral/battle-card.md`. Report:
- File created with line count
- Competitor categories covered
- Number of objections handled
- Any gaps that need human attention
