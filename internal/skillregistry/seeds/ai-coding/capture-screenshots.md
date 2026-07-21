---
name: capture-screenshots
description: Capture screenshots of locally running services or any URL for product pages, docs, or marketing
---

# Capture Screenshots

Capture screenshots of locally running services or any URL for use in product pages, documentation, or marketing.

## Input

Read `config/project.md` if it exists. Look for the "Services" and "Screenshot Manifest" sections.

If no `config/project.md` exists or no services are listed, ask the user:
1. What services are running locally? (name, URL, start command)
2. What screenshots to capture? (description, view, filename)

## Prerequisites

Before starting, verify with the user:
1. Services are running locally (Docker Compose or similar). Confirm URLs.
2. Services have sample/seed data loaded

## Browser Selection

Detect which browser automation is available and pick the best option:

### Option A: cmux browser (preferred)

Check if running inside cmux:
```bash
echo $CMUX_WORKSPACE_ID
```

If set, use cmux browser. It has no domain restrictions, CSS-selector-based interactions, and Playwright-style waits.

### Option B: Claude-in-Chrome (fallback)

If not in cmux, use Claude-in-Chrome MCP tools. Note: some domains may be restricted.

---

## Capture Workflow

### Step 1: Initialise Browser

**cmux:**
```bash
cmux browser open [url]
# Returns surface=surface:N — use this handle for all commands
cmux browser surface:N wait --load-state complete --timeout 15
```

**Claude-in-Chrome:**
1. Call `mcp__claude-in-chrome__tabs_context_mcp` to get current browser state
2. Call `mcp__claude-in-chrome__tabs_create_mcp` to open a new tab

### Step 2: Capture Each Service

For each service listed in `config/project.md` (or provided by the user):

Use the screenshot manifest from `config/project.md` if available. Each row in the manifest defines:
- What to capture (description)
- Which view/page to navigate to
- Target filename

#### Desktop capture (1440x900)

**cmux:**
```bash
cmux browser surface:N navigate <url>
cmux browser surface:N wait --load-state complete --timeout 15
cmux browser surface:N viewport 1440 900
cmux browser surface:N screenshot --out output/screenshots/<filename>.png
```

**Claude-in-Chrome:**
1. Navigate: `mcp__claude-in-chrome__navigate`
2. Resize to 1440x900: `mcp__claude-in-chrome__resize_window`
3. Wait for load
4. Screenshot: `mcp__claude-in-chrome__computer` with action `screenshot`

#### Mobile capture (390x844)

**cmux:**
```bash
cmux browser surface:N viewport 390 844
cmux browser surface:N screenshot --out output/screenshots/<filename>-mob.png
```

**Claude-in-Chrome:**
1. Resize to 390x844: `mcp__claude-in-chrome__resize_window`
2. Screenshot → append `-mob` to filename

#### Scrolling captures

For full-page captures, scroll through sections:

**cmux:**
```bash
cmux browser surface:N scroll-into-view '.section-2'
cmux browser surface:N screenshot --out output/screenshots/<filename>-section2.png
```

**Claude-in-Chrome:**
Use `mcp__claude-in-chrome__computer` with scroll action, then screenshot.

### Step 3: Animated GIF (Optional)

If the user wants an animated demo:

**cmux:**
```bash
cmux browser surface:N screencast start
# Perform the navigation/interaction sequence
cmux browser surface:N navigate <url>
cmux browser surface:N click '.cta-button'
# ... more interactions
cmux browser surface:N screencast stop
```

**Claude-in-Chrome:**
Use `mcp__claude-in-chrome__gif_creator`:
1. Start recording
2. Navigate through the workflow
3. Stop recording → save with descriptive filename

Capture extra frames before and after actions for smooth playback.

### Step 4: Save and Report

1. Save all screenshots to `output/screenshots/`
2. Write `output/screenshots/manifest.md` with:
   - Filename, description, dimensions, viewport used
   - Notes on what each screenshot shows
   - Which images map to which website page sections

### Image Optimisation

Remind the user:
- Screenshots should be compressed before committing (PNG or WebP)
- Recommended: use `pngquant` or `cwebp` for compression
- Target: under 200KB per image for page performance
