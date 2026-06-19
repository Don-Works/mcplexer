import { useEffect } from 'react'

// useOsNotifications wires browser-native OS notifications for approval +
// mesh events. PWA replacement for the OS notifications the old Electron
// shell fired via electron's Notification module.
//
// Architecture: this module no longer opens its own EventSource streams
// (it used to subscribe to /approvals/stream + /notifications/stream a
// SECOND time, on top of the always-on subscriptions from
// `useApprovalStream` and `useSignalStream`). Doubled SSE subscriptions
// blew past Chrome's 6-per-origin HTTP/1.1 cap and stalled every later
// fetch — the "click-Dashboard-and-it-hangs" bug. Instead, this module
// exposes two `fire*` functions that the canonical SSE hooks call.
//
// Mounted once at App root. Side-effect only.

// granted is a module-level flag so the fire functions (called from
// other hooks) can cheap-check it without re-querying Notification.permission
// on every event.
let granted = false

export function useOsNotifications() {
  useEffect(() => {
    if (typeof window === 'undefined') return
    if (!('Notification' in window)) return

    granted = Notification.permission === 'granted'
    if (Notification.permission === 'default') {
      const t = setTimeout(() => {
        Notification.requestPermission()
          .then((p) => {
            granted = p === 'granted'
          })
          .catch(() => {
            // browsers throw if called outside a user gesture in some configs
          })
      }, 2_000)
      return () => clearTimeout(t)
    }
  }, [])
}

interface ApprovalLike {
  id: string
  tool_name: string
}

// fireApprovalPending posts an OS notification for a freshly-pending tool
// approval. Called by useApprovalStream when an event of type "pending"
// arrives. Silently no-ops if permission isn't granted.
export function fireApprovalPending(approval: ApprovalLike): void {
  if (!granted) return
  try {
    const n = new Notification('MCPlexer: Approval Required', {
      body: approval.tool_name,
      // tag dedupes — a second event for the same approval id replaces
      // the previous banner instead of stacking.
      tag: `approval:${approval.id}`,
      icon: '/icon-192.png',
    })
    n.onclick = () => {
      window.focus()
      if (window.location.pathname !== '/approvals') {
        window.location.assign('/approvals')
      }
      n.close()
    }
  } catch {
    // OS rejected the notification — fine, the in-app tray still shows it.
  }
}

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
// publisher set an explicit Link on the signal, we honour it verbatim —
// that's how approvals get to deep into /approvals?selected=<id> and how
// mesh queue events land on /mesh?queue=1.
function destinationForSignal(evt: SignalLike): string {
  if (evt.link && evt.link.trim() !== '') return evt.link
  const kind = (evt.kind || evt.source || '').toLowerCase()
  if (kind.includes('approval')) return '/approvals'
  if (kind.includes('secret')) return '/approvals'
  if (kind.startsWith('memory')) return '/memory'
  if (kind === 'mesh' || kind.includes('mesh')) {
    return evt.message_id ? `/mesh?msg=${encodeURIComponent(evt.message_id)}` : '/mesh'
  }
  return '/signals'
}

// fireSignalHighOrCritical posts an OS notification for high/critical
// signal events. Called by useSignalStream when a matching SSE message
// arrives. Priority gate mirrors electron/src/notifications.ts: OS
// banner earns its place only for high/critical; normal/low live in the
// in-app Signal tray.
export function fireSignalHighOrCritical(evt: SignalLike): void {
  if (!granted) return
  if (evt.priority !== 'critical' && evt.priority !== 'high') return
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
