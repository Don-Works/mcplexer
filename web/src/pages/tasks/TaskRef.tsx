// TaskRef + LinkifiedText — the two primitives the rest of the
// dashboard imports to make task IDs appear as first-class clickable
// references wherever they're mentioned (mesh content, audit rows,
// signal payloads, worker run output, task notes themselves).

import { Link } from 'react-router-dom'
import { ListTodo } from 'lucide-react'
import { CopyButton } from '@/components/ui/copy-button'
import { cn } from '@/lib/utils'
import { linkifyTaskRefs, shortTaskId } from './task-utils'

// TaskRef — a chip that links to /tasks/:id. Renders the short id in
// mono with the task icon. Used in row affordances + composition
// lists + autolinker substitution.
export function TaskRef({
  id,
  workspaceId,
  variant = 'chip',
  showCopy,
  title,
  className,
}: {
  id: string
  workspaceId?: string
  variant?: 'chip' | 'inline' | 'plain'
  showCopy?: boolean
  title?: string
  className?: string
}) {
  if (!id) return null
  const href = workspaceId
    ? `/tasks/${encodeURIComponent(id)}?workspace=${encodeURIComponent(workspaceId)}`
    : `/tasks?focus=${encodeURIComponent(id)}`

  if (variant === 'plain') {
    return (
      <Link
        to={href}
        title={title ?? `Open task ${id}`}
        className={cn('font-mono text-[11px] text-primary hover:underline', className)}
      >
        {shortTaskId(id)}
      </Link>
    )
  }

  if (variant === 'inline') {
    return (
      <Link
        to={href}
        title={title ?? `Open task ${id}`}
        className={cn(
          'inline-flex items-baseline gap-1 px-1 font-mono text-[11px] text-primary hover:underline',
          className,
        )}
        onClick={(e) => e.stopPropagation()}
      >
        <ListTodo className="h-3 w-3 self-center" />
        {shortTaskId(id)}
      </Link>
    )
  }

  return (
    <span className={cn('inline-flex items-center gap-1', className)}>
      <Link
        to={href}
        title={title ?? `Open task ${id}`}
        className="inline-flex items-center gap-1.5 border border-border bg-muted/40 px-2 py-0.5 font-mono text-[11px] uppercase tracking-wider text-muted-foreground hover:border-primary/60 hover:text-foreground"
      >
        <ListTodo className="h-3 w-3" />
        {shortTaskId(id)}
      </Link>
      {showCopy ? <CopyButton value={id} className="h-5 w-5" /> : null}
    </span>
  )
}

// LinkifiedText — render free text with task:<id> tokens autolinked.
// Whitespace is preserved (whitespace-pre-wrap up to caller). Pure
// component; safe to use anywhere a string is rendered.
export function LinkifiedText({
  text,
  workspaceId,
  className,
}: {
  text: string
  workspaceId?: string
  className?: string
}) {
  const nodes = linkifyTaskRefs(text, { workspaceId })
  return <span className={className}>{nodes}</span>
}
