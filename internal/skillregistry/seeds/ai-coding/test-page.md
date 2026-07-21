---
name: test-page
description: Test a generated product page on the locally running website
---

# Test Website Page

Test a generated product page on the locally running website.

## Input

**Page path:** `$ARGUMENTS` (default: auto-detect from `output/web/page.tsx` metadata)

The page must already exist in the website repo. Read the website repo path from `config/project.md` (key: "Website repo path"). Default: `../web_development/`. Check for it at:
`{website-repo}/src/app/{slug}/page.tsx`

If the website repo is not cloned, tell the user to set it up (see CLAUDE.md).

---

## Browser Selection

Detect which browser automation is available:

**cmux (preferred):** Check `echo $CMUX_WORKSPACE_ID` — if set, use cmux browser. No domain restrictions, CSS selectors, Playwright-style waits.

**Claude-in-Chrome (fallback):** Use MCP tools if not in cmux.

---

## Workflow

### Step 1: Determine Page URL

Read the generated page's metadata to find the URL slug. If not obvious, ask the user.

### Step 2: Start Dev Server

```bash
cd {website-repo} && npm run dev
```

Wait for the server to report ready (usually at `http://localhost:3000`).

### Step 3: Desktop Test (1440x900)

**cmux:**
```bash
cmux browser open http://localhost:3000/{slug}/
cmux browser surface:N wait --load-state complete --timeout 15
cmux browser surface:N viewport 1440 900
cmux browser surface:N screenshot --out /tmp/test-desktop.png
```

**Claude-in-Chrome:**
1. Open new tab, navigate to URL
2. Resize to 1440x900
3. Screenshot

**Verify visually:**
- [ ] Hero section renders with title, subtitle, CTA button
- [ ] Hero image loads (not broken image icon)
- [ ] Stats bar shows cards in a row
- [ ] Feature sections have correct multi-column layouts
- [ ] Grid/swiper components render as grid on desktop
- [ ] Shared components (from `config/website-reference.md`) render at bottom
- [ ] Fonts render correctly (check heading and body fonts from `config/project.md` "Brand Colors" section, if configured)
- [ ] CTA buttons use the primary brand colour from `config/project.md` (if configured)

Scroll through the full page, taking screenshots of each section.

### Step 4: Mobile Test (390x844)

**cmux:**
```bash
cmux browser surface:N viewport 390 844
cmux browser surface:N screenshot --out /tmp/test-mobile.png
```

**Claude-in-Chrome:**
1. Resize to 390x844
2. Screenshot

**Verify:**
- [ ] Hero image moves below title text
- [ ] Stats bar shows 2 cards per row
- [ ] Multi-column sections stack to single column
- [ ] Grid/swiper components show as horizontal swiper
- [ ] CTA buttons are full-width
- [ ] Text is readable without horizontal scrolling
- [ ] No content overflows the viewport

### Step 5: Link Verification

**cmux:**
```bash
cmux browser surface:N snapshot --interactive --compact
# Check href attributes on CTA buttons
cmux browser surface:N get attr --selector 'a.cta' --attr href
cmux browser surface:N click 'a.cta'
cmux browser surface:N get url
```

**Claude-in-Chrome:**
Read page interactive elements via `mcp__claude-in-chrome__read_page`.

**Check all links:**
- [ ] CTA buttons link to the configured CTA pattern from `config/project.md` (default: `/contact/?intent={slug}`)
- [ ] Breadcrumb home link goes to `/`
- [ ] No broken links (404s)

Click at least one CTA button and verify navigation.

### Step 6: Lighthouse Audit

```bash
cd {website-repo} && npx lighthouse http://localhost:3000/{slug}/ --output json --output html --output-path ./lighthouse-{slug} --chrome-flags="--headless"
```

Report scores with pass/fail:
- [ ] Performance: target >= 80
- [ ] Accessibility: target >= 90
- [ ] Best Practices: target >= 90
- [ ] SEO: target >= 90

If any score is below target, list the specific issues and suggest fixes.

### Step 7: Build Test

```bash
cd {website-repo} && npm run build
```

Verify:
- [ ] Build completes without errors
- [ ] Static export generates the page directory
- [ ] No TypeScript errors

---

## Report

```markdown
| Test | Status | Notes |
|------|--------|-------|
| Desktop layout | PASS/FAIL | |
| Mobile layout | PASS/FAIL | |
| Links | PASS/FAIL | |
| Lighthouse Performance | Score | |
| Lighthouse Accessibility | Score | |
| Lighthouse Best Practices | Score | |
| Lighthouse SEO | Score | |
| Build | PASS/FAIL | |
```

List any issues that need fixing before publishing.
