---
name: mobile-pwa-polish
description: Use when auditing or fixing mobile web/PWA usability for app-like dashboards, including viewport overflow, unwanted zoom, safe-area insets, off-screen controls, hidden close buttons, dense responsive panels, and screenshot-based verification.
---

# Mobile PWA Polish

Audit and fix mobile web/PWA UX issues in app-like frontends where the user expects a stable installed-app viewport, dense operational screens, and controls that stay reachable on phones and desktop.

## First Pass

1. Identify the primary shell, viewport meta, global CSS, modal/sheet primitives, and the worst dense pages.
2. Reproduce at `320x568`, `375x812`, `430x932`, `768x1024`, and `1440x900`.
3. Check for horizontal document overflow, clipped fixed panels, focus zoom, notch/home-indicator overlap, hidden close buttons, and controls below the visible viewport.
4. Fix shared primitives before patching many pages individually.
5. Run the build and repeat the viewport checks after the final patch.

## Viewport And Zoom

For app-like PWAs whose product requirement is a fixed app viewport, use:

```html
<meta
  name="viewport"
  content="width=device-width, initial-scale=1.0, maximum-scale=1.0, user-scalable=no, viewport-fit=cover"
/>
```

If the product is content-heavy or public-web accessibility is the priority, do not disable pinch zoom. Instead keep `viewport-fit=cover` and prevent iOS focus zoom by making form controls `16px` on mobile.

Use CSS that makes the app own the viewport without letting fixed chrome exceed it:

```css
html,
body,
#root {
  min-height: 100dvh;
}

html,
body {
  overflow-x: hidden;
  overscroll-behavior: none;
}

@media (max-width: 767px) {
  input,
  select,
  textarea {
    font-size: 16px;
  }
}
```

Use `100dvh` for mobile app shells instead of bare `100vh`, and account for safe areas:

```tsx
<div className="h-[100dvh] [padding-top:env(safe-area-inset-top)]">
  <main className="pb-[calc(1rem+env(safe-area-inset-bottom))]" />
</div>
```

## Overflow Audit

Search for likely causes:

```bash
rg "w-\[|min-w-\[|w-screen|100vw|h-screen|overflow-x|fixed inset|DialogContent|SheetContent" web/src web/index.html
```

Common fixes:

- Add `min-w-0` to flex/grid children that contain long text.
- Replace mobile-visible fixed widths with `w-full max-w-*` or responsive widths.
- Keep page shells `overflow-x-hidden`, but put tables/code/matrices in explicit `overflow-x-auto` containers.
- Avoid body-level horizontal scroll for app shells; make the specific wide widget scroll instead.

## Dialogs And Sheets

Every modal, drawer, tray, and bottom/side panel needs:

- A visible close button reachable without a keyboard.
- A touch target around `40-44px` on mobile.
- `max-h` based on `100dvh`, not only `vh`.
- Internal scrolling that does not move the close affordance off-screen.
- Safe-area bottom padding for action bars and form footers.

Patch shared primitives first. For Radix/shadcn-style components, make `DialogContent` and `SheetContent` mobile-safe by default, then remove page-level overrides that fight those defaults.

## Dense Operational Pages

For dashboards, registries, task lists, audits, and config pages:

- Use compact vertical stacking on phones and dense multi-column layouts from `lg` upward.
- Keep filter/search bars single-row when possible; hide low-value labels on phone widths.
- Use icon buttons with `aria-label` for compact commands.
- Keep primary actions visible at the top of the flow.
- Add `truncate`, `break-words`, or `break-all` where ids, paths, hashes, and skill names can blow out a row.

## PWA Chrome

Verify:

- `manifest.webmanifest` has the intended `display`, icons, and theme colors.
- `apple-mobile-web-app-capable` and status-bar style match the layout.
- Header, bottom strips, and fixed trays avoid notches and the home indicator.
- Side drawers can close on touch devices and after navigation.

## Verification

Run the app and capture mobile and desktop evidence. Prefer the repo's existing browser tooling; Playwright is a good fallback:

```bash
cd web && npm run dev
npx playwright screenshot --viewport-size=320,568 http://localhost:5173 /tmp/mobile-320.png
npx playwright screenshot --viewport-size=375,812 http://localhost:5173 /tmp/mobile-375.png
npx playwright screenshot --viewport-size=430,932 http://localhost:5173 /tmp/mobile-430.png
npx playwright screenshot --viewport-size=1440,900 http://localhost:5173 /tmp/desktop-1440.png
```

For each target route, assert:

- `document.documentElement.scrollWidth <= window.innerWidth`
- Close buttons are visible and tappable.
- Header/sidebar/tray controls are on-screen.
- Inputs do not trigger mobile focus zoom.
- The build passes:

### Manual Checks

1. Open each page at 320px width
2. Scroll horizontally — should have zero horizontal scroll
3. Tap every button — touch targets should feel generous
4. Open/close every modal — close button visible and tappable
5. Check notch areas (simulator or real device)

## Build Verification

After all fixes:
```bash
cd web && npm run build
```

If the project embeds a built frontend in the backend binary, refresh and include the final dist only after source fixes are complete.

Must complete without errors. Check `dist/` output if embedded builds are required.

## Common Patterns in This Codebase

- `Sheet` / `SheetContent` from `@/components/ui/sheet` — mobile nav drawer
- `cn()` utility from `@/lib/utils` — conditional class merging
- `overflow-hidden` on root containers prevents content escape
- `min-w-0` on flex children prevents flex blowout
- `md:hidden` / `md:flex` for responsive visibility
- `px-4 md:px-6` for responsive horizontal padding

## Anti-Patterns to Avoid

- `overflow-x: hidden` without a containing parent (masks bugs)
- `position: fixed` without safe-area consideration
- `user-scalable=no` or `maximum-scale=1` (accessibility violation)
- Fixed widths on mobile-visible elements (`w-[800px]`)
- Touch targets < 44px without explicit desktop override
- Modal without close button relying on backdrop tap only
