// Humanise an auto-generated auth-scope slug for display.
//
// The quick-setup flow generates names like `clickup_oauth_agent_example_workspace`
// and `linear_oauth_gateway_linear` by concatenating server + provider ids.
// They are correct as stable handles but ugly in the UI.
//
// Strategy:
//   1. Strip well-known auth-type suffixes ("oauth", "client_credentials")
//      so we don't show the operator the same word twice next to the type
//      badge.
//   2. Drop the leading "gateway_" / "agent_" prefix injected by the
//      auto-naming code — these are gateway-internal markers, not info
//      the operator cares about.
//   3. Replace separators with spaces and title-case each word.
//   4. If the result collapses to empty (e.g. "oauth" alone), fall back
//      to the original slug so the user still sees something.
// Tokens that show up in auto-generated slugs but carry no operator
// signal — they're either the auth type ("oauth") repeated next to the
// type badge, or the gateway-internal source marker ("agent",
// "gateway") inserted between the server + provider slug, or the
// underscore-split halves of "client_credentials".
const NOISE_TOKENS = new Set([
  'oauth',
  'oauth2',
  'client',
  'credentials',
  'agent',
  'gateway',
])

export function humaniseScopeName(name: string): string {
  if (!name) return ''
  // Names that already contain a capital, a space, or a paren were
  // almost certainly hand-written by the operator (e.g. "FreeAgent
  // OAuth", "Intervals Pro (Local)"). Leave them alone — humanising
  // them lowercases the operator's intentional casing.
  if (/[A-Z\s()]/.test(name)) return name
  const tokens = name
    .split(/[_-]+/)
    .map((t) => t.trim().toLowerCase())
    .filter((t) => t && !NOISE_TOKENS.has(t))
  if (tokens.length === 0) return name
  return tokens
    .map((t) => t.charAt(0).toUpperCase() + t.slice(1))
    .join(' ')
}

// scopeLabel returns the operator-facing label for an auth scope:
// display_name when set, otherwise the humanised slug.
export function scopeLabel(scope: { name: string; display_name?: string }): string {
  if (scope.display_name && scope.display_name.trim()) return scope.display_name.trim()
  return humaniseScopeName(scope.name)
}
