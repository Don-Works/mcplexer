import { useCallback, useState } from 'react'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Loader2 } from 'lucide-react'
import { toast } from 'sonner'
import { useApi } from '@/hooks/use-api'
import {
  getSkillRegistryVersionDiff,
  listSkillRegistryVersions,
  setSkillRegistryTag,
  type SkillRegistryEntry,
  type SkillVersionDiff,
} from '@/api/client'

// Versions — compact list, pin-stable inline, diff vs previous.

interface Props {
  name: string | null
  onOpenChange: (b: boolean) => void
  onTagSet: () => void
}

export function VersionsDialog({ name, onOpenChange, onTagSet }: Props) {
  const [diff, setDiff] = useState<SkillVersionDiff | null>(null)
  const [diffLoading, setDiffLoading] = useState(false)

  const fetcher = useCallback(() => {
    if (!name) return Promise.resolve([] as SkillRegistryEntry[])
    return listSkillRegistryVersions(name)
  }, [name])
  const { data, loading } = useApi(fetcher)

  async function pinStable(v: number) {
    if (!name) return
    try {
      await setSkillRegistryTag(name, '@stable', v)
      toast.success(`@stable → ${name}@${v}`)
      onTagSet()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Tag failed')
    }
  }

  async function showDiff(oldVersion: number, newVersion: number) {
    if (!name) return
    setDiffLoading(true)
    try {
      const result = await getSkillRegistryVersionDiff(name, oldVersion, newVersion)
      setDiff(result)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Diff failed')
      setDiff(null)
    } finally {
      setDiffLoading(false)
    }
  }

  function handleOpenChange(open: boolean) {
    if (!open) {
      setDiff(null)
    }
    onOpenChange(open)
  }

  return (
    <Dialog open={!!name} onOpenChange={handleOpenChange}>
      <DialogContent className="max-w-2xl sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{name} — version history</DialogTitle>
        </DialogHeader>
        {loading && (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
            Loading…
          </div>
        )}
        <ul className="max-h-[320px] divide-y divide-border/40 overflow-auto">
          {data?.map((v, idx) => {
            const prev = data[idx + 1]
            return (
              <li key={v.id} className="flex items-start gap-3 px-1 py-3">
                <span className="mt-1 shrink-0 text-[11px] uppercase tracking-wider text-muted-foreground">
                  v{v.version}
                </span>
                <div className="min-w-0 flex-1">
                  <p className="line-clamp-2 text-[13px] text-foreground/90">{v.description}</p>
                  <p className="mt-0.5 text-[11px] text-muted-foreground/70">
                    {new Date(v.published_at).toLocaleString()} · {v.author || 'anonymous'}
                    {v.parent_version != null && (
                      <> · parent v{v.parent_version}</>
                    )}
                  </p>
                </div>
                <div className="flex shrink-0 flex-col gap-1">
                  {prev && (
                    <button
                      type="button"
                      onClick={() => showDiff(prev.version, v.version)}
                      className="border border-transparent px-2 py-1 text-[11px] uppercase tracking-wider text-muted-foreground transition-colors hover:border-border hover:text-foreground"
                    >
                      Diff v{prev.version}
                    </button>
                  )}
                  <button
                    type="button"
                    onClick={() => pinStable(v.version)}
                    className="border border-transparent px-2 py-1 text-[11px] uppercase tracking-wider text-muted-foreground transition-colors hover:border-border hover:text-foreground"
                  >
                    Pin @stable
                  </button>
                </div>
              </li>
            )
          })}
        </ul>
        {(diffLoading || diff) && (
          <div className="space-y-2 border border-border/60 bg-muted/20 p-3">
            <p className="text-[11px] uppercase tracking-wider text-muted-foreground">
              {diff
                ? `Diff v${diff.old_version} → v${diff.new_version}`
                : 'Loading diff…'}
            </p>
            {diffLoading && (
              <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />
            )}
            {diff?.body_diff && (
              <pre className="max-h-48 overflow-auto whitespace-pre-wrap text-[11px] leading-relaxed text-foreground/85">
                {diff.body_diff}
              </pre>
            )}
            {diff && !diff.body_diff && !diff.frontmatter_diff && (
              <p className="text-[12px] text-muted-foreground">No textual changes.</p>
            )}
            {diff?.tree && diff.tree.length > 0 && (
              <ul className="space-y-0.5 text-[11px] text-muted-foreground">
                {diff.tree.map((entry) => (
                  <li key={`${entry.status}-${entry.path}`}>
                    {entry.status}
                    {entry.path ? `: ${entry.path}` : ''}
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
        <DialogFooter>
          <Button variant="ghost" onClick={() => handleOpenChange(false)}>
            Close
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
