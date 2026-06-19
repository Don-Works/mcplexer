// MilestoneSection — the horizontal scroll-row of MilestoneTile cards
// that appears above the workspace groups on TasksListPage when at
// least one milestone exists in the current workspace filter.
//
// Renders nothing when the list is empty so unfocused-workspaces
// (or workspaces with no milestones yet) don't get a useless header.

import { Flag } from 'lucide-react'

import type { MilestoneBurndown } from '@/api/tasks'

import { MilestoneTile } from './MilestoneTile'

interface MilestoneSectionProps {
  milestones: MilestoneBurndown[] | null
}

export function MilestoneSection({ milestones }: MilestoneSectionProps) {
  if (!milestones || milestones.length === 0) return null
  return (
    <section className="border border-border bg-card/30">
      <header className="flex items-center justify-between border-b border-border bg-card px-3 py-2">
        <div className="flex items-center gap-2">
          <Flag className="h-3.5 w-3.5 text-cyan-400" />
          <span className="text-sm font-semibold">Milestones</span>
          <span className="font-mono text-[10px] uppercase tracking-wider text-muted-foreground">
            {milestones.length}
          </span>
        </div>
        <span className="text-[10px] text-muted-foreground">
          click a tile to focus its children
        </span>
      </header>
      <div className="overflow-x-auto">
        <div className="flex gap-3 p-3">
          {milestones.map((m) => (
            <MilestoneTile key={m.task.id} milestone={m} />
          ))}
        </div>
      </div>
    </section>
  )
}
