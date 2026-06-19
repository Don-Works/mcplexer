// parse.ts — utilities for accepting either the QR JSON payload or a
// `mcplexer://pair/<code>?peer=...&u=...&n=...` URL on the EnterCodeModal.
// The returned shape mirrors the QRPayload interface with optional fields so
// the caller can fall back to user-typed inputs when the payload is partial.

export interface PairingInput {
  code?: string
  peer_id?: string
  display_name?: string
  user_id?: string
}

// PAIRING_URL_PREFIX is the custom-protocol prefix used in the QR payload
// and copy/paste flow. Browsers don't open mcplexer:// URLs natively — the
// PWA's manifest could register a protocol handler if we want that — but
// the parser accepts the format either way so QR scans + clipboard pastes
// work regardless of who opens the link.
export const PAIRING_URL_PREFIX = 'mcplexer://pair/'

// parsePairingURL reads a mcplexer://pair/<code>?peer=...&u=...&n=... URL and
// returns the parsed fields. Returns null when the input doesn't look like a
// pairing URL (caller can then try JSON parsing).
//
// The URL shape is intentionally small: <code> in the path, peer/user_id/
// display_name in the query string, all url-encoded. No multiaddrs — same
// reasoning as the QR payload (we resolve via DHT post-pair).
export function parsePairingURL(input: string): PairingInput | null {
  const trimmed = input.trim()
  if (!trimmed.toLowerCase().startsWith(PAIRING_URL_PREFIX)) {
    return null
  }
  let url: URL
  try {
    url = new URL(trimmed)
  } catch {
    return null
  }
  // The "host" portion of mcplexer://pair/<code> is `pair`; the code is the
  // first path segment. Empty segments (trailing slash, double slash) are
  // skipped so paste-URL fallback survives copy-pasted whitespace.
  const segments = url.pathname.split('/').filter(Boolean)
  const code = segments[0]
  if (!code || !/^\d{6}$/.test(code)) {
    return null
  }
  const peer = url.searchParams.get('peer') ?? undefined
  if (!peer) return null
  const user = url.searchParams.get('u') ?? undefined
  const name = url.searchParams.get('n') ?? undefined
  return {
    code,
    peer_id: peer,
    user_id: user,
    display_name: name,
  }
}

// parsePairingJSON parses the historic QR-encoded JSON payload (current QR
// shape: code + peer_id [+ optional user_id + display_name]). Returns null
// when input isn't a valid JSON object — caller should treat that as a soft
// failure.
export function parsePairingJSON(input: string): PairingInput | null {
  let raw: unknown
  try {
    raw = JSON.parse(input)
  } catch {
    return null
  }
  if (typeof raw !== 'object' || raw === null) return null
  const obj = raw as Record<string, unknown>
  const code = typeof obj.code === 'string' ? obj.code : undefined
  const peer = typeof obj.peer_id === 'string' ? obj.peer_id : undefined
  const user = typeof obj.user_id === 'string' ? obj.user_id : undefined
  const name = typeof obj.display_name === 'string' ? obj.display_name : undefined
  if (!peer) return null
  return { code, peer_id: peer, user_id: user, display_name: name }
}

// parsePairingInput accepts either format and returns the parsed shape.
// Tries URL first (cheap pattern check), then JSON.
export function parsePairingInput(input: string): PairingInput | null {
  return parsePairingURL(input) ?? parsePairingJSON(input)
}
