// Browser-native fallback for useful signals when this browser has permission
// but no Web Push subscription. Subscribed PWAs receive the server push instead,
// so an open tab never stacks a duplicate notification for the same event.
//
// Architecture: this module no longer opens its own EventSource streams
// (it used to subscribe to /approvals/stream + /notifications/stream a
// SECOND time, on top of the always-on subscriptions from
// `useApprovalStream` and `useSignalStream`). Doubled SSE subscriptions
// blew past Chrome's 6-per-origin HTTP/1.1 cap and stalled every later
// fetch — the "click-Dashboard-and-it-hangs" bug. Instead, the canonical
// Signal SSE hook calls the foreground fallback below.
//
// Mounted once at App root. Kept as a no-op compatibility hook so App owns the
// notification bridge lifecycle, but permission prompts now happen only from an
// explicit user gesture in the PWA page.

import { safeNotificationPath } from './safe-link'

export function useOsNotifications() {}

interface SignalLike {
  message_id: string
  title: string
  body?: string
  priority?: string
  // kind + source + link let the OS banner route to the right page —
  // an approval signal lands on /approvals, a secret prompt on
  // /approvals, etc. Without these the banner always dumped users on
  // /mesh?msg=<id> which never resolves for non-mesh signals.
  kind?: string
  source?: string
  link?: string
}

// destinationForSignal mirrors right-now-stream.tsx's destinationFor() so
// OS banner clicks land where the in-app signal-row click would. When the
// publisher set a same-origin relative Link, we preserve the deep path. Mesh
// content is untrusted, so protocol, scheme-relative, and backslash variants
// fall through to the kind-derived product route.
export function destinationForSignal(evt: SignalLike): string {
  const explicit = safeNotificationPath(evt.link)
  if (explicit) return explicit
  const kind = (evt.kind || evt.source || '').toLowerCase()
  if (kind.includes('approval')) return '/approvals'
  if (kind.includes('secret')) return '/approvals'
  if (kind.startsWith('memory')) return '/memory'
  if (kind.startsWith('task')) return '/app'
  if (kind === 'mesh' || kind.includes('mesh')) {
    return evt.message_id ? `/mesh?msg=${encodeURIComponent(evt.message_id)}` : '/mesh'
  }
  return '/signals'
}

export function isForegroundNotificationEligible(evt: SignalLike): boolean {
  const priority = (evt.priority || '').trim().toLowerCase()
  const source = (evt.source || '').trim().toLowerCase()
  const kind = (evt.kind || '').trim().toLowerCase()
  if (priority === 'critical' || priority === 'high') return true
  if (kind === 'push_test') return true
  if (source === 'approval' && kind === 'approval_pending') return true
  if (source === 'secret' && kind === 'secret_prompt') return true
  if (source === 'task' && (kind === 'task_assigned' || kind === 'task_due')) return true
  return source === 'memory' && kind === 'memory_offer_received'
}

async function hasWebPushSubscription(): Promise<boolean> {
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return false
  if (typeof window === 'undefined' || !('PushManager' in window)) return false
  try {
    const registration = await navigator.serviceWorker.ready
    return Boolean(await registration.pushManager.getSubscription())
  } catch {
    return false
  }
}

const recentForegroundNotifications = new Map<string, number>()
const foregroundDedupeMs = 5 * 60 * 1000

function alreadyNotified(messageID: string): boolean {
  const now = Date.now()
  for (const [id, at] of recentForegroundNotifications) {
    if (now - at > foregroundDedupeMs) recentForegroundNotifications.delete(id)
  }
  if (recentForegroundNotifications.has(messageID)) return true
  recentForegroundNotifications.set(messageID, now)
  return false
}

// fireUsefulSignalNotification is the foreground fallback for browsers whose
// notification permission survived but whose Web Push subscription did not.
// The server-side allowlist remains the primary PWA delivery policy.
export async function fireUsefulSignalNotification(evt: SignalLike): Promise<void> {
  if (typeof Notification === 'undefined' || Notification.permission !== 'granted') return
  if (!isForegroundNotificationEligible(evt)) return
  if (await hasWebPushSubscription()) return
  if (alreadyNotified(evt.message_id)) return
  try {
    const url = destinationForSignal(evt)
    const n = new Notification(evt.title || 'MCPlexer', {
      body: evt.body || '',
      tag: `signal:${evt.message_id}`,
      icon: '/icon-192.png',
    })
    n.onclick = () => {
      window.focus()
      window.location.assign(url)
      n.close()
    }
  } catch {
    // OS rejected the notification — fine, the in-app tray still shows it.
  }
}
