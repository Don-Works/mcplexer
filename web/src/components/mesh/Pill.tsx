import type { ComponentType, MouseEvent, ReactNode } from 'react'
import { cn } from '@/lib/utils'

// Pill is the shared "small categorical label" primitive — workspace
// badges, origin badges, and message tag chips are all instances. Before
// this lived as three near-identical span/button implementations that
// drifted in radius, padding, and max-width; the critique flagged that
// as a P1 consistency issue.
//
// Variants:
//   tone="muted"     — default, neutral container fill
//   tone="brand"     — active/selected state, primary tint
//   tone="local"     — emerald (agent on this machine)
//   tone="peer"      — slate-tinted (agent on a paired peer)
//   tone="workspace" — sky/info, matches the Memory page's ScopeBadge so
//                      a workspace-scoped item reads the same color
//                      across Mesh and Memory surfaces.
//
// Pass `onClick` to make it interactive (renders as <button>, supports
// keyboard focus, shows the toggleable border on hover); omit it for a
// static label.

export type PillTone = 'muted' | 'brand' | 'local' | 'peer' | 'workspace'

interface BaseProps {
  icon?: ComponentType<{ className?: string }>
  label: ReactNode
  title?: string
  tone?: PillTone
  // active = pressed state for toggle-style pills (workspace + tag filters).
  active?: boolean
  // testId is forwarded as data-testid; callers pass a stable hook.
  testId?: string
  // maxLabelCh caps the label width in characters so long workspace names
  // and peer hashes don't push the rest of the row offscreen. Defaults to
  // 16ch which clears most repo/branch names.
  maxLabelCh?: number
}

interface InteractiveProps extends BaseProps {
  onClick: (e: MouseEvent<HTMLButtonElement>) => void
  ariaLabel?: string
}

type Props = BaseProps | InteractiveProps

function isInteractive(p: Props): p is InteractiveProps {
  return 'onClick' in p && typeof p.onClick === 'function'
}

const toneClasses: Record<PillTone, { static: string; interactive: string; active: string }> = {
  muted: {
    static: 'border-border bg-muted text-muted-foreground',
    interactive:
      'border-border bg-muted/60 text-muted-foreground hover:border-primary/40 hover:bg-primary/10 hover:text-foreground',
    active: 'border-primary/40 bg-primary/15 text-foreground',
  },
  brand: {
    static: 'border-primary/30 bg-primary/10 text-foreground',
    interactive:
      'border-primary/30 bg-primary/10 text-foreground hover:border-primary/50 hover:bg-primary/15',
    active: 'border-primary/50 bg-primary/20 text-foreground',
  },
  local: {
    static: 'border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300',
    interactive:
      'border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300 hover:bg-emerald-500/15',
    active: 'border-emerald-500/50 bg-emerald-500/20 text-emerald-700 dark:text-emerald-200',
  },
  peer: {
    static: 'border-border bg-muted text-muted-foreground',
    interactive:
      'border-border bg-muted text-muted-foreground hover:border-primary/40 hover:bg-primary/10 hover:text-foreground',
    active: 'border-primary/40 bg-primary/15 text-foreground',
  },
  workspace: {
    static: 'border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300',
    interactive:
      'border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300 hover:border-sky-500/50 hover:bg-sky-500/15',
    active: 'border-sky-500/60 bg-sky-500/20 text-sky-800 dark:text-sky-200',
  },
}

export function Pill(props: Props): React.ReactElement {
  const Icon = props.icon
  const tone = props.tone ?? 'muted'
  const maxCh = props.maxLabelCh ?? 16
  const styles = toneClasses[tone]
  const interactive = isInteractive(props)

  const body = (
    <>
      {Icon ? <Icon className="h-2.5 w-2.5 shrink-0" aria-hidden /> : null}
      <span
        className="truncate"
        style={{ maxWidth: `${maxCh}ch` }}
      >
        {props.label}
      </span>
    </>
  )

  const baseClass = cn(
    'inline-flex items-center gap-1 rounded-sm border px-1.5 py-px text-[11px] font-medium leading-4 transition-colors',
  )

  if (interactive) {
    return (
      <button
        type="button"
        onClick={props.onClick}
        title={props.title}
        aria-label={props.ariaLabel ?? props.title}
        aria-pressed={props.active}
        data-testid={props.testId}
        className={cn(baseClass, props.active ? styles.active : styles.interactive, 'cursor-pointer')}
      >
        {body}
      </button>
    )
  }

  return (
    <span
      title={props.title}
      data-testid={props.testId}
      className={cn(baseClass, props.active ? styles.active : styles.static)}
    >
      {body}
    </span>
  )
}
