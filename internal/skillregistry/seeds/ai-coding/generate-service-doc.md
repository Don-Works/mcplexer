---
name: generate-service-doc
description: Create comprehensive service documentation from an extraction file as the master source for all collateral
---

# Generate Service Documentation

Create comprehensive service documentation from an extraction file. This is the master source document from which all other collateral is derived.

## Input

Read these files:
1. **Extraction:** `output/extraction.md` (run `/extract` first if this does not exist)
2. **Brand guidelines:** `config/brand-guidelines.md`
3. **Source document:** `$ARGUMENTS` (required) — for reference and fact-checking

---

## Document Structure

Write to `output/collateral/service-doc.md`. Follow this structure:

### Key Terms

A glossary table of technical terms used in the document. Each entry: Term | Plain-English explanation. Include only terms that a non-technical enterprise buyer would not know. Keep explanations to one sentence.

### One-Liner

Single sentence describing the product. Must be understandable to someone with no domain expertise.

### Overview

2-3 paragraphs. What the product does, what its components are, and why it matters. No jargon without explanation.

### The Problem

What enterprise operators struggle with today. Use numbered points. Each point: name the problem, then state the business impact.

### The Solution

Benefits table with columns: Benefit | What It Actually Means | Why It Matters | Proof Point.

Every row must trace back to a specific capability in the source document.

### Features by Category

Group features by the source document's top-level structure. For each category, use a table: Feature | Description | Why It Matters | Status.

- **Description** should translate the technical capability to business value
- **Status** should be "Shipped", "Shipped" (for previously In Progress items), or "Planned"
- Planned items go in a separate "Roadmap" subsection at the end

### Service Deliverables

Table: Deliverable | Description. What the customer actually receives.

### What We Need From You

Table: Requirement | Why We Need It. Customer prerequisites for deployment.

### Technical Differentiators

For each alternative or competitor category:
- Comparison table: Dimension | Our Product | Alternative
- Focus on capabilities the alternative cannot match

### Why DIY Fails

Table: DIY Assumption | Reality. Bust common misconceptions about building in-house.

### Objection Handling

For each common objection:
- The objection as a heading
- Full response paragraph with proof points
- WHAT / WHY IT MATTERS / PROOF / TALK TRACK structure

### Sales Talking Points

#### Elevator Pitch (30 seconds)

A concise spoken pitch. Must sound natural when read aloud.

#### By Persona

For each target buyer persona (e.g. CISO, Head of Compliance, CTO, Business Stakeholder):
- 1-2 paragraph talk track
- Written as if the salesperson is speaking in a meeting
- Include a pause point and a question to hand control back to the buyer
- Must sound human, not scripted

---

## Content Rules

Apply these throughout:

1. **WHAT / WHY IT MATTERS / PROOF** — every technical claim needs all three
2. **TALK TRACK** — major selling points should include a conversational script
3. **British English** — colour, organisation, optimisation, behaviour, licence (noun), practise (verb)
4. **Banned words** — apply the banned word list from `config/brand-guidelines.md`
5. **Enterprise language** — apply the enterprise terminology table from `config/brand-guidelines.md`
6. **No overselling** — only claim what the source document states. If a number is not in the spec, do not invent one
7. **Status accuracy** — Planned items must be labelled. Never present unreleased features as shipped
8. **Tables over paragraphs** — prefer tables for scanability
9. **Talk tracks must sound human** — read them mentally. Rewrite anything that sounds robotic
10. **No emdashes** in marketing copy. Use a full stop and start a new sentence
11. **No semicolons** in marketing copy. Split into two sentences
12. **No bare tildes before numbers** — `~70%` renders as strikethrough in PDF. Write "approx. 70%" or just "70%"
13. **Oxford comma** always

---

## Quality Checklist

Before writing final output, verify:
- [ ] Every technical claim has WHAT / WHY IT MATTERS / PROOF
- [ ] No banned words appear
- [ ] British English throughout
- [ ] All Planned items are clearly labelled
- [ ] Talk tracks sound natural when read aloud
- [ ] Key Terms glossary covers all jargon
- [ ] No "Related Documents" section included

---

## Output

Write to `output/collateral/service-doc.md`. Report:
- File created with line count
- Sections completed
- Any gaps that need human attention
