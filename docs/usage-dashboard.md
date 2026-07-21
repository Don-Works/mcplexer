# AI usage dashboard

The AI usage dashboard gives operators one view of subscription allowance,
locally observed model use, and OpenRouter spend across harnesses. It covers
Claude Pro/Max, Codex, MiniMax, Z.AI, Grok, MiMo, and OpenRouter.

Open **Usage** in the web dashboard, or use the REST and MCP surfaces described
below. The dashboard is observational: it does not buy credits, change a plan,
or submit work to a provider.

## Reading the numbers

The normalized response deliberately keeps three kinds of consumption apart:

- **Live allowance windows** are provider-reported quotas such as a rolling
  five-hour allowance, weekly pool, token grant, or credit limit. Each window
  keeps its own unit and reset time.
- **Observed usage** is work MCPlexer can account for locally over
  `window_days`: requests, input/output/cache tokens, cost, and runs whose
  accounting was missing. Changing `days` changes this local window; it does
  not change a provider's quota window.
- **Credit spend** is metered money or credits reported by a provider such as
  OpenRouter. It is not converted into a subscription percentage unless the
  provider also reports a limit.

Missing data is never treated as zero. A provider row remains present with a
status and explanation. A numeric zero means the source explicitly reported
zero; `accounting_missing_runs` identifies successful local runs for which
MCPlexer could not establish token or cost accounting.

## Sources by provider

Automatic probes are best-effort and isolated: one unavailable CLI or failed
API does not prevent the other rows from rendering. MCPlexer never scrapes
browser cookies. Claude and Grok are queried through their logged-in CLIs;
their credential files are not opened by the dashboard.

| Provider | Live allowance source | Observed-use source and fallback |
| --- | --- | --- |
| Claude Pro/Max | A bounded, screen-reader-mode PTY opens the logged-in Claude CLI and reads `/usage`. It captures the current session, weekly all-model, and model-specific windows. `claude auth status --json` contributes only `subscriptionType`; identity fields are discarded. | MCPlexer worker-run accounting is shown when available. The probe runs in an empty, owner-only cache directory, declines project MCP activation, and never reads Claude credentials. |
| Codex | The installed, logged-in Codex CLI is probed through its local app-server rate-limit method. The plan and reset windows remain allowance data. | The app-server usage method supplies a separate observed total when available; it is never mixed into allowance windows. MCPlexer ledger accounting remains the fallback. |
| MiniMax | `GET /v1/token_plan/remains` using either an explicit encrypted auth-scope reference or the installed OpenCode credential. Interval and weekly Coding Plan windows are separated, and the API's misleading `usage_count` fields are treated as remaining counts. | OpenCode statistics provide local request/token observations and estimated list-price cost. A manual plan/window remains available as an explicit fallback. |
| Z.AI | The first-party monitor quota endpoint (`/api/monitor/usage/quota/limit`) using either an explicit encrypted auth-scope reference or the installed OpenCode credential. Its reported account level supplies the Coding Plan tier; empty zero-only meters are omitted. | MCPlexer run records and local OpenCode statistics supply observed totals. Custom API roots are limited to approved first-party HTTPS hosts. |
| Grok | A bounded PTY starts the logged-in Grok CLI and reads its billing extension event from a temporary `0600` debug file. This yields the subscription tier and current weekly shared period; percentage is omitted when Grok does not report it. | MCPlexer worker-run accounting is shown where available. No Grok auth file or shared log is read. |
| MiMo | `mimo providers whoami` verifies the local connection without retaining user identity. There is no supported live personal-plan allowance endpoint. | `mimo stats --days N --models` and MCPlexer run records provide observed usage. The UI says **Authenticated** while separately saying that live allowance data is unavailable. |
| OpenRouter | `GET /api/v1/key` reports total spend, limits, remaining credit, and daily/weekly/monthly spend where the key permits it. | OpenRouter model runs are grouped by harness and model from local CLI and MCPlexer accounting. Provider credit totals are not used to infer per-harness attribution. |

The CLI must already be logged in for a local probe to succeed. The daemon
resolves supported CLIs from `PATH` plus their standard user, Homebrew, and
macOS application install locations, so a minimal launchd `PATH` does not hide
an installed harness. CLI output and provider APIs can change; parse or
compatibility failures are surfaced in the affected row.
Optional live integration coverage is available with
`MCPLEXER_LIVE_USAGE_TEST=1 go test ./internal/usage/collectors -run TestLive`.

## Configure a source without exposing a token

MiniMax, Z.AI, and OpenRouter connect automatically when their fixed provider
entries exist in `~/.local/share/opencode/auth.json`; MiMo uses the installed
CLI and `~/.local/share/mimocode/auth.json`. The reader accepts only those two
fixed files and four fixed provider keys, rejects symlinks/non-regular or
oversized files, and never persists or logs the credential value.

An explicit source always overrides auto-discovery. Provider credentials can
instead stay in MCPlexer's encrypted secret store. Create or reuse an auth
scope through the normal secret workflow, then call
`mcplexer__configure_usage_source` with only the scope ID and key name:

```json
{
  "provider": "openrouter",
  "harness": "opencode",
  "plan": "metered",
  "auth_scope_id": "<existing-auth-scope-id>",
  "secret_key": "OPENROUTER_API_KEY"
}
```

`auth_scope_id` and `secret_key` are references, not secret values. Never put a
plaintext provider token in the tool call, configuration, chat, logs, or this
document; use `secret__prompt` when a human must supply one. The tool has no
plaintext-token field.

Supported configuration fields are:

- `provider`: `claude`, `codex`, `minimax`, `zai`, `grok`, `mimo`, or
  `openrouter`.
- `label` and `plan`: optional display context.
- `harness`: an optional label on the single OpenRouter account source.
  Per-harness usage attribution comes from local accounting, not from separate
  OpenRouter account keys.
- `auth_scope_id` plus `secret_key`: an existing encrypted credential
  reference; supply both or neither.
- `base_url`: optional approved first-party HTTPS API root for MiniMax, Z.AI,
  or OpenRouter. Defaults are normally preferable.
- `limit`, `unit`, `window_label`, and `window_minutes`: optional manual
  allowance context. Units are `percent`, `requests`, `credits`, `usd`, or
  `tokens`. A manual limit does not manufacture observed usage.

Reconfiguring the same provider replaces its source. Exactly one OpenRouter
account-credit source is accepted; its observed breakdown can still contain
any number of harnesses. Remove a source with
`mcplexer__remove_usage_source`.

Read or force-refresh the same normalized snapshot with
`mcplexer__get_usage_dashboard` and `mcplexer__refresh_usage_dashboard`.

## REST API and caching

Both endpoints use the normal MCPlexer HTTP authentication:

- `GET /api/v1/usage?days=30` returns the current snapshot. `days` defaults to
  30 and is bounded to 365.
- `POST /api/v1/usage/refresh?days=30` bypasses the usage cache, runs the
  configured probes, and returns the new snapshot.

Slow provider probes are cached for five minutes, and concurrent misses for
the same source share one in-flight probe. The response includes
`generated_at`, provider-level `updated_at`, and `stale` indicators so callers
can distinguish the response generation time from each source measurement.
Refresh failures are isolated per provider and do not erase successful rows.

Provider and OpenRouter statuses mean:

- `ok`: the configured source returned its current supported data, or an
  authentication-only provider such as MiMo verified its connection.
- `partial`: useful data exists, but at least one important dimension—usually
  a live allowance—is not measurable.
- `unconfigured`: no usable source or required credential reference exists.
- `unavailable`: the source is supported but cannot run on this host, or the
  provider has no supported machine-readable source.
- `error`: a configured probe was attempted and failed, for example because of
  authentication, network, timeout, or response-format failure.

`stale: true` is independent of status: it means the values are last-known
rather than a new provider response.

## Verification

Run the focused tests, then the normal repository checks:

```sh
go test ./internal/usage/... ./internal/store/sqlite ./internal/api ./internal/control
MCPLEXER_LIVE_USAGE_TEST=1 go test ./internal/usage/collectors -run TestLive -v
cd web && npm test -- src/__tests__/usage.test.ts src/__tests__/usage-labels.test.ts
cd web && npm run build
task lint
task build
```

Start the development server with `task dev`, open
`http://localhost:3333/usage`, and verify that every provider is present even
when unconfigured. With the HTTP bearer token supplied through your existing
secure environment, the API can also be checked directly:

```sh
curl --fail --silent --show-error \
  --header "Authorization: Bearer $MCPLEXER_API_TOKEN" \
  "http://127.0.0.1:3333/api/v1/usage?days=30"

curl --fail --silent --show-error --request POST \
  --header "Authorization: Bearer $MCPLEXER_API_TOKEN" \
  "http://127.0.0.1:3333/api/v1/usage/refresh?days=30"
```

Confirm that missing providers have an explicit status, that repeated GETs
within five minutes preserve provider `updated_at` values, that POST refreshes
the probes, and that no response or error contains a provider credential.
