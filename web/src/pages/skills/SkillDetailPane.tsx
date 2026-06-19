import { useCallback } from 'react'
import { Card, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { FileText, Folder, Globe, History, Loader2, Package, Sparkles, Tag, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { useApi } from '@/hooks/use-api'
import {
  getSkillRegistryEntry,
  setSkillRegistryTag,
  type SkillRegistryEntry,
} from '@/api/client'
import { Markdown, stripFrontmatter } from '@/lib/markdown'
import { SkillStatsTile } from './SkillStatsTile'

// Detail — metadata strip + rendered markdown body.

interface Props {
  name: string | null
  onTagSet: () => void
  onVersions?: (name: string) => void
  onDelete?: (entry: SkillRegistryEntry) => void
}

export function SkillDetailPane({ name, onTagSet, onVersions, onDelete }: Props) {
  const fetcher = useCallback(() => {
    if (!name) return Promise.resolve(null as unknown as SkillRegistryEntry)
    return getSkillRegistryEntry(name)
  }, [name])
  const { data, loading, error, refetch } = useApi(fetcher)

  async function handleSetStable() {
    if (!data) return
    try {
      await setSkillRegistryTag(data.name, '@stable', data.version)
      toast.success(`@stable now points to ${data.name}@${data.version}`)
      onTagSet()
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Set tag failed')
    }
  }

  if (!name) {
    return (
      <Card>
        <CardContent className="grid min-h-[400px] place-items-center px-6 py-10 text-center">
          <div className="space-y-2">
            <Sparkles className="mx-auto h-5 w-5 text-muted-foreground/40" />
            <p className="text-sm text-muted-foreground">
              Pick a skill to read it. Or ask the registry above.
            </p>
          </div>
        </CardContent>
      </Card>
    )
  }
  if (loading && !data)
    return (
      <Card>
        <CardContent className="flex items-center gap-2 px-4 py-6 text-sm text-muted-foreground">
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
          Loading…
        </CardContent>
      </Card>
    )
  if (error)
    return (
      <Card>
        <CardContent className="px-4 py-6 text-sm text-destructive">{error}</CardContent>
      </Card>
    )
  if (!data) return null

  const stripped = stripFrontmatter(data.body)
  const sourceLabel = sourceTypeLabel(data)

  return (
    <Card data-testid="skill-detail" className="overflow-hidden py-0">
      <CardContent className="p-0">
        <div className="border-b border-border px-5 py-4">
          <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-1.5">
                <Badge variant="outline" tone="mono">v{data.version}</Badge>
                <Badge variant="outline" tone={data.workspace_id ? 'info' : 'muted'} title={data.workspace_id ? `Workspace ${data.workspace_id}` : 'Global'}>
                  {data.workspace_id ? <Folder className="h-3 w-3" /> : <Globe className="h-3 w-3" />}
                  {data.workspace_id ? 'workspace' : 'global'}
                </Badge>
                {sourceLabel && (
                  <Badge variant="outline" tone={data.bundle_sha256 ? 'success' : 'muted'} title={data.source_path || undefined}>
                    <Package className="h-3 w-3" />
                    {sourceLabel}
                  </Badge>
                )}
              </div>
              <h2 className="mt-3 break-words text-xl font-semibold tracking-tight text-foreground">
                {data.name}
              </h2>
              {data.description && (
                <p className="mt-2 line-clamp-3 max-w-3xl text-sm leading-relaxed text-muted-foreground">
                  {data.description}
                </p>
              )}
            </div>
            <div className="flex shrink-0 flex-wrap items-center gap-2">
              {onVersions && (
                <Button variant="outline" size="sm" onClick={() => onVersions(data.name)}>
                  <History className="h-3.5 w-3.5" />
                  Versions
                </Button>
              )}
              <Button variant="outline" size="sm" onClick={handleSetStable}>
                <Tag className="h-3.5 w-3.5" />
                Pin @stable
              </Button>
              {onDelete && (
                <Button
                  variant="ghost"
                  size="sm"
                  className="text-destructive hover:text-destructive"
                  onClick={() => onDelete(data)}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                  Delete
                </Button>
              )}
            </div>
          </div>

          <div className="mt-4 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground sm:gap-x-4">
            {data.author && <span>Author: <span className="text-foreground/80">{data.author}</span></span>}
            <span>
              Published:{' '}
              <span className="text-foreground/80">
                {new Date(data.published_at).toLocaleDateString(undefined, {
                  year: 'numeric',
                  month: 'short',
                  day: 'numeric',
                })}
              </span>
            </span>
            {data.content_hash && (
              <span>
                Hash: <code className="text-foreground/80">{data.content_hash.slice(0, 8)}</code>
              </span>
            )}
            {data.tags && data.tags.length > 0 && (
              <span className="flex flex-wrap items-center gap-1">
                Tags:
                {data.tags.map((t) => (
                  <span key={t} className="border border-border px-1.5 py-0.5 text-[10px] text-muted-foreground">
                    {t}
                  </span>
                ))}
              </span>
            )}
          </div>
        </div>

        {data.source_path && (
          <div className="border-b border-border/40 bg-muted/15 px-5 py-2 text-[11px] text-muted-foreground">
            <span className="text-muted-foreground/60">Source path: </span>
            <code className="break-all text-foreground/80">{data.source_path}</code>
          </div>
        )}

        <div className="border-t border-border/60 px-5 py-3">
          <div className="flex items-center gap-2 text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
            <FileText className="h-3.5 w-3.5" />
            Instructions
          </div>
        </div>
        <div className="max-w-[82ch] px-6 pb-7">
          {stripped ? (
            <Markdown source={stripped} />
          ) : (
            <p className="py-6 text-sm text-muted-foreground">No SKILL.md body is available for this version.</p>
          )}
        </div>
        <SkillStatsTile name={data.name} embedded />
      </CardContent>
    </Card>
  )
}

function sourceTypeLabel(data: SkillRegistryEntry): string {
  if (data.bundle_sha256) return 'bundle'
  if (data.source_type === 'path') return 'path'
  if (data.source_type === 'inline') return 'inline'
  if (data.source_type === 'git') return 'git'
  return ''
}
