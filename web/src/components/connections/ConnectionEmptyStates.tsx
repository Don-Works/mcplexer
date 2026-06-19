import type { ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import type { ConnectionFilter } from './connection-model'

export function EmptySetupState({
  icon,
  title,
  body,
  action,
}: {
  icon: ReactNode
  title: string
  body: string
  action: ReactNode
}) {
  return (
    <div className="min-w-0 overflow-hidden rounded-md border border-dashed border-border/60 bg-muted/20 px-4 py-12 text-center sm:px-6">
      <div className="mx-auto mb-3 flex h-12 w-12 items-center justify-center rounded-md border border-border/50 text-muted-foreground">
        {icon}
      </div>
      <p className="text-sm font-medium">{title}</p>
      <p className="mx-auto mt-1 max-w-full text-wrap break-words text-xs leading-relaxed text-muted-foreground sm:max-w-md">
        {body}
      </p>
      <div className="mt-4">{action}</div>
    </div>
  )
}

export function NoMatches({ query, filter }: { query: string; filter: ConnectionFilter }) {
  const detail = query
    ? 'No servers match your search. Try a different name or credential.'
    : `No servers match the ${filterLabel(filter)} filter in this workspace.`
  return (
    <div className="rounded-md border border-dashed border-border/60 bg-muted/20 px-6 py-10 text-center">
      <p className="text-sm font-medium">Nothing to show</p>
      <p className="mt-1 text-xs text-muted-foreground">{detail}</p>
    </div>
  )
}

export function ErrorBlock({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div className="flex items-center justify-between gap-3 rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
      <span>{message}</span>
      <Button size="sm" variant="ghost" onClick={onRetry}>Retry</Button>
    </div>
  )
}

export function ConnectionsSkeleton() {
  return (
    <div className="grid gap-4 xl:grid-cols-[18rem_1fr]">
      <div className="h-72 animate-pulse rounded-md bg-muted" />
      <div className="space-y-3">
        <div className="h-20 animate-pulse rounded-md bg-muted" />
        <div className="h-10 animate-pulse rounded-md bg-muted" />
        <div className="h-64 animate-pulse rounded-md bg-muted" />
      </div>
    </div>
  )
}

function filterLabel(filter: ConnectionFilter): string {
  switch (filter) {
    case 'all':
      return 'all'
    case 'connected':
      return 'connected'
    case 'needs-auth':
      return 'needs auth'
    case 'available':
      return 'available'
  }
}
