import { useEffect, useSyncExternalStore } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  listNotifications,
  markAllNotificationsRead,
  markNotificationsRead,
  unreadNotificationCount,
  type StoredNotification,
} from '@/api/notifications'
import { subscribeEvent } from '@/hooks/use-event-stream'
import { fireUsefulSignalNotification } from './use-os-notifications'
import {
  consumeFlash,
  countsByKind,
  getSnapshot,
  markAllReadLocal,
  markReadLocal,
  pushLive,
  selectFiltered,
  setFilter,
  setLoaded,
  setTrayOpen,
  setUnreadCount,
  subscribe,
  type NotificationFilter,
} from './store'

// useSignalStream wires three things into the global Signal store:
//  • initial backfill via /api/v1/notifications
//  • live SSE push via /api/v1/notifications/stream (was useNotifyStream)
//  • a low-frequency unread-count poll as a safety net for missed events
//
// Mounted once at App root. Returns nothing — this is a side-effect hook.
export function useSignalStream() {
  useEffect(() => {
    let cancelled = false

    // 1) Initial backfill.
    async function backfill() {
      try {
        const res = await listNotifications({ limit: 200 })
        if (cancelled) return
        setLoaded(res.notifications, res.unread_count)
      } catch {
        // Backfill failure is non-fatal — live SSE will populate.
        setLoaded([], 0)
      }
    }
    void backfill()

    // 2) Live push via the multiplexed event hub ('notifications' channel).
    //    The hub (hooks/use-event-stream.ts) owns the single shared
    //    EventSource + reconnect/backoff for all always-on streams; we just
    //    register a handler. The hub already JSON-parses the payload.
    const unsubLive = subscribeEvent('notifications', (data) => {
      if (cancelled) return
      const evt = data as {
        message_id?: string
        agent_name?: string
        role?: string
        kind?: string
        priority?: string
        title?: string
        body?: string
        tags?: string
        link?: string
        created_at?: string
        source?: string
      }
      if (!evt.message_id || !evt.title) return
      // SSE doesn't carry the DB id (the bus serializes the Event shape, not
      // the StoredEvent). We synthesize a transient id (-1 plus offset) so the
      // row renders; persisted lookups happen via message_id. The live pushed
      // row gets reconciled with the persisted row on the next backfill.
      const synthetic: StoredNotification = {
        id: -Math.floor(Date.now() / 1000),
        message_id: evt.message_id,
        source: evt.source ?? 'mesh',
        agent_name: evt.agent_name ?? '',
        role: evt.role ?? '',
        kind: evt.kind ?? '',
        priority: evt.priority ?? 'normal',
        title: evt.title,
        body: evt.body ?? '',
        tags: evt.tags ?? '',
        link: evt.link ?? '',
        created_at: evt.created_at ?? new Date().toISOString(),
        read_at: null,
      }
      pushLive(synthetic)
      // Foreground fallback only. A subscribed PWA receives Web Push and
      // this helper suppresses the duplicate native notification.
      void fireUsefulSignalNotification({
        message_id: synthetic.message_id,
        title: synthetic.title,
        body: synthetic.body,
        priority: synthetic.priority,
        kind: synthetic.kind,
        source: synthetic.source,
        link: synthetic.link,
      })
    })

    // 3) Low-frequency unread-count poll. Catches anything dropped during
    //    SSE reconnect / hibernation. 30s is conservative — the live
    //    channel is the primary signal.
    const pollId = setInterval(async () => {
      // Skip while the window is backgrounded — the live SSE channel keeps
      // us current when visible, and a hidden tab shouldn't burn one of the
      // browser's scarce HTTP/1.1 connection slots on a safety-net poll.
      if (document.visibilityState !== 'visible') return
      try {
        const res = await unreadNotificationCount()
        if (!cancelled) setUnreadCount(res.unread_count)
      } catch {
        // ignore
      }
    }, 30_000)

    return () => {
      cancelled = true
      unsubLive()
      clearInterval(pollId)
    }
  }, [])
}

// useSignal returns a snapshot + commit helpers. Components subscribe via
// useSyncExternalStore so re-renders only fire on actual state changes.
export function useSignal() {
  const snap = useSyncExternalStore(subscribe, getSnapshot, getSnapshot)
  const navigate = useNavigate()
  return {
    events: snap.events,
    filteredEvents: selectFiltered(snap),
    counts: countsByKind(snap),
    unreadCount: snap.unreadCount,
    loaded: snap.loaded,
    trayOpen: snap.trayOpen,
    filter: snap.filter,
    pendingFlash: snap.pendingFlash,
    openTray: () => setTrayOpen(true),
    closeTray: () => setTrayOpen(false),
    toggleTray: () => setTrayOpen(!snap.trayOpen),
    setFilter: (f: NotificationFilter) => setFilter(f),
    consumeFlash,
    markRead: async (id: number) => {
      // Skip persisted write for synthetic ids (live-pushed rows before
      // backfill reconciliation). They get a server write on next
      // backfill via message_id.
      if (id > 0) {
        try {
          await markNotificationsRead([id])
        } catch {
          // best-effort; local optimistic update stands
        }
      }
      markReadLocal([id])
    },
    markAllRead: async () => {
      try {
        await markAllNotificationsRead()
      } catch {
        // best-effort
      }
      markAllReadLocal()
    },
    openLink: (link: string | undefined) => {
      if (link) navigate(link)
    },
  }
}

// useSignalTray — global cmd+J / ctrl+J shortcut (sibling of cmd+K).
export function useSignalTray() {
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      const isToggle = (e.metaKey || e.ctrlKey) && (e.key === 'j' || e.key === 'J')
      if (!isToggle) return
      e.preventDefault()
      const cur = getSnapshot().trayOpen
      setTrayOpen(!cur)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])
}
