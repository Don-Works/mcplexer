# `.mcskill` Bundle Format

**Status:** Draft (M2.1)
**Owner:** mcplexer core
**Spec version:** 1
**Last updated:** 2026-04-30

This document specifies the on-disk layout, manifest schema, and validation
rules for `.mcskill` skill bundles. It is the long-lived format spec — the
companion Go types live in `internal/skills/manifest.go`.

This spec is **format only**. Installation logic (M2.2), capability
enforcement (M2.3), and signing (M2.4) are out of scope and have their own
spec documents.

---

## 1. Goals

A `.mcskill` bundle is a **shareable, versioned, content-addressed unit of
prompt + supporting files** that:

1. Can be exported from one mcplexer install and imported into another with
   zero hand-editing.
2. Declares all runtime capabilities up front so the user can give informed
   consent at install time.
3. Pins versions of the skills and downstream MCP servers it depends on so
   the same bundle behaves the same across environments.
4. Has a stable, parseable, human-readable manifest — TOML, not JSON, not
   YAML — so the file is comfortable to author by hand and review in PRs.
5. Leaves room for cryptographic signing (M2.4) without committing to a
   particular scheme today.

Non-goals:

- Replacing the `~/.claude/skills/*.md` convention. Bundles are a **wire
  format**; on-disk a skill is still primarily a markdown file.
- Defining a runtime sandbox. Capability declarations are advisory until
  M2.3 wires them into an enforcer.
- Locking in a registry / discovery protocol.

---

## 2. File extension and naming

A bundle is a **gzip-compressed tar archive** with the extension `.mcskill`.

```
{name}-{version}.mcskill
```

- `name` matches the manifest's `name` field.
- `version` matches the manifest's `version` field.
- Archives MUST contain a single top-level directory whose name is
  `{name}-{version}` so that extracting twice does not collide.

A SHA-256 of the raw `.mcskill` bytes is the bundle's content address. M2.4
will define how this digest interacts with signatures.

---

## 3. On-disk layout

Inside the archive's top-level directory:

```
{name}-{version}/
├── manifest.toml        # required, schema below
├── skill.md             # required, prompt body (markdown + YAML frontmatter)
├── README.md            # optional, human-readable docs
├── scripts/             # optional, executable helpers (M2.3 enforces perms)
│   ├── do-thing.sh
│   └── ...
└── assets/              # optional, static files (templates, images, prompts)
    └── ...
```

### 3.1 `manifest.toml`

The single source of truth. Every other file in the archive is reachable
from a path declared (directly or by convention) in the manifest.

### 3.2 `skill.md`

Markdown file with YAML frontmatter compatible with the existing
`~/github/personal/ai-coding/skills/*.md` format. Frontmatter MUST include
at least `name` and `description`; these MUST equal the manifest's `name`
and `description`. (The duplication is intentional — it keeps the markdown
self-describing if a user copies it out of the bundle.)

### 3.3 `scripts/`

Anything in `scripts/` is invocable by the skill via the runtime; M2.3 will
define exactly how. For now, a skill bundle should not assume scripts can
do anything the manifest's `[capabilities]` block does not declare.

### 3.4 `assets/`

Read-only static files. Available to scripts at a path resolved by the
skill runtime (TBD M2.3). No declarative meaning beyond "ship these
verbatim."

---

## 4. Manifest schema (`manifest.toml`)

### 4.1 Choice of TOML library

We use **`github.com/pelletier/go-toml/v2`** (v2.3.0+).

Rationale:

- Strict decoding with `DisallowUnknownFields()` — catches typos in
  user-authored manifests at parse time rather than as silent ignores.
- Better error messages than `BurntSushi/toml`, including line/column.
- Active maintenance and the de-facto modern Go TOML library.
- No CGO, no breaking-version drift.

### 4.2 Top-level fields

```toml
manifest_version     = 1                  # required, integer, currently 1
name                 = "blog-post"        # required, see §4.3
version              = "1.2.3"            # required, strict semver (§4.4)
description          = "Turn an idea into a blog post"   # required
author               = "Example Maintainer <maintainer@example.com>"  # optional, free-form
license              = "AGPL-3.0-or-later"              # optional, SPDX identifier
homepage             = "https://github.com/example/skills"  # optional URL
tags                 = ["writing"]        # optional, search keywords
entry_point          = "skill.md"         # optional, default "skill.md"
readme               = "README.md"        # optional, default "README.md"
mcplexer_min_version = "0.3.0"            # optional, strict semver
signature            = "<opaque>"         # optional, see §6
```

### 4.3 `name` rules

- Lowercase letters, digits, and hyphens only.
- Must start and end with an alphanumeric.
- Optional one-segment scope prefix `@scope/`, where `scope` itself follows
  the same rules. Example: `@example/internal`.
- Implementation regex (canonical):

  ```
  ^(?:@[a-z0-9][a-z0-9-]*[a-z0-9]/)?[a-z0-9][a-z0-9-]*[a-z0-9]$
  ```

### 4.4 Versioning convention

- `version` is strict [SemVer 2.0.0](https://semver.org/) — `MAJOR.MINOR.PATCH`
  with optional `-prerelease` and `+build`. No `v` prefix.
- The `(name, version)` tuple is unique. Re-publishing the same tuple with
  different bytes is forbidden. Bump the version (typically the patch).
- `mcplexer_min_version`, when set, is strict semver and is checked before
  the bundle is considered installable on the current host.

---

## 5. Dependencies

Skills can depend on other skills. The `[dependencies]` table is keyed by
skill name and each value is an inline table with a `version` constraint.

```toml
[dependencies]
cmux-browser = { version = "^1.0.0" }
"@example/internal" = { version = ">=2.1.0, <3.0.0" }
```

### 5.1 Range grammar

We accept the npm/cargo-flavoured grammar:

| Operator   | Meaning                                |
| ---------- | -------------------------------------- |
| `=` / none | Exact match (`=1.2.3` ≡ `1.2.3`)       |
| `!=`       | Not equal                              |
| `>`, `>=`  | Greater than [or equal]                |
| `<`, `<=`  | Less than [or equal]                   |
| `~`        | Approx, only patch may change (`~1.2`) |
| `^`        | Caret, no breaking change (`^1.2.3`)   |
| `1.2.x`    | X-range / wildcard                     |
| `*` / `""` | Any                                    |

Multiple comparators may be comma-separated and are AND-joined:
`">=1.2.0, <2.0.0"`.

The validator only checks **shape**. Range *evaluation* (resolving against
a registry) is M2.2's job and may pull in `golang.org/x/mod/semver` or a
similar library.

---

## 6. Capabilities

The `[capabilities]` table is **always present**. Validation requires the
table itself even when every nested field is empty/default. This forces
authors to make a deliberate choice about each axis.

```toml
[capabilities]

[[capabilities.mcp_servers]]
name    = "github"
version = "^1.0.0"

[[capabilities.mcp_servers]]
name     = "linear"
optional = true

[capabilities.network]
enabled       = true
allowed_hosts = ["api.github.com", "linear.app"]

[capabilities.filesystem]
mode  = "read_only"
paths = ["~/notes/...", "~/.config/mcplexer"]
```

### 6.1 `mcp_servers` (array of inline table)

Each entry references a downstream MCP server in the host mcplexer's
catalogue. **Schema decision:** `mcp_servers` is an array of *tables*, not
a flat array of strings. This is more verbose to author but lets us:

- Pin a semver range (`version`).
- Mark a server as `optional` so the skill installs even when the server
  is missing (the skill's scripts must guard their own calls).
- Add future fields (e.g. `min_tools`, `requires_capability`) without a
  breaking schema change.

| Field      | Type   | Required | Notes                                          |
| ---------- | ------ | -------- | ---------------------------------------------- |
| `name`     | string | yes      | Matches the gateway namespace (e.g. `github`). |
| `version`  | string | no       | Semver range; same grammar as §5.1.            |
| `optional` | bool   | no       | Default `false`.                               |

### 6.2 `network`

| Field           | Type     | Required | Notes                                                      |
| --------------- | -------- | -------- | ---------------------------------------------------------- |
| `enabled`       | bool     | no       | Default `false`.                                           |
| `allowed_hosts` | []string | no       | Allow-list of `host` or `host:port`. Empty + enabled = any. |

It is a validation error to set `allowed_hosts` while `enabled = false`.

### 6.3 `filesystem`

`mode` MUST be one of:

- `"none"` — no filesystem access. `paths` MUST be empty.
- `"read_only"` — read access to the listed paths. `paths` MUST be non-empty.
- `"read_write"` — read+write access. `paths` MUST be non-empty.

Paths support:

- A leading `~` (home expansion).
- A trailing `/...` to match the directory and any descendant.
- No glob characters; no `..` segments. (M2.3 will harden this.)

An unknown `mode` value is a validation error.

### 6.4 `mcplexer_min_version`

Top-level (not under `[capabilities]`) on purpose — it's a host-runtime
*requirement*, not a runtime capability declaration. Strict semver. The
installer (M2.2) compares this against the running mcplexer's build
version and refuses to install if it would underprovision.

---

## 7. Signing (forward-compat stub)

The top-level `signature` field is a free-form opaque string. Today it is
**ignored** by the parser and validator. M2.4 will pick a scheme
(candidates: ssh-ed25519 detached signature over the canonical archive
bytes, minisign, or sigstore bundle) and define the encoding.

Authors and tools today MUST NOT rely on `signature` having any meaning
beyond "preserved verbatim through parse → marshal round-trips."

Why now? Because the field needs a name and a place in the schema before
any tool starts emitting bundles, even if the contents are TBD. Adding a
top-level field later is a breaking schema change; reserving the slot now
is free.

---

## 8. Validation rules (summary)

`internal/skills.Validate` returns `ErrInvalidManifest` wrapping one or
more of these sentinels:

| Sentinel                          | Triggered by                                                       |
| --------------------------------- | ------------------------------------------------------------------ |
| `ErrMissingField`                 | Required field empty/absent (`name`, `version`, `description`,...) |
| `ErrInvalidName`                  | `name` does not match §4.3 regex                                   |
| `ErrInvalidVersion`               | `version`, `mcplexer_min_version`, or any range fails to parse     |
| `ErrUnsupportedManifestVersion`   | `manifest_version` ≠ supported list                                |
| `ErrInvalidCapability`            | unknown filesystem mode, mode/paths conflict, network conflict     |

Errors are joined with `errors.Join`; callers may match individual
sentinels with `errors.Is`.

Unknown top-level or nested TOML fields are a parse error (the decoder
runs with `DisallowUnknownFields()`). This protects users from typos like
`mcplxer_min_version` silently ignored.

---

## 9. Round-trip guarantee

`Parse(b)` followed by `Marshal(m)` followed by `Parse(b')` MUST produce
a Manifest deeply equal to the first. The test suite enforces this. This
makes the format safe to use as a wire format and as a canonical
on-install snapshot.

The wire form is *not* byte-stable (TOML allows multiple equivalent
serialisations). M2.4's signing scheme will define a canonicalisation if
it requires byte-stability.

---

## 10. Open questions deferred to later milestones

- M2.2: lockfile format for resolved dependency versions.
- M2.3: enforcement layer for `[capabilities]`.
- M2.4: signature encoding + key trust model.
- Future: compatibility shims for skills authored against older
  `manifest_version` values once we ship a v2.

---

## Appendix A — Full example

```toml
manifest_version = 1
name             = "blog-post"
version          = "1.2.3"
description      = "Turn an idea into a blog post"
author           = "Example Maintainer <maintainer@example.com>"
license          = "AGPL-3.0-or-later"
homepage         = "https://github.com/example/mcplexer-skills"
tags             = ["writing", "marketing"]
entry_point      = "skill.md"
readme           = "README.md"
mcplexer_min_version = "0.3.0"

[dependencies]
cmux-browser = { version = "^1.0.0" }

[capabilities]

[[capabilities.mcp_servers]]
name    = "github"
version = "^1.0.0"

[[capabilities.mcp_servers]]
name     = "linear"
optional = true

[capabilities.network]
enabled       = true
allowed_hosts = ["api.github.com", "linear.app"]

[capabilities.filesystem]
mode  = "read_only"
paths = ["~/notes/...", "~/.config/mcplexer"]
```
