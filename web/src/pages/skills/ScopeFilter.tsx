import { Check, ChevronDown, Folder, Globe, ListTree } from 'lucide-react'
import { cn } from '@/lib/utils'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import type { SkillScopeFilter } from '@/api/client'
import type { Workspace } from '@/api/types'

interface Props {
  value: SkillScopeFilter
  onChange: (v: SkillScopeFilter) => void
  workspaces: Workspace[]
}

// ScopeFilter collapses the old pill row into a single dropdown trigger.
// The popover groups synthetic scopes (All / Shared) above the user's
// workspaces with a labelled separator — that disambiguates the case
// where a user has literally named a workspace "Global" sitting next to
// the Shared synthetic.
export function ScopeFilter({ value, onChange, workspaces }: Props) {
  const activeLabel = describeScope(value, workspaces)
  const activeIcon = iconForScope(value)
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        className={cn(
          'inline-flex items-center gap-2 border border-border bg-transparent px-2.5 py-1 text-[12px] text-foreground/90 transition-colors',
          'hover:border-muted-foreground hover:bg-accent/20 data-[state=open]:border-primary/60 data-[state=open]:bg-accent/30',
        )}
        data-testid="scope-trigger"
      >
        <span className="text-muted-foreground/70 uppercase text-[10px] tracking-wider">
          Workspace
        </span>
        <span className="flex items-center gap-1.5 text-foreground">
          {activeIcon}
          {activeLabel}
        </span>
        <ChevronDown className="ml-0.5 h-3 w-3 text-muted-foreground/60" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-[14rem]">
        <DropdownMenuLabel className="text-[10px] uppercase tracking-wider text-muted-foreground/70">
          Synthetic
        </DropdownMenuLabel>
        <ScopeItem
          active={value.mode === 'all'}
          icon={<ListTree className="h-3.5 w-3.5" />}
          label="All scopes"
          hint="Every skill visible to admin"
          onSelect={() => onChange({ mode: 'all' })}
          testid="scope-all"
        />
        <ScopeItem
          active={value.mode === 'global'}
          icon={<Globe className="h-3.5 w-3.5" />}
          label="Shared"
          hint="Workspace-independent"
          onSelect={() => onChange({ mode: 'global' })}
          testid="scope-global"
        />
        {workspaces.length > 0 && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuLabel className="text-[10px] uppercase tracking-wider text-muted-foreground/70">
              Workspaces
            </DropdownMenuLabel>
            {workspaces.map((ws) => (
              <ScopeItem
                key={ws.id}
                active={value.mode === 'workspace' && value.workspaceId === ws.id}
                icon={<Folder className="h-3.5 w-3.5" />}
                label={ws.name}
                onSelect={() => onChange({ mode: 'workspace', workspaceId: ws.id })}
                testid={`scope-ws-${ws.id.slice(0, 8)}`}
              />
            ))}
          </>
        )}
        <DropdownMenuSeparator />
        <p className="px-2 py-1.5 text-[10px] leading-relaxed text-muted-foreground/70">
          Agents publish with{' '}
          <code className="text-foreground/80">scope: workspace</code> to pin a skill, else
          it lands shared.
        </p>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function ScopeItem({
  active,
  icon,
  label,
  hint,
  onSelect,
  testid,
}: {
  active: boolean
  icon: React.ReactNode
  label: string
  hint?: string
  onSelect: () => void
  testid: string
}) {
  return (
    <DropdownMenuItem
      onSelect={onSelect}
      data-testid={testid}
      className={cn(
        'flex items-center gap-2 text-[12px]',
        active && 'bg-accent/40 text-foreground',
      )}
    >
      <span className="text-muted-foreground/80">{icon}</span>
      <span className="flex-1 truncate">{label}</span>
      {hint && <span className="text-[10px] text-muted-foreground/60">{hint}</span>}
      {active && <Check className="h-3 w-3 text-primary" />}
    </DropdownMenuItem>
  )
}

function describeScope(value: SkillScopeFilter, workspaces: Workspace[]): string {
  if (value.mode === 'all') return 'All scopes'
  if (value.mode === 'global') return 'Shared'
  const ws = workspaces.find((w) => w.id === value.workspaceId)
  return ws?.name ?? 'Workspace'
}

function iconForScope(value: SkillScopeFilter): React.ReactNode {
  if (value.mode === 'all') return <ListTree className="h-3.5 w-3.5 text-muted-foreground/70" />
  if (value.mode === 'global') return <Globe className="h-3.5 w-3.5 text-muted-foreground/70" />
  return <Folder className="h-3.5 w-3.5 text-muted-foreground/70" />
}
