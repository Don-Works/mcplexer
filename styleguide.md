# MCPlexer Visual Style Guide

This guide is based on the public MCPlexer website style and is intended for reuse across related projects so they feel like one product family.

## 1. Brand Direction

- Tone: technical, security-focused, local-first, terminal-native.
- Aesthetic: dark control-plane UI with cyan as the primary signal color.
- Personality: precise, trustworthy, minimal marketing fluff.

## 2. Design Tokens

Use these as CSS variables in every project:

```css
:root {
  --color-bg: #0a0b10;
  --color-bg-alt: #07080c;
  --color-surface: #12131a;
  --color-surface-hover: #1a1b24;
  --color-surface-elevated: #1e1f2a;
  --color-border: #1e2030;
  --color-border-hover: #2a2d42;

  --color-text: #e0e4ef;
  --color-text-muted: #8b90a0;
  --color-text-dim: #5a5f72;

  --color-cyan: #2ea4e0;
  --color-cyan-light: #5bbdee;
  --color-cyan-dark: #1a7ab5;

  --color-green: #2dd4a0;
  --color-red: #ef4444;
  --color-amber: #f59e0b;
}
```

## 3. Typography

- Primary face: `JetBrains Mono`.
- Fallback stack: `ui-monospace, SFMono-Regular, monospace`.
- Sizing rhythm:
- Hero wordmark: `clamp(4rem, 14vw, 11rem)`, weight `800`, letter-spacing `-0.04em`, line-height `0.9`.
- Section headings: `text-2xl` to `text-3xl`, bold.
- Body copy: `text-sm` default, `text-xs` for dense explanatory text.
- Labels/caps: `text-[10px] uppercase tracking-wider`.

## 4. Layout System

- Main container width: `max-w-6xl`.
- Horizontal padding: `px-4 sm:px-6`.
- Section vertical spacing: `py-20 sm:py-28`.
- Card spacing:
- Standard card: `p-6`.
- Compact feature card: `p-5`.
- Big CTA panel: `p-10 sm:p-16`.

## 5. Background + Atmosphere

- Base page background: `--color-bg`.
- Use layered texture:
- Grid overlay (`60px x 60px`) with subtle border color.
- Optional noise layer using low-opacity SVG turbulence.
- Optional radial vignette in hero.
- Accent effects:
- Cyan glow (`glow-cyan` / `glow-cyan-sm`) for hero terminal and final CTA.
- `text-gradient` only for short emphasis text.

## 6. Motion

- Keep animation sparse and meaningful.
- Approved motions:
- `fade-in` for section reveal (`~0.5s` to `0.6s`).
- `stagger` children by ~`80ms`.
- `pulse-cyan` for live/active indicators.
- `blink` terminal cursor.
- Glitch effect reserved for hero wordmark only.

## 7. Component Patterns

- Header:
- Fixed top bar (`h-14`) with translucent background + border.
- Left: icon + product name.
- Right: minimal nav + primary utility CTA.
- Hero:
- Dominant product name, short value prop, direct CTA pair.
- Include terminal mockup as proof-of-function.
- Cards:
- Surface background + 1px border.
- Hover behavior: border-only shift (`border` to `border-hover`), no heavy transforms.
- Feature icon tile: cyan tint background (`bg-cyan/10`) with cyan stroke/border.
- Terminal blocks:
- Background `--color-bg-alt`, bordered header with 3 status dots.
- Dense monospace content using success/warn/error colors.
- CTA section:
- Elevated surface + glow.
- One primary filled button + one secondary outline button.
- Optional install command chip.

## 8. Color Usage Rules

- Cyan is for action, selected state, key nouns, and trusted highlights.
- Green/amber/red are semantic status colors only (success, pending, error/risk).
- Keep text contrast high:
- Main text on dark backgrounds: `--color-text`.
- Long-form secondary text: `--color-text-muted`.
- Metadata/chrome text: `--color-text-dim`.

## 9. Responsive Behavior

- Mobile-first.
- Keep CTA groups as `flex-col` on mobile, `flex-row` on `sm+`.
- Grid transitions:
- 1 column -> 2 columns at `sm`.
- 2/3 columns at `lg` where content density allows.
- Preserve readability in terminal blocks with `overflow-x-auto`.

## 10. Reusable Tailwind Utility Conventions

If using Tailwind, preserve these naming patterns in global styles:

- `.terminal`, `.terminal-header`, `.terminal-dot`
- `.grid-pattern`, `.noise`, `.scanlines`
- `.glow-cyan`, `.glow-cyan-sm`
- `.animate-fade-in`, `.animate-pulse-cyan`, `.stagger`
- `.offset-line`, `.cursor-blink`

## 11. Starter CSS Snippet

```css
body {
  background: var(--color-bg);
  color: var(--color-text);
  font-family: "JetBrains Mono", ui-monospace, SFMono-Regular, monospace;
  -webkit-font-smoothing: antialiased;
  -moz-osx-font-smoothing: grayscale;
}

* {
  border-color: var(--color-border);
}

::selection {
  background: var(--color-cyan);
  color: var(--color-bg);
}
```

## 12. Do / Don’t

- Do keep layouts clean, data-dense, and engineering-oriented.
- Do use cyan accents intentionally instead of saturating every element.
- Do use terminal metaphors where they communicate behavior.
- Don’t introduce rounded, playful consumer-app styling.
- Don’t switch away from monospace-first typography for core brand surfaces.
- Don’t overuse motion or glitch effects outside hero contexts.
