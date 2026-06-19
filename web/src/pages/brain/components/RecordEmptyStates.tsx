import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Plus } from 'lucide-react'

// Per-kind empty states: each says, in plain language, what the thing is for
// and offers the gesture to make one. Three distinct bodies, never one
// templated icon+heading+text card. Copy uses plain hyphens (no em dashes, no
// emoji) per the design hard bans.

export function TasksEmpty({ onNew }: { onNew: () => void }) {
  return (
    <EmptyState
      testid="brain-tasks-empty"
      title="Nothing to do here yet."
      description={
        <>
          Tasks track work in this space. You and your agents add them, move them along, and check
          them off together.
        </>
      }
      action={<NewButton label="New task" onClick={onNew} />}
    />
  )
}

export function MemoriesEmpty({ onNew }: { onNew: () => void }) {
  return (
    <EmptyState
      testid="brain-memories-empty"
      title="No facts here yet."
      description={
        <>
          Facts are the things worth remembering: how something works, a decision and why you made
          it, a preference. Your agents recall them automatically, and the brain will quietly
          suggest some as you write.
        </>
      }
      action={<NewButton label="New fact" onClick={onNew} />}
    />
  )
}

export function NotesEmpty({ onNew }: { onNew: () => void }) {
  return (
    <EmptyState
      testid="brain-notes-empty"
      title="No notes yet."
      description={
        <>
          Notes are where you write things down: meeting notes, how-tos, anything you want to keep.
          Write freely. Your agents can read every word.
        </>
      }
      action={<NewButton label="New note" onClick={onNew} />}
    />
  )
}

function NewButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <Button size="sm" variant="outline" className="rounded-none" onClick={onClick}>
      <Plus className="mr-1.5 h-3.5 w-3.5" /> {label}
    </Button>
  )
}
