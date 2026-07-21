# Addon worked example: absence.io (Hawk auth)

How to wrap a [Hawk](https://github.com/mozilla/hawk)-authenticated REST API as
an mcplexer addon, using **absence.io** (HR / leave management) as the worked
example. This exercises the `kind: hawk` auth path: the gateway computes a fresh
HMAC `Authorization: Hawk ...` header (nonce + timestamp + payload hash) on every
downstream request — something a static `bearer` / `api_key_header` addon cannot do.

> Addon specs (`~/.mcplexer/addons/*.yaml`) are user-specific and git-ignored
> (see `.gitignore`), so the spec is reproduced here as reference rather than
> committed as a live file.

## What absence.io needs

- Base URL: `https://app.absence.io/api/v2` (JSON).
- Auth: **Hawk**, using an **API Key ID** + **API Key** (the shared secret),
  generated in absence.io under *Profile → Integrations*. List endpoints are
  `POST`s with a JSON body (`skip`, `limit`, `filter`).

## 1. Hawk auth scope

Create a `hawk`-type auth scope and populate its three secrets. **The gateway
reads these exact (uppercase) keys:**

| key              | value                                                       |
|------------------|-------------------------------------------------------------|
| `HAWK_ID`        | the Hawk **id** — absence.io's *API Key ID*                 |
| `HAWK_KEY`       | the Hawk **key** — absence.io's *API Key* (shared secret)   |
| `HAWK_ALGORITHM` | HMAC algorithm — `sha256`                                   |

```bash
# MCP admin: mcplexer.create_auth_scope({ name: "absence-io", type: "hawk" })
# Populate (placeholders — never commit real creds; HAWK_KEY is the secret,
# best entered via the dashboard secret UI rather than a shell command line):
export MCPLEXER_AGE_KEY=~/.mcplexer/mcplexer.db.age
S=<absence-io-scope-id>
mcplexer secret put "$S" HAWK_ID        '<ABSENCE_API_KEY_ID>'
mcplexer secret put "$S" HAWK_KEY       '<ABSENCE_API_KEY_SECRET>'
mcplexer secret put "$S" HAWK_ALGORITHM 'sha256'
```

## 2. Provision the addon

Let `create_addon` provision the host — do **not** hand-create a separate
namespace server first (that produces a duplicate `absence` server). It writes
`~/.mcplexer/addons/absence.yaml`, creates an internal `addon-host-absence`
server, and hot-reloads the tool catalogue:

```js
mcpx.create_addon({
  name: "absence",
  base_url: "https://app.absence.io/api/v2",
  parent_server: "addon-host-absence",   // the host it provisions
  auth_scope: "absence-io",              // the hawk scope from step 1
  auth: { kind: "hawk" },
  endpoints: [ /* list_absences, list_users — see spec below */ ],
})
```

Then add one allow route so the tools dispatch with the scope's auth, and
restart (routes load at daemon boot):

```js
mcplexer.create_route({ workspace_id: "global",
  downstream_server_id: "addon-host-absence",
  auth_scope_id: "<absence-io-scope-id>",
  policy: "allow", path_glob: "**", tool_match: ["absence__*"], priority: 100 })
```
```bash
systemctl --user restart mcplexer.service   # loads the new route
# (adding/altering the HAWK_* secrets afterwards is live — no restart needed)
```

## 3. Addon spec (reference)

`~/.mcplexer/addons/absence.yaml`:

```yaml
# absence — custom MCP addon. absence.io v2. Hawk-signed.
parent_server: addon-host-absence
auth_scope: absence-io
tools:
  - name: list_absences
    description: 'Query absences (body: skip, limit, filter JSON: date range/status).'
    method: POST
    url: https://app.absence.io/api/v2/absences
    input_schema:
      type: object
      properties:
        skip:   { type: integer, description: Pagination offset }
        limit:  { type: integer, description: Max records (default 50) }
        filter: { type: string,  description: JSON filter object }
  - name: list_users
    description: Query users (team roster).
    method: POST
    url: https://app.absence.io/api/v2/users
    input_schema:
      type: object
      properties:
        skip:  { type: integer, description: Pagination offset }
        limit: { type: integer, description: Max records }
```

## 4. Use

```js
absence.list_users({ limit: 200 })
absence.list_absences({ limit: 200, filter: '{"start":{"$gte":"2026-05-25"},"end":{"$lte":"2026-06-05"}}' })
```

A typical use: a team-status report's **People** section (who's on leave /
capacity during the reporting period).
