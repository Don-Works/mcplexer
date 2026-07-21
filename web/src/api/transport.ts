const BASE = import.meta.env.VITE_API_BASE_URL || '/api/v1'

// Default per-request timeout. This is load-bearing, not a nicety: the
// gateway is served over plain http://, so the browser speaks HTTP/1.1 and
// caps us at ~6 connections per origin. Several always-on SSE streams
// (notifications/approvals/secret-prompts/…) each permanently hold one of
// those slots, leaving only a couple for everything else. Without a
// timeout a single slow response holds its connection open indefinitely;
// stacked sidebar polls then saturate the remaining slots and EVERY
// subsequent request hangs as "pending" with no way to recover (a classic
// connection-pool livelock — the symptom was "click around → whole UI goes
// to permanent loading"). A finite timeout guarantees a stuck request
// aborts and releases its connection so the pool always drains.
//
// 30s is comfortably above every healthy read/write (badge polls finish in
// well under a second) while still bounding recovery. Genuinely-long ops
// (downstream discovery, backups, external-API probes) pass an explicit
// larger `timeoutMs` below.
export const DEFAULT_TIMEOUT_MS = 30_000

export interface RequestOptions {
  // Override the default request timeout in ms. Pass 0 to disable entirely
  // (reserve for genuinely unbounded operations).
  timeoutMs?: number
}

// apiURL composes a fully-qualified path under the API base. Useful for
// non-JSON endpoints (file downloads, multipart uploads) that need to
// bypass the request<T> JSON wrapper.
export function apiURL(path: string): string {
  return `${BASE}${path}`
}

export class ApiClientError extends Error {
  status: number
  body: string

  constructor(status: number, body: string) {
    super(`API error ${status}: ${body}`)
    this.name = 'ApiClientError'
    this.status = status
    this.body = body
  }
}

// timeoutSignal returns an AbortSignal that fires after `ms`, combined with
// any caller-supplied signal so both an external cancel and the timeout can
// abort the fetch. Returns undefined when timeouts are disabled and no
// caller signal was given.
function timeoutSignal(ms: number, caller?: AbortSignal | null): AbortSignal | undefined {
  if (ms <= 0) return caller ?? undefined
  const timeout = AbortSignal.timeout(ms)
  if (!caller) return timeout
  // AbortSignal.any combines both; guard for older runtimes.
  if (typeof AbortSignal.any === 'function') return AbortSignal.any([caller, timeout])
  return caller
}

export async function request<T>(
  path: string,
  init?: RequestInit,
  opts?: RequestOptions,
): Promise<T> {
  const signal = timeoutSignal(opts?.timeoutMs ?? DEFAULT_TIMEOUT_MS, init?.signal)
  const res = await fetch(`${BASE}${path}`, {
    headers: { 'Content-Type': 'application/json' },
    ...init,
    ...(signal ? { signal } : {}),
  })
  if (!res.ok) {
    const body = await res.text()
    throw new ApiClientError(res.status, body)
  }
  if (res.status === 204) return undefined as T
  return res.json() as Promise<T>
}
