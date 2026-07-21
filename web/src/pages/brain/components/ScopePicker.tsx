import { useEffect, useMemo } from 'react'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { ChevronRight } from 'lucide-react'
import type { BrainClientNode, BrainWorkspaceNode } from '@/api/brainBrowser'

// ScopePicker is the spine of the Brain shell (DESIGN §2): a quiet header
// strip rendering `client ▸ workspace ▸ [ central | repo ]`. Picking a client
// filters the workspace dropdown to its children. The source toggle is the
// per-workspace federated source (central tree vs repo:.mcplexer/). It is a
// header strip, never a persistent command bar.
interface Props {
  clients: BrainClientNode[]
  workspaces: BrainWorkspaceNode[]
  client: string | null
  workspace: string | null
  source: 'central' | 'repo' | null
  onClient: (client: string | null) => void
  onWorkspace: (ws: string) => void
  onSource: (source: 'central' | 'repo') => void
}

export function ScopePicker({
  clients,
  workspaces,
  client,
  workspace,
  source,
  onClient,
  onWorkspace,
  onSource,
}: Props) {
  // Workspaces shown in the dropdown are filtered to the selected client's
  // children; with no client (flat install or "all") every workspace shows.
  const visibleWs = useMemo(
    () => (client ? workspaces.filter((w) => w.parent_id === client) : workspaces),
    [client, workspaces],
  )

  // The active workspace node drives the source toggle's available state — a
  // workspace whose canonical brain is repo-only cannot be flipped to central.
  const activeWsNode = useMemo(
    () => workspaces.find((w) => w.id === workspace) ?? null,
    [workspaces, workspace],
  )

  // Keep the workspace selection valid: if the chosen client no longer
  // contains the active workspace, fall to the first child. Effect, never a
  // render-time setState (the prior anti-pattern this build resolves).
  useEffect(() => {
    if (visibleWs.length === 0) return
    if (!workspace || !visibleWs.some((w) => w.id === workspace)) {
      onWorkspace(visibleWs[0].id)
    }
  }, [visibleWs, workspace, onWorkspace])

  const effectiveSource = source ?? activeWsNode?.source ?? 'central'

  return (
    <div className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2 text-sm">
      {clients.length > 0 && (
        <>
          <span className="text-xs text-muted-foreground">client</span>
          <Select
            value={client ?? '__all__'}
            onValueChange={(v) => onClient(v === '__all__' ? null : v)}
          >
            <SelectTrigger className="h-8 w-[180px] rounded-none">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__all__">all clients</SelectItem>
              {clients.map((c) => (
                <SelectItem key={c.id} value={c.id}>
                  {c.display_name}
                  <span className="ml-1 font-mono text-[10px] text-muted-foreground">
                    {c.workspace_count}
                  </span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <ChevronRight className="h-3.5 w-3.5 text-muted-foreground/50" aria-hidden />
        </>
      )}

      <span className="text-xs text-muted-foreground">workspace</span>
      <Select value={workspace ?? ''} onValueChange={onWorkspace}>
        <SelectTrigger className="h-8 w-[200px] rounded-none">
          <SelectValue placeholder="select workspace" />
        </SelectTrigger>
        <SelectContent>
          {visibleWs.map((w) => (
            <SelectItem key={w.id} value={w.id}>
              {w.display_name}
              <span className="ml-1 font-mono text-[10px] text-muted-foreground">
                {w.task_count}t · {w.memory_count}m
              </span>
            </SelectItem>
          ))}
        </SelectContent>
      </Select>

      <ChevronRight className="h-3.5 w-3.5 text-muted-foreground/50" aria-hidden />

      <span className="text-xs text-muted-foreground">source</span>
      <ToggleGroup
        type="single"
        value={effectiveSource}
        onValueChange={(v) => v && onSource(v as 'central' | 'repo')}
        variant="outline"
      >
        <ToggleGroupItem value="central" className="h-8 rounded-none font-mono text-xs">
          central
        </ToggleGroupItem>
        <ToggleGroupItem value="repo" className="h-8 rounded-none font-mono text-xs">
          repo
        </ToggleGroupItem>
      </ToggleGroup>
    </div>
  )
}
