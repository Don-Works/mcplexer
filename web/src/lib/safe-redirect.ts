export function redirectToOAuth(url: string): void {
  let parsed: URL
  try {
    parsed = new URL(url)
  } catch {
    throw new Error('Received an invalid authorization URL')
  }

  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    throw new Error('Received an unsafe authorization URL protocol')
  }

  // Use window.open() so Electron's setWindowOpenHandler intercepts it
  // and opens in the system browser instead of navigating the app window.
  window.open(parsed.toString(), '_blank')
}
