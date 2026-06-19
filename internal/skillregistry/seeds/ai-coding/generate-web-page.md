---
name: generate-web-page
description: Transform a technical feature document into a Next.js product page
---

# Generate Website Product Page

Transform a technical feature document into a Next.js product page. Works with any product and any Next.js website. Reads component patterns from `config/website-reference.md`.

## Input

Read these files:
1. **Source document:** `$ARGUMENTS` (required)
2. **Extraction:** `output/extraction.md` (run `/extract` first if this does not exist)
3. **Brand guidelines:** `config/brand-guidelines.md`
4. **Design system reference:** `config/website-reference.md` (component API, design tokens, responsive patterns)

Also read a reference page from the target website for structural patterns:
- Read the website repo path from `config/project.md` (key: "Website repo path"). Default: `../web_development/`
- Look in `{website-repo}/src/app/` for an existing product page to use as a template
- If the website repo does not exist, tell the user to clone it (see CLAUDE.md setup)

If no `config/website-reference.md` exists, read the reference page carefully and infer the component API from its usage.

---

## Page Generation

### Step 1 — Derive Page Identity

From the source document, determine:
- **Product name** — the main heading or title
- **URL slug** — kebab-case version of the product name (e.g. "Enterprise Privacy Suite" → `enterprise-privacy`)
- **Page title** — for metadata and hero
- **Tagline** — one-line subtitle
- **Description** — 1-2 sentence summary for meta description
- **CTA intent** — the intent parameter for booking links (e.g. `enterprise-privacy`)
- **Icon** — a suitable icon name from the design system (e.g. `shield-check`)

### Step 2 — Plan Sections

Read the extraction and source document. Plan the page sections based on the product's structure. A typical page has:

1. **JSON-LD** — WebPage and Service schemas
2. **Body style override** — match the pattern from the reference page
3. **Breadcrumbs** — product name and icon
4. **Hero** — title, tagline, description, CTA button, hero image (desktop absolute right, mobile below text)
5. **Stats bar** — 4 key metrics in a grid of Card components
6. **Component sections** — one section per major product component. Alternate backgrounds. Each section has:
   - Title and description
   - Feature cards (3-column grid) or bullet list with tick icons
   - Optional screenshot image
7. **Capability grid/swiper** — related capabilities using the components from `config/website-reference.md`
8. **How It Works / Integration section** — how components work together
9. **Use Cases** — target industry/persona cards
10. **CTA section** — Card with background image, title, and demo button
11. **Additional shared components** — use any shared components documented in `config/website-reference.md` (e.g. callback, social proof, partner logos)

Adapt the number and type of sections to fit the product. Not every product needs every section type.

### Step 3 — Generate Glossary

Create a `glossary` object containing plain-English definitions for every technical term used on the page. Wrap technical terms in `<GlossaryTerm>` components for interactive tooltip definitions. Generate the `GlossaryTerm` component as a separate file if it does not already exist.

### Step 4 — Generate Page Component

Write a complete Next.js App Router page component using the imports and patterns from the reference page and `config/website-reference.md`. Use the exact import paths and component names documented there. If no reference exists, read an existing product page from the website repo and infer the component API from its usage.

**Metadata:** Include `title`, `description`, `keywords`, `alternates.canonical`, `openGraph` (with image), and `twitter` card.

**Responsive:** Use `lg:hidden` / `max-lg:hidden` for desktop/mobile variants. Mobile-first with `md:` and `lg:` breakpoints.

**Images:** Use `next/image` with `unoptimized` prop. Include `alt` text that describes the content, not just the component name.

### Step 5 — Generate Asset Manifest

Write `output/web/assets-needed.md` listing every image referenced in the page:

| Asset | Description | Dimensions | New? |
|-------|-------------|-----------|------|
| `{slug}-hero.png` | Hero image | 600x600 (source) | Yes |
| `{slug}-{section}.png` | Section screenshot | 580x400 | Yes |
| ... | ... | ... | ... |

---

## Output

Write to:
- `output/web/page.tsx` — the page component
- `output/web/GlossaryTerm.tsx` — glossary tooltip component (if not already present)
- `output/web/assets-needed.md` — image manifest

## Content Rules

- **British English**, no banned words, enterprise language from brand guidelines
- **Only show shipped features** — do not include Planned items on the website
- **Translate technical to business value** in every feature description
- **CTA links** should use the pattern from `config/project.md` (key: "CTA link pattern"). Default: `/contact/?intent={slug}`
- **No emdashes** in page copy
- **No bare tildes before numbers** — `~70%` renders as strikethrough in some renderers. Write "approx. 70%" or just "70%"
- **Consistent Tailwind patterns** from the reference page

## Verification

After generating, verify:
- TypeScript compiles (no type errors in JSX)
- All imports match the existing component API from the reference page
- No inline styles except the body override
- Image paths are consistent with the asset manifest
- All text follows brand guidelines
- Mobile and desktop layouts are handled with responsive classes
