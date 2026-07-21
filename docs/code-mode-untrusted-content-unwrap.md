# Code-mode: auto-unwrap `<untrusted-content>` so driving browser tools doesn't need a manual parser

**Status:** proposal / implementation brief
**Area:** `mcpx__execute_code` (code-mode) result handling + the untrusted-content wrapping of low-trust tool results
**Origin:** driving the `brw` / `brw_chromium` browser tools through code-mode is far more painful than it should be — see "Symptom" below.

---

## Symptom

Inside `mcpx__execute_code`, tool results are supposed to auto-unwrap: an MCP
`{content:[{type:'text',text:'...JSON...'}]}` envelope comes back as a parsed JS
object, so you read `result.id` directly. That works for many tools.

But results from **low-trust (browser / web) tools** are wrapped by the gateway in
a prompt-injection guard:

```
{"kind":"text","text":"<untrusted-content source=\"tool:brw_chromium__brw_list_tab_groups\" trust=\"low\">[...JSON...]</untrusted-content>","bytes":...}
```

Because the `text` is no longer **pure** JSON (it's JSON wrapped in
`<untrusted-content>` tags), the code-mode auto-unwrap can't parse it. The caller
gets back a `{kind, text}` object (or a raw string) instead of the structured
value. So every agent driving these tools has to hand-roll the same unwrap:

```js
function unwrap(r){
  if (r && typeof r==='object' && typeof r.text==='string') r = r.text;
  if (typeof r==='string'){
    const m = r.match(/<untrusted-content[^>]*>([\s\S]*?)<\/untrusted-content>/);
    const inner = m ? m[1] : r;
    try { return JSON.parse(inner); } catch(e){ return inner; }
  }
  return r;
}
```

Worse, it's **inconsistent**: some browser tools (`brw_read`) come back already
parsed, others (`brw_list_tabs`, `brw_list_tab_groups`) come back wrapped. So the
agent can't even predict which results need the crutch — it ends up defensively
wrapping everything and burning round-trips debugging result shapes for what
should be one-shot navigation. (Verbatim user feedback: *"this integration should
fucking fly."*)

There's a secondary papercut: the sandbox lint emits *"JSON.parse() is
unnecessary on tool results — the sandbox auto-unwraps MCP envelopes"* **even when
JSON.parse IS necessary** (on a wrapped string), training agents to remove the
very call that makes it work.

## Why the wrapper exists (must be preserved for real content)

The `<untrusted-content trust="low">` wrapper is a deliberate **prompt-injection
defence**: web-derived strings (page text, titles, link text from `brw_read` /
`brw_find` / `brw_snapshot`) must reach the model marked as untrusted so the model
treats them as data, not instructions. **Do not remove the wrapper from genuine
page content.** The fix must keep that guarantee.

## The fix (hybrid: classify, then unwrap structurally)

The wrapping conflates two very different result kinds. Separate them:

1. **Structural / metadata results** — ids, titles, group names, tab lists:
   `brw_list_tabs`, `brw_list_tab_groups`, `brw_open` (returns a tab id), focus,
   group, status. These carry the daemon's *own* metadata; a tab id can't mount
   an injection. **Return these as clean JSON (no wrapper)** so code-mode
   auto-unwrap yields a structured object. (A page *title* embedded in a tab list
   is the one grey area — see "Open question".)

2. **Content results** — `brw_read`, `brw_find`, `brw_snapshot`, `brw_screenshot`
   alt-text, console text, network bodies: **keep the `<untrusted-content>`
   wrapper.**

Plus a defensive improvement to code-mode unwrap so the crutch isn't needed even
for wrapped JSON:

3. **Auto-unwrap should recognise a single `<untrusted-content>…</untrusted-content>`
   wrapper around a JSON body, parse the inner JSON, and return the structured
   value — while preserving the untrusted provenance** so the model still knows
   it's low-trust. Options for preserving provenance on the parsed value:
   - attach a non-enumerable `__source` / `__trust` marker to the returned
     object/array, or
   - return a thin tagged wrapper the print()/serialiser renders back inside
     `<untrusted-content>` tags when surfaced to the model, but which reads as the
     plain value in code (`.id`, `.elements`, etc.).
   Either way: **clean to program against, still untrusted when shown to the model.**

With (1)+(3), `brw_chromium.brw_list_tab_groups()` returns a parsed array directly,
`brw_chromium.brw_read()` content stays marked untrusted, and no agent ever writes
`unwrap()` again.

## Where to implement (pointers — verify against current code)

- The **untrusted-content wrapping** site: grep the gateway for `untrusted-content`
  / `trust="low"` / the per-tool or per-source trust classification that decides
  what gets wrapped. Add a "structural/trusted-metadata" classification for the
  brw structural tools (and any other downstream tool that returns pure metadata),
  so their results skip the wrapper.
- The **code-mode auto-unwrap**: the function that converts an MCP
  `{content:[{type:'text',text}]}` result into the JS value handed back inside
  `mcpx__execute_code`. Extend it to detect + strip a lone `<untrusted-content>`
  wrapper and `JSON.parse` the inner body (keeping a provenance marker).
- The **sandbox linter** rule that flags `JSON.parse` on tool results: suppress it
  when the result is a string containing `<untrusted-content>` (i.e. when the parse
  is genuinely required), or drop the rule once (3) lands and parsing is never
  needed.

## Acceptance criteria

- In `mcpx__execute_code`: `const g = brw_chromium.brw_list_tab_groups();` →
  `g` is a JS array; `g[0].title` works with **no** `unwrap()` / `JSON.parse`.
- `brw_chromium.brw_list_tabs()` and `brw_chromium.brw_open({url})` likewise
  return structured values directly.
- `brw_chromium.brw_read()` page text is **still** delivered to the model wrapped
  as untrusted content (injection guard intact) — add/keep a test asserting this.
- No lint warning when an agent does have to parse a genuinely-wrapped string.
- A regression test driving a couple of brw structural tools end-to-end with **no
  manual unwrap**.

## Open question for the implementer

Tab/group **titles and URLs** are web-derived but appear in structural results
(`brw_list_tabs`). Decide: (a) treat the whole structural result as trusted (titles
are low-risk as data, never executed) — simplest, recommended; or (b) keep the
result structured but wrap just the title/url string fields. (a) is almost
certainly fine and is what makes nav "fly"; (b) is purer but reintroduces parsing.

---

*Companion deliverable: ship a first-class `brw` skill into the mcplexer skill
registry (see `skills/brw.md`, brought over from the brw repo). brw is the
preferred browser-automation integration; playwright stays for the specific
scenarios brw can't cover.*
