---
name: publish-page
description: Copy a generated product page to the website repo, update navigation, and create a PR
---

# Publish Page to Website Repository

Copy the generated product page and assets to the website repository, update navigation, and create a pull request.

## Input

**Page slug:** `$ARGUMENTS` (default: auto-detect from `output/web/page.tsx` metadata)

## Prerequisites

Before running this skill:
1. Website repo is cloned at the path specified in `config/project.md` (key: "Website repo path", default: `../web_development/`)
2. Page has been generated at `output/web/page.tsx`
3. Screenshots exist in `output/screenshots/` (or will be captured separately)
4. Page has been tested locally (run `/test-page` first)

---

## Workflow

### Step 1: Determine Page Identity

Read the generated page to determine:
- **Slug** (URL path segment, e.g. `enterprise-privacy`)
- **Display name** (for navigation, e.g. "Enterprise Privacy")
- **Icon** (for navigation menu, e.g. `shield-check`)

### Step 2: Copy Files

Read the website repo path from `config/project.md` (key: "Website repo path"). Default: `../web_development/`.

```bash
# Create page directory
mkdir -p {website-repo}/src/app/{slug}/

# Copy page component
cp output/web/page.tsx {website-repo}/src/app/{slug}/page.tsx

# Copy any additional components (GlossaryTerm, ImageLightbox, etc.)
for f in output/web/*.tsx; do
  [ "$(basename "$f")" != "page.tsx" ] && cp "$f" {website-repo}/src/app/{slug}/
done

# Copy screenshots to public images (suppress errors for missing files)
cp output/screenshots/{slug}-*.png {website-repo}/public/images/ 2>/dev/null || true
```

### Step 3: Update Navigation

Read navigation settings from `config/project.md`:
- **Header component path** (default: `src/components/layout/Header/Header.tsx`)
- **Navigation location** (the menu group to add the page to)
- **Menu icon path** (default: `/menu-icons/{icon}.svg`)

Edit `{website-repo}/{header-component-path}`.

Find the configured navigation location and add:

```tsx
{ name: '{display name}', link: '/{slug}/', icon: '{menu-icon-path}', isNew: true },
```

Add it after the last existing entry in the group. If no navigation location is configured, ask the user where to add the page.

### Step 4: Create Branch and Commit

```bash
cd {website-repo}

# Create feature branch
git checkout -b {slug}-page

# Stage specific files only
git add src/app/{slug}/
git add public/images/{slug}-*.png 2>/dev/null || true
git add {header-component-path}

# Commit
git commit -m "$(cat <<'EOF'
Add {display name} product page

New product page at /{slug}/ for {product description}.
- Navigation: Added to site navigation menu

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"

# Push
git push -u origin {slug}-page
```

### Step 5: Create Pull Request

```bash
gh pr create --title "Add {display name} product page" --body "$(cat <<'EOF'
## Summary
- New product page at `/{slug}/` generated from technical documentation
- Follows the design pattern from existing product pages
- Added to site navigation

## Test plan
- [ ] `npm run dev` — page renders at /{slug}/
- [ ] Desktop layout (1440px) — hero, stats grid, feature cards align correctly
- [ ] Mobile layout (390px) — responsive stacking, swiper cards work
- [ ] `npm run build` — static export succeeds without errors
- [ ] All CTA links use the configured link pattern
- [ ] Images load correctly (no broken references)
- [ ] Lighthouse accessibility score >= 90
- [ ] Navigation entry appears in the configured menu location

Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

### Step 6: Report

Output the PR URL and a summary of what was published.

---

## Rollback

If anything goes wrong:
```bash
cd {website-repo}
git checkout main
git branch -D {slug}-page
```
