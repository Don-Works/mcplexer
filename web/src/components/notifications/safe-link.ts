// Mesh and notification publishers are untrusted. Only route explicit links
// that stay inside this dashboard; protocol URLs, scheme-relative URLs, and
// backslash variants must fall back to a known product destination.
export function safeNotificationPath(link: string | undefined): string | null {
  const value = link?.trim()
  if (!value || !value.startsWith('/') || value.startsWith('//') || value.includes('\\')) {
    return null
  }

  try {
    const base = new URL('https://mcplexer.invalid/')
    const parsed = new URL(value, base)
    if (parsed.origin !== base.origin) return null
    return `${parsed.pathname}${parsed.search}${parsed.hash}`
  } catch {
    return null
  }
}
