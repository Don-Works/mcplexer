import { useCallback } from 'react'
import { Link, Navigate, useLocation, useSearchParams } from 'react-router-dom'
import { cn } from '@/lib/utils'
import { AuthScopesPage } from './AuthScopesPage'
import { OAuthProvidersPage } from './OAuthProvidersPage'
import { DescriptionsPage } from '../DescriptionsPage'

// /config?tab=* redirects — keeps deep-links alive while the IA moves.
// Workspace configuration lands in the canonical query-backed console.
// credentials, oauth, descriptions stay here under /advanced.
const TAB_REDIRECTS: Record<string, string> = {
  servers: '/workspaces?view=servers&server_tab=installed',
  routes: '/workspaces?view=access&advanced=1',
  workspaces: '/workspaces?view=settings',
  credentials: '/advanced/credentials',
  oauth: '/advanced/oauth-providers',
  descriptions: '/advanced/descriptions',
}

function legacyRedirectTarget(rawTab: string, searchParams: URLSearchParams): string | null {
  const base = TAB_REDIRECTS[rawTab]
  if (!base) return null

  const next = new URLSearchParams(searchParams)
  next.delete('tab')
  next.delete('sub')

  if (rawTab === 'servers') {
    const server = next.get('server')
    if (server && !next.has('focus_server')) {
      next.set('focus_server', server)
    }
    next.delete('server')
  }

  const query = next.toString()
  const separator = base.includes('?') ? '&' : '?'
  return query ? `${base}${separator}${query}` : base
}

// Canonical Advanced sub-pages — each maps to a path segment under /advanced.
type AdvancedTab = 'credentials' | 'oauth-providers' | 'descriptions'

const TABS: { id: AdvancedTab; label: string }[] = [
  { id: 'credentials', label: 'Credentials' },
  { id: 'oauth-providers', label: 'OAuth Providers' },
  { id: 'descriptions', label: 'Descriptions' },
]

const DEFAULT_TAB: AdvancedTab = 'credentials'

// Derive the active tab from the current pathname:
//   /advanced/credentials → 'credentials'
//   /advanced/oauth-providers → 'oauth-providers'
//   /advanced → DEFAULT_TAB
function tabFromPathname(pathname: string): AdvancedTab {
  const seg = pathname.split('/').filter(Boolean).pop() ?? ''
  const match = TABS.find((t) => t.id === seg)
  return match ? match.id : DEFAULT_TAB
}

export function ConfigPage() {
  const [searchParams] = useSearchParams()
  const rawTab = searchParams.get('tab')
  const location = useLocation()

  // Legacy /config?tab=* redirect — send to new canonical URL.
  const redirectTo = rawTab ? legacyRedirectTarget(rawTab, searchParams) : null
  if (redirectTo) {
    return <Navigate to={redirectTo} replace />
  }

  // Path-based tab selection for /advanced/* routes.
  const tab = tabFromPathname(location.pathname)

  return <AdvancedShell tab={tab} />
}

// AdvancedShell renders the tab bar + content. Tabs navigate to /advanced/<id>
// so the URL is canonical and bookmarkable.
function AdvancedShell({ tab }: { tab: AdvancedTab }) {
  const buildHref = useCallback((id: AdvancedTab) => `/advanced/${id}`, [])

  return (
    <div className="space-y-5">
      <div className="flex items-end justify-between border-b border-border">
        <nav
          role="tablist"
          aria-label="Advanced configuration sections"
          className="flex flex-nowrap items-center gap-x-1 overflow-x-auto pb-1"
        >
          {TABS.map((t) => {
            const active = t.id === tab
            return (
              <Link
                key={t.id}
                to={buildHref(t.id)}
                role="tab"
                aria-selected={active}
                data-testid={`config-tab-${t.id}`}
                className={cn(
                  'relative -mb-px px-3 py-2 text-[13px] font-medium tracking-wide transition-colors',
                  active
                    ? 'text-foreground'
                    : 'text-muted-foreground hover:text-foreground',
                )}
              >
                {t.label}
                <span
                  aria-hidden
                  className={cn(
                    'pointer-events-none absolute inset-x-2 bottom-0 h-[2px] transition-colors',
                    active ? 'bg-primary' : 'bg-transparent',
                  )}
                />
              </Link>
            )
          })}
        </nav>
      </div>

      <div>
        {tab === 'credentials' && <AuthScopesPage />}
        {tab === 'oauth-providers' && <OAuthProvidersPage />}
        {tab === 'descriptions' && <DescriptionsPage />}
      </div>
    </div>
  )
}
