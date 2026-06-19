import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  AlertTriangle,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  FolderInput,
  Loader2,
  RefreshCw,
} from 'lucide-react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import {
  importLocalSkill,
  listLocalUnpublishedSkills,
  type LocalSkill,
  type LocalSkillStatus,
  type LocalUnpublishedResponse,
} from '@/api/client'

// LocalSkillsMigrationTile renders the W5 "Local skills not in registry"
// surface at the top of the Skills page. Lists every SKILL.md directory
// under ~/.claude/skills/ with checkboxes; selected rows can be imported
// in one click. Empty state reassures the user nothing is missing.
//
// On mount it calls GET /api/v1/skills/local-unpublished and renders a
// loading skeleton until the response arrives. Errors collapse to an
// inline banner so they never block the rest of the page.
interface Props {
  onImported?: () => void
}

export function LocalSkillsMigrationTile({ onImported }: Props) {
  const [data, setData] = useState<LocalUnpublishedResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [importing, setImporting] = useState(false)
  const [expanded, setExpanded] = useState(false)

  const fetchAll = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const resp = await listLocalUnpublishedSkills()
      setData(resp)
      // Auto-select the rows we can actually import (status=new).
      const nextSel = new Set<string>()
      for (const s of resp.skills) {
        if (s.status === 'new') nextSel.add(s.path)
      }
      setSelected(nextSel)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to discover local skills')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void fetchAll()
  }, [fetchAll])

  const importable = useMemo(
    () => data?.skills.filter((s) => s.status === 'new' || s.status === 'version-conflict') ?? [],
    [data],
  )
  const counts = useMemo(() => {
    const tally: Record<LocalSkillStatus, number> = {
      new: 0, duplicate: 0, 'version-conflict': 0, unparseable: 0,
    }
    for (const s of data?.skills ?? []) tally[s.status] = (tally[s.status] ?? 0) + 1
    return tally
  }, [data])

  // Nothing to do — keep the tile out of the page entirely when the
  // walk found no SKILL.md dirs at all. When everything is duplicate we
  // show a tiny "All local skills are in the registry." confirmation so
  // the user knows the check ran.
  if (loading && !data) return <SkeletonTile />
  if (error) return <ErrorTile error={error} onRetry={fetchAll} />
  if (!data || data.skills.length === 0) return null
  if (importable.length === 0 && counts.unparseable === 0) {
    return <AllImportedTile path={data.path} />
  }

  async function handleImportSelected() {
    if (selected.size === 0) return
    setImporting(true)
    let success = 0
    let failed = 0
    for (const path of selected) {
      const row = data?.skills.find((s) => s.path === path)
      if (!row) continue
      try {
        const res = await importLocalSkill({
          name: row.name || row.dir,
          source_dir: row.path,
          overwrite: row.status === 'version-conflict',
        })
        if (res.action === 'failed') {
          failed++
          toast.error(`${row.name}: ${res.error ?? 'import failed'}`)
        } else {
          success++
        }
      } catch (e) {
        failed++
        toast.error(`${row.name}: ${e instanceof Error ? e.message : 'request failed'}`)
      }
    }
    setImporting(false)
    if (success > 0) toast.success(`Imported ${success} skill${success === 1 ? '' : 's'}`)
    if (failed > 0 && success > 0) {
      toast.error(`Failed to import ${failed} skill${failed === 1 ? '' : 's'}`)
    }
    setSelected(new Set())
    await fetchAll()
    onImported?.()
  }

  function toggleRow(path: string, on: boolean) {
    setSelected((prev) => {
      const next = new Set(prev)
      if (on) next.add(path)
      else next.delete(path)
      return next
    })
  }
  function toggleAll(on: boolean) {
    if (!on) {
      setSelected(new Set())
      return
    }
    setSelected(new Set(importable.map((s) => s.path)))
  }

  return (
    <Card data-testid="local-skills-migration-tile" className="py-0">
      <CardHeader className="flex flex-row items-center justify-between gap-4 space-y-0 py-3">
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          className="flex min-w-0 flex-1 items-start gap-3 text-left"
          aria-expanded={expanded}
        >
          <span className="mt-0.5 text-muted-foreground">
            {expanded ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
          </span>
          <span className="min-w-0">
            <CardTitle className="flex items-center gap-2 text-base">
              <FolderInput className="h-4 w-4 text-primary" />
              Local skills not in registry
            </CardTitle>
            <span className="mt-1 block truncate text-sm text-muted-foreground">
              {importable.length} importable skill{importable.length === 1 ? '' : 's'} under{' '}
              <code className="text-[11px] text-foreground/80">{data.path}</code>
              {counts.unparseable > 0 ? `; ${counts.unparseable} need attention` : ''}
            </span>
          </span>
        </button>
        <div className="flex shrink-0 items-center gap-2">
          <Button
            size="sm"
            onClick={handleImportSelected}
            disabled={selected.size === 0 || importing}
            data-testid="import-selected"
          >
            {importing ? (
              <>
                <Loader2 className="mr-2 h-3.5 w-3.5 animate-spin" />
                Importing
              </>
            ) : (
              <>Import selected ({selected.size})</>
            )}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => void fetchAll()}
            disabled={loading || importing}
            aria-label="Refresh local skills"
          >
            <RefreshCw className={cn('h-3.5 w-3.5', loading && 'animate-spin')} />
          </Button>
        </div>
      </CardHeader>
      {expanded && <CardContent className="space-y-3 pb-4">
        <div className="flex items-center justify-between gap-3 border-b border-border/60 pb-2">
          <label className="flex items-center gap-2 text-sm text-muted-foreground">
            <Checkbox
              checked={selected.size > 0 && selected.size === importable.length}
              onCheckedChange={(v) => toggleAll(Boolean(v))}
              aria-label="Select all importable"
              disabled={importable.length === 0}
            />
            <span>Select all importable</span>
          </label>
          <p className="text-xs text-muted-foreground">
            Imported skills move to <code className="text-[11px] text-foreground/80">.migrated/</code>.
          </p>
        </div>
        <ul className="max-h-80 space-y-1.5 overflow-y-auto pr-1">
          {data.skills.map((s) => (
            <SkillRow
              key={s.path}
              skill={s}
              checked={selected.has(s.path)}
              onToggle={(v) => toggleRow(s.path, v)}
            />
          ))}
        </ul>
      </CardContent>}
    </Card>
  )
}

function SkillRow({
  skill,
  checked,
  onToggle,
}: {
  skill: LocalSkill
  checked: boolean
  onToggle: (on: boolean) => void
}) {
  const canImport = skill.status === 'new' || skill.status === 'version-conflict'
  return (
    <li
      className={cn(
        'flex items-center justify-between gap-3 rounded-sm border border-transparent px-2 py-1.5',
        canImport ? 'hover:border-border/60 hover:bg-muted/40' : 'opacity-70',
      )}
      data-testid="local-skill-row"
    >
      <label className="flex min-w-0 flex-1 items-center gap-3 cursor-pointer">
        <Checkbox
          checked={checked}
          onCheckedChange={(v) => onToggle(Boolean(v))}
          disabled={!canImport}
          aria-label={`Select ${skill.name || skill.dir}`}
        />
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-medium text-foreground">
            {skill.name || skill.dir}
          </div>
          <div className="truncate text-xs text-muted-foreground">
            {skill.description || skill.parse_error || skill.path}
          </div>
        </div>
      </label>
      <StatusBadge status={skill.status} version={skill.registry_version} />
    </li>
  )
}

function StatusBadge({
  status,
  version,
}: {
  status: LocalSkillStatus
  version?: number
}) {
  switch (status) {
    case 'new':
      return (
        <Badge variant="default" className="text-[10px] uppercase tracking-wide">
          new
        </Badge>
      )
    case 'duplicate':
      return (
        <Badge variant="secondary" className="text-[10px] uppercase tracking-wide">
          in registry v{version}
        </Badge>
      )
    case 'version-conflict':
      return (
        <Badge variant="outline" className="text-[10px] uppercase tracking-wide text-amber-600">
          conflict v{version}
        </Badge>
      )
    case 'unparseable':
      return (
        <Badge variant="destructive" className="text-[10px] uppercase tracking-wide">
          unparseable
        </Badge>
      )
  }
}

function SkeletonTile() {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          Discovering local skills…
        </CardTitle>
      </CardHeader>
    </Card>
  )
}

function ErrorTile({ error, onRetry }: { error: string; onRetry: () => void }) {
  return (
    <Card className="border-destructive/40 bg-destructive/5">
      <CardHeader className="flex flex-row items-start justify-between gap-4 space-y-0">
        <div>
          <CardTitle className="flex items-center gap-2 text-base text-destructive">
            <AlertTriangle className="h-4 w-4" />
            Could not list local skills
          </CardTitle>
          <p className="mt-1 text-sm text-muted-foreground">{error}</p>
        </div>
        <Button variant="outline" size="sm" onClick={onRetry}>
          <RefreshCw className="mr-2 h-3.5 w-3.5" />
          Retry
        </Button>
      </CardHeader>
    </Card>
  )
}

function AllImportedTile({ path }: { path: string }) {
  return (
    <Card className="border-emerald-500/30 bg-emerald-500/5">
      <CardHeader className="flex flex-row items-center gap-3 space-y-0 py-3">
        <CheckCircle2 className="h-4 w-4 text-emerald-600" />
        <p className="text-sm text-muted-foreground">
          All local skills under{' '}
          <code className="text-[11px] text-foreground/80">{path}</code> are in the registry.
        </p>
      </CardHeader>
    </Card>
  )
}
