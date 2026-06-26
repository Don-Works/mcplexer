import { useEffect, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { listApprovals } from '@/api/client'

const BASE_TITLE = 'MCPlexer'

// PAGE_TITLES maps the first significant URL segment to a human-friendly
// page name shown in the document title. Pinned-tab favicons + titles
// are the only way a user can tell several MCPlexer tabs apart in
// browser tab strips, so the names need to be short and unambiguous.
const PAGE_TITLES: Record<string, string> = {
  '': 'Dashboard',
  audit: 'Audit',
  approvals: 'Approvals',
  mesh: 'Mesh',
  skills: 'Skills',
  signals: 'Signal',
  config: 'Config',
  servers: 'Servers',
  workspaces: 'Workspaces',
  pairing: 'People & Devices',
  backups: 'Backups',
  settings: 'Settings',
  setup: 'Quick Setup',
  'harness-setup': 'AI Harnesses',
  install: 'AI Harnesses',
  'create-mcp': 'Custom MCP',
  'dry-run': 'Dry Run',
  descriptions: 'Descriptions',
}

function pageNameFromPath(pathname: string): string {
  // Strip leading slash, take first segment.
  const seg = pathname.replace(/^\/+/, '').split('/')[0]
  return PAGE_TITLES[seg] ?? PAGE_TITLES['']
}

// useDocumentTitle keeps the browser tab title in sync with two signals:
//   • the current route (so multiple MCPlexer tabs stay distinguishable)
//   • the count of pending approvals (so the user notices new work even
//     when the tab is in the background)
//
// Format: "(N) Page · MCPlexer"      — when there are pending approvals
//         "Page · MCPlexer"          — when the page isn't Dashboard
//         "MCPlexer"                 — on Dashboard with nothing pending
//
// The Dashboard tab stays bare because that's the "home" surface and
// the user doesn't need a redundant "Dashboard · MCPlexer" on the
// first tab. Approval counts override that and prefix anyway.
export function useDocumentTitle() {
  const location = useLocation()
  const [pendingCount, setPendingCount] = useState(0)

  // Poll for pending approvals on a 15s cadence. Notifications SSE only
  // carries mesh events today; if we ever wire an approval bus into
  // notify.Bus this becomes push-driven and the interval drops to a
  // safety-net long-poll.
  useEffect(() => {
    let cancelled = false

    async function tick() {
      if (cancelled) return
      try {
        const pending = await listApprovals('pending')
        if (cancelled) return
        setPendingCount(pending.length)
      } catch {
        // Daemon may be transiently unreachable — keep last known count.
      }
    }

    void tick()
    const id = setInterval(() => void tick(), 15_000)

    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [])

  useEffect(() => {
    const page = pageNameFromPath(location.pathname)
    const onDashboard = page === PAGE_TITLES['']
    const pageSuffix = onDashboard ? '' : `${page} · `
    const countPrefix = pendingCount > 0 ? `(${pendingCount}) ` : ''
    document.title = `${countPrefix}${pageSuffix}${BASE_TITLE}` || BASE_TITLE
    return () => {
      document.title = BASE_TITLE
    }
  }, [location.pathname, pendingCount])
}
