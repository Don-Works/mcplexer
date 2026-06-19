import { Button } from '@/components/ui/button'
import { Plus, Server } from 'lucide-react'
import { CATEGORY_LABELS, CATEGORY_ORDER } from '@/data/server-catalog'
import type { CatalogEntry } from '@/data/server-catalog'
import type { MergedServer } from './ServerCardGrid'

const AUTH_LABELS: Record<string, string> = {
  none: 'no auth',
  'api-key': 'api key',
  oauth: 'oauth',
  config: 'config',
}

interface Props {
  servers: MergedServer[]
  onAdd: (catalog: CatalogEntry) => void
}

// Browse-catalog density: 25-30 rows in the space of 6 cards. The catalog
// is a list to scan, not a board of tiles — every row is the same shape so
// the eye can sweep down by name + category + auth without re-parsing
// per-card chrome.
export function ServerCatalogList({ servers, onAdd }: Props) {
  if (servers.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-16 text-muted-foreground">
        <Server className="mb-2 h-8 w-8 text-muted-foreground/50" />
        <p className="text-sm">No servers match your search</p>
      </div>
    )
  }

  // Group by category, preserving canonical order; non-catalog entries are
  // routed under "custom" at the end.
  const grouped = new Map<string, MergedServer[]>()
  for (const s of servers) {
    const cat = s.catalog?.category ?? 'custom'
    const list = grouped.get(cat) ?? []
    list.push(s)
    grouped.set(cat, list)
  }
  const orderedKeys = [
    ...CATEGORY_ORDER.filter((c) => grouped.has(c)),
    ...(grouped.has('custom') ? ['custom'] : []),
  ]

  return (
    <div className="space-y-6">
      {orderedKeys.map((cat) => {
        const label = cat === 'custom' ? 'Custom Servers' : CATEGORY_LABELS[cat as keyof typeof CATEGORY_LABELS]
        const rows = grouped.get(cat) ?? []
        return (
          <section key={cat}>
            <div className="sticky top-0 z-10 -mx-2 mb-1 flex items-baseline justify-between bg-background px-2 pb-1.5 pt-2">
              <h2 className="text-[10px] font-semibold uppercase tracking-widest text-muted-foreground/70">
                {label}
              </h2>
              <span className="font-mono text-[10px] tabular-nums text-muted-foreground/50">
                {rows.length}
              </span>
            </div>
            <ul className="divide-y divide-border/40 border-y border-border/40">
              {rows.map((s) => (
                <CatalogRow key={s.catalog?.id ?? s.db?.id} server={s} onAdd={onAdd} />
              ))}
            </ul>
          </section>
        )
      })}
    </div>
  )
}

function CatalogRow({ server, onAdd }: { server: MergedServer; onAdd: (c: CatalogEntry) => void }) {
  const c = server.catalog
  const name = c?.name ?? server.db?.name ?? 'Unknown'
  const description = c?.description ?? ''
  const auth = c?.auth
  const isUserInstalled = !!server.db && !(server.db.source === 'default' && server.db.disabled)

  return (
    <li
      data-testid={`catalog-row-${c?.id ?? server.db?.id}`}
      className="group flex items-center gap-3 px-2 py-2 transition-colors hover:bg-muted/30"
    >
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline gap-2">
          <span className="truncate text-[13px] font-medium text-foreground">{name}</span>
          {auth && (
            <span className="shrink-0 font-mono text-[10px] uppercase tracking-wider text-muted-foreground/70">
              {AUTH_LABELS[auth] ?? auth}
            </span>
          )}
        </div>
        {description && (
          <p className="mt-0.5 line-clamp-1 text-[11px] text-muted-foreground group-hover:line-clamp-none">
            {description}
          </p>
        )}
      </div>
      <div className="shrink-0">
        {isUserInstalled ? (
          <span className="font-mono text-[10px] uppercase tracking-wider text-emerald-500/80">
            installed
          </span>
        ) : c ? (
          <Button
            size="sm"
            variant="outline"
            onClick={() => onAdd(c)}
            data-testid={`catalog-add-${c.id}`}
            className="h-7 px-2 text-[11px]"
          >
            <Plus className="mr-1 h-3 w-3" />
            Install
          </Button>
        ) : null}
      </div>
    </li>
  )
}
