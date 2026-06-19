---
name: generate-proposal
description: Transform product docs and client context into a tailored client-specific proposal
---

# Generate Client-Specific Proposal

Transform product documentation and client context into a tailored proposal that maps product capabilities to specific client requirements.

## Input

Read these files:
1. **Extraction:** `output/extraction.md` (run `/extract` first if this does not exist)
2. **Brand guidelines:** `config/brand-guidelines.md`
3. **Source document:** `$ARGUMENTS` (required)
4. **Client context:** Ask the user for client context if not provided. This can be:
   - A file path to client requirements, RFP, or infrastructure spec
   - A verbal description of the client's situation
   - A URL to fetch client information from

If no client context is available, stop and ask the user to provide it.

---

## Document Structure

Write to `output/collateral/{client-name}-proposal.md`. Ask the user for the client name if not obvious.

### Header

```markdown
# {Product Name} for {Client Name}
## How {Product Description} Addresses {Client Name}'s Requirements
```

### Executive Summary

2-3 paragraphs. State the client's situation, what they need, and how the product addresses it. Reference specific client requirements by name or quote.

### Requirement Mapping

This is the core of the document. For each relevant client requirement:

```markdown
### {N}. {Requirement Category}

**{Client name} requirement:**
> "{Quote or paraphrase from client context}"

**How {product name} addresses this:**

| Capability | How it applies to {client name} |
|------------|--------------------------------|
| {capability from extraction} | {specific application to this client} |
```

Include a **Key point for {client name}:** callout after each mapping that summarises the business value in one sentence.

### What the Product Does NOT Cover

Honest scope boundary. List client requirements that are outside the product's scope and note what other system or layer handles them. This builds trust.

### Deployment Recommendation

Phased deployment plan tailored to the client:
- **Phase 1:** Initial deployment / sandbox (weeks 1-4)
- **Phase 2:** Testing / integration (weeks 5-8)
- **Phase 3:** Production (weeks 9-12)

Adapt phases to the client's context. Include specific configuration steps relevant to their setup.

### Next Steps

Numbered list. Typically:
1. Technical alignment session (with duration and agenda)
2. Proof of concept deployment
3. Compliance or integration review

---

## Content Rules

1. **Every capability claim must trace to the source document** — do not invent features
2. **Map to client requirements specifically** — generic descriptions are not acceptable. Use the client's terminology, reference their systems, name their constraints
3. **British English** throughout
4. **Banned words** — same list as brand guidelines
5. **No emdashes** — use full stops. This is a client-facing document
6. **No bare tildes before numbers** — `~70%` renders as strikethrough in PDF. Write "approx. 70%" or just "70%"
7. **Honest scope boundaries** — always include what the product does NOT cover
8. **Planned items** must be labelled if included. Do not present them as available
9. **No pricing** unless the user explicitly provides pricing information
10. **No version metadata, "prepared by", or "confidential" boilerplate** — do not add these sections

---

## Output

Write to `output/collateral/{client-name}-proposal.md`. Report:
- File created
- Number of client requirements mapped
- Requirements not covered (out of scope)
- Any gaps where the product does not fully address a client need
