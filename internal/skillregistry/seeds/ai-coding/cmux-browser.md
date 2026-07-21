---
name: cmux-browser
description: Automate browser interactions using the cmux built-in browser for any URL, form, dashboard, or web automation task
---

# cmux Browser

Automate browser interactions using the cmux built-in browser. No domain restrictions. Use for any URL, form, dashboard, or web automation task.

## Input

**Target:** `$ARGUMENTS` (URL to open, or instruction like "check the dashboard", "fill out the form at ...")

If no URL is provided, check for an existing browser surface first (Setup).

## Rules

**Multi-agent safety** — multiple agents may be running in separate cmux workspaces simultaneously.

1. **Always discover first.** Run `cmux --json tree` before creating or touching any browser surface. Parse the JSON to understand the full layout.
2. **Stay in your workspace.** All commands default to `$CMUX_WORKSPACE_ID`. Never target another workspace's panes or surfaces.
3. **Always create your own surface** unless the user explicitly tells you to reuse one. Other agents' browser surfaces are off-limits.
4. **Tag your surfaces immediately** after creation so ownership is visible in `cmux tree`:
   ```bash
   cmux rename-tab --surface surface:N "browse: <purpose>"
   ```
5. **Never close or navigate surfaces you did not create.**
6. **Record your handles.** Store the `surface:N` and `pane:N` returned at creation. All subsequent commands use the surface handle.

## Setup: Discover Layout & Create Browser Surface

**Step 1 — Discover the current layout:**
```bash
cmux --json tree
```
Parse the JSON output. For each workspace, check `panes[].surfaces[]`:
- `type: "browser"` → existing browser surface
- `title` → check for `"browse: ..."` tags (another agent's surface — do not touch)
- `url` → what it's showing
- `here: true` → the surface where this command was invoked (your terminal)

Identify: how many panes exist, which have browsers, which are yours (if any), and the pane ref of your terminal (`here: true`).

**Step 2 — Create your browser surface:**
```bash
# Default: new split in current workspace
cmux browser open [url]
# Returns: surface=surface:N pane=pane:N placement=split

# Explicit direction (right of your terminal is usually best):
cmux new-pane --type browser --direction right --url <url>

# Add as tab to a browser pane you already own:
cmux new-surface --type browser --pane pane:N --url <url>
```

**Step 3 — Tag and wait:**
```bash
cmux rename-tab --surface surface:N "browse: <purpose>"
cmux browser surface:N wait --load-state complete --timeout 15
```

**Step 4 — Check viewport and resize if needed:**
```bash
cmux browser surface:N eval 'JSON.stringify({w:window.innerWidth,h:window.innerHeight})'
```
If the viewport is too narrow (e.g., < 1024px for desktop sites), resize the pane:
```bash
cmux resize-pane --pane pane:N -R --amount 20
```

## Pane Layout

Record initial state before reorganizing: `cmux --json list-panes`

```bash
# Resize
cmux resize-pane --pane pane:N -R --amount 20   # wider (-L narrower, -D taller, -U shorter)

# Move / reorganize
cmux drag-surface-to-split --surface surface:N right  # surface → own split
cmux swap-pane --pane pane:N --target-pane pane:M      # exchange positions
cmux move-surface --surface surface:N --pane pane:M    # move surface between panes
cmux join-pane --target-pane pane:N                    # merge panes back
```

**Viewport follows pane size.** The browser renders at the pane's actual pixel dimensions. After resizing a pane, check the viewport again with `eval` to confirm the size is right for the task. (`viewport <width> <height>` exists in the CLI but is **not supported on WKWebView** — resize the pane instead.)


## Navigate

```bash
cmux browser surface:N navigate <url>
cmux browser surface:N wait --load-state complete --timeout 15
```

For SPAs, wait for a specific element:

```bash
cmux browser surface:N wait --selector ".content-loaded" --timeout 15
```

Check location: `cmux browser surface:N get url` / `get title`


## Read Page

```bash
# Full text (best for data extraction)
cmux browser surface:N get text --selector 'body'

# Accessibility tree (best for finding interactive elements)
cmux browser surface:N snapshot --compact

# Interactive elements only
cmux browser surface:N snapshot --interactive --compact

# Scoped reads
cmux browser surface:N get text --selector '.main-content'
cmux browser surface:N get html --selector 'table'
cmux browser surface:N get count --selector 'tr'
cmux browser surface:N get value --selector 'input[name=email]'
cmux browser surface:N get attr --selector 'a.cta' --attr href
```


## Interact

```bash
# Click
cmux browser surface:N click 'button.submit'

# Fill inputs (clears first)
cmux browser surface:N fill 'input[name=email]' 'user@example.com'

# Keyboard
cmux browser surface:N press Enter

# Dropdowns
cmux browser surface:N select 'select[name=country]' 'GB'

# Checkboxes
cmux browser surface:N check 'input[type=checkbox]'

# Scroll
cmux browser surface:N scroll --dy 500
cmux browser surface:N scroll-into-view '.footer'

# Hover
cmux browser surface:N hover '.dropdown-trigger'
```

Append `--snapshot-after` to any interaction to see the result immediately.

### Filling React-controlled inputs (the big WKWebView gotcha)

`cmux fill` writes the DOM `value` and dispatches an event, but **React's `onChange` often does not fire** on WKWebView, so controlled-component state stays empty and any "submit disabled until valid" button stays disabled. Symptom: `input.value === "maintainer@example.com"` but `props.value === ""` on the React fiber, and the submit button is still `disabled`.

Two workarounds that do work:

1. **Per-character keystrokes** — most reliable for plain `<input>`:
   ```bash
   cmux browser surface:N click 'input[name=email]'
   for c in m a x @ e x a m p l e . c o m; do
     cmux browser surface:N press "$c"
   done
   ```

2. **Native setter + bubbling input event** — needed for inputs that swallow synthetic keystrokes (notably **shadcn `InputOTP`**, which ignores `press` even when focused):
   ```bash
   cmux browser surface:N eval --script '
     const i = document.querySelector("[data-input-otp]");
     const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set;
     setter.call(i, "791842");
     i.dispatchEvent(new Event("input", { bubbles: true }));
   '
   ```

Verify the React state actually updated before clicking submit — read the fiber:
```bash
cmux browser surface:N eval --script '
  const i = document.querySelector("input[name=email]");
  const k = Object.keys(i).find(k => k.startsWith("__reactProps"));
  JSON.stringify({ domVal: i.value, reactVal: i[k]?.value, btnDisabled: document.querySelector("button[type=submit]").disabled })
'
```
If `reactVal` is empty or the button is still disabled, the state didn't propagate — try the other workaround.

### Clicking elements with obfuscated classes

Many SPAs use hashed CSS class names. When `click` fails with a `js_error`, fall back to JS:

```bash
cmux browser surface:N eval --script 'Array.from(document.querySelectorAll("button")).find(b => b.textContent.trim() === "Create Campaign").click()'
```

**Do not use** Playwright-style `:has-text()` pseudo-selectors — not supported in WKWebView.

To discover elements when class names are opaque:

```bash
cmux browser surface:N eval --script 'JSON.stringify(Array.from(document.querySelectorAll("button,a,[role=button]")).filter(e => e.textContent.includes("Target Text")).map(e => ({tag:e.tagName, cls:e.className.substring(0,60), text:e.textContent.trim().substring(0,40)})))'
```


## JavaScript Evaluation

```bash
cmux browser surface:N eval 'document.title'
cmux browser surface:N eval --script 'JSON.stringify(/* complex expression */)'
```

Use JS for: structured data extraction from complex DOM, triggering JS-only UI transitions, reading app state.


## Viewport & Screenshots

```bash
# Take a screenshot
cmux browser surface:N screenshot --out /tmp/screenshot.png
```

Viewport size is controlled by **resizing the pane** — `cmux browser ... viewport` is not supported on WKWebView and will return `not_supported`.

Then use the Read tool to view the image.

**Cropping a region:** Get element bounds via JS, then crop with ImageMagick. Screenshots are 2x retina — multiply coordinates by 2.

```bash
# Get bounds
cmux browser surface:N eval --script 'JSON.stringify(document.querySelector(".target").getBoundingClientRect())'
# Crop (WxH+X+Y, all values x2 for retina)
magick screenshot.png -crop 1280x862+758+754 cropped.png
```


## Multi-Tab Workflows

**Basic commands:**
```bash
cmux browser surface:N tab new
cmux browser surface:N tab list
cmux browser surface:N tab switch <index>
cmux browser surface:N tab close
```

**Reference + working tab pattern:**
```bash
# Tab 0: reference (docs, dashboard to monitor)
# Tab 1: working page (form, page under test)
cmux browser surface:N tab new
cmux browser surface:N navigate https://working-page.com
# Switch back to reference: tab switch 0
# Switch to working: tab switch 1
```

For multiple sites, repeat `tab new` + `navigate` for each. Use `tab list` to see indexes.

**Side-by-side alternative:** For two browsers visible simultaneously, open a second surface in a new pane: `cmux new-pane --type browser --direction right --url <url>`


## Debugging

```bash
cmux browser surface:N console list    # Console logs
cmux browser surface:N errors list     # JS errors
cmux browser surface:N dialog accept   # Dismiss alerts
```

`network requests` is not supported on WKWebView. To see what an interaction triggered, instrument with `eval` (wrap `fetch`/`XMLHttpRequest`) before clicking, or read app-side logs.


## Advanced

```bash
# Wait for dynamic content
cmux browser surface:N wait --text "Loading complete"
cmux browser surface:N wait --url-contains "/dashboard"
cmux browser surface:N wait --function 'window.appReady === true'

# Cookies / storage
cmux browser surface:N cookies get --name session
cmux browser surface:N storage local get --key authToken

# File uploads
cmux browser surface:N fill 'input[type=file]' '/path/to/file.png'

# Save/restore state
cmux browser surface:N state save /tmp/state.json

# Inject CSS/JS
cmux browser surface:N addstyle 'body { zoom: 0.8; }'
```


## Related skills

- **[google-chat](google-chat.md)** — Send/read messages on chat.google.com. Documents WKWebView quirks (eval blocked, no viewport set), DM thread ID extraction, and the right input/send selectors.

## Cleanup

Close surfaces you created. Rejoin split panes. Never close surfaces you didn't create.
```bash
cmux close-surface --surface surface:N
cmux join-pane --target-pane pane:ORIGINAL   # if you split panes
```


## Chrome Tools Coexistence

If both cmux browser and `mcp__claude-in-chrome__*` tools are available, **prefer cmux**. It runs in-process with no extension dependency, has no domain restrictions, and surfaces are tracked in `cmux tree`.

If using Chrome MCP tools instead (not in cmux):
1. Always call `tabs_context_mcp` first to establish a tab group
2. Always create new tabs with `tabs_create_mcp` — never reuse existing tabs from other sessions
3. Use the returned `tabId` for all subsequent calls

**Do not mix** cmux browser and Chrome MCP tools in the same session.


## Known Limitations (WKWebView)

- **No `:has-text()` selectors** — use JS `eval` with `Array.from(...).find(e => e.textContent.includes(...))` instead.
- **CSS `click` may fail on obfuscated classes** — fall back to JS `.click()` via `eval`.
- **`get text` requires `--selector`** — always pass `--selector 'body'` for full page text.
- **`fill` doesn't update React-controlled state** — the DOM gets the value but `onChange` may not fire, leaving submit buttons disabled. Use per-character `press` or the native-setter+input-event pattern. See "Filling React-controlled inputs" above.
- **`viewport <w> <h>`** — not supported. Resize the pane instead.
- **`network requests`** — not supported. Instrument with `eval` if you need to observe traffic.


## Workflow Pattern

1. **Discover** — `cmux --json tree` to understand the layout
2. **Create** — `cmux browser open` (never reuse others' surfaces)
3. **Tag** — `cmux rename-tab` to mark ownership
4. **Layout** — resize/split pane if needed
5. **Navigate** + wait for load
6. **Read** the page (text or snapshot)
7. **Act** (click, fill, select — fall back to JS `eval` if selectors fail)
8. **Verify** (read again, check URL, screenshot)
9. **Repeat** 6-8 until done
10. **Cleanup** — close your surfaces, restore layout

Always verify after interactions. Never assume success.
