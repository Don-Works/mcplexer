import { ApiClientError, apiURL, request } from './transport'

// Hammerspoon — optional macOS desktop-automation bridge. Three endpoints:
//   GET  /hammerspoon/snippet — embedded Lua bridge (download via <a href>).
//   POST /hammerspoon/install — write the snippet + rotate the bridge password.
//   POST /hammerspoon/probe   — 5-step diagnostic; result also persisted into
//                               the downstream row's CapabilitiesCache so the
//                               dashboard can render a traffic-light without
//                               re-probing on every page load.
export interface HammerspoonInstallResponse {
  ok: boolean
  files_written: string[]
  init_lua_modified: boolean
  init_lua_backup?: string
  reload_attempted: boolean
  reload_error?: string
  next_steps: string[]
}

export interface HammerspoonInstallErrorBody {
  error: string
  step: string
}

export interface HammerspoonProbeCheck {
  ok: boolean
  duration_ms: number
  detail?: string
}

export interface HammerspoonProbeRemediation {
  check: string
  title: string
  body: string
}

export type HammerspoonHealth = 'ok' | 'degraded' | 'broken'

export interface HammerspoonProbeResponse {
  health: HammerspoonHealth
  checks: Record<string, HammerspoonProbeCheck>
  probed_at: string
  remediation?: HammerspoonProbeRemediation[]
}

export function hammerspoonSnippetURL(): string {
  return apiURL('/hammerspoon/snippet')
}

export async function fetchHammerspoonSnippet(): Promise<string> {
  const res = await fetch(hammerspoonSnippetURL())
  if (!res.ok) {
    throw new ApiClientError(res.status, await res.text())
  }
  return res.text()
}

export function installHammerspoon(): Promise<HammerspoonInstallResponse> {
  return request(
    '/hammerspoon/install',
    { method: 'POST', body: JSON.stringify({}) },
    { timeoutMs: 90_000 },
  )
}

export function probeHammerspoon(): Promise<HammerspoonProbeResponse> {
  return request(
    '/hammerspoon/probe',
    { method: 'POST', body: JSON.stringify({}) },
    { timeoutMs: 90_000 },
  )
}
