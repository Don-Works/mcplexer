import { useCallback, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { useApi } from '@/hooks/use-api'
import {
  backupDownloadURL,
  createBackup,
  deleteBackup,
  listBackups,
  restoreBackup,
} from '@/api/client'
import type { BackupManifest } from '@/api/client'
import { Archive, Download, Loader2, Plus, RotateCcw, Trash2 } from 'lucide-react'
import { toast } from 'sonner'

export function BackupsPage() {
  const fetcher = useCallback(() => listBackups(), [])
  const { data, loading, error, refetch } = useApi(fetcher)

  const [creating, setCreating] = useState(false)
  const [note, setNote] = useState('')
  const [restoreTarget, setRestoreTarget] = useState<BackupManifest | null>(null)
  const [restoring, setRestoring] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<BackupManifest | null>(null)

  async function handleCreate() {
    setCreating(true)
    try {
      const mf = await createBackup(note || undefined)
      setNote('')
      toast.success(`Backup created: ${mf.id}`)
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to create backup')
    } finally {
      setCreating(false)
    }
  }

  async function handleRestore() {
    if (!restoreTarget) return
    setRestoring(true)
    try {
      const res = await restoreBackup(restoreTarget.id)
      toast.success(
        `Restored from ${res.restored_from}. Pre-restore snapshot: ${res.pre_restore_snapshot_id}. Restart MCPlexer to apply.`,
        { duration: 12000 },
      )
      setRestoreTarget(null)
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Restore failed')
    } finally {
      setRestoring(false)
    }
  }

  async function handleDelete() {
    if (!deleteTarget) return
    try {
      await deleteBackup(deleteTarget.id)
      toast.success('Backup deleted')
      setDeleteTarget(null)
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Delete failed')
    }
  }

  return (
    <div className="space-y-5 max-w-4xl">
      <div>
        <h1 className="text-2xl font-bold">Backup &amp; Restore</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Snapshot the live config (SQLite DB + secrets + skills) before risky changes. Restore rolls everything back; an
          automatic pre-restore snapshot is taken so you can always undo. Agents can use{' '}
          <span className="font-mono text-xs">mcplexer__create_backup</span> /{' '}
          <span className="font-mono text-xs">restore_backup</span> for the same flow.
        </p>
      </div>

      <Card>
        <CardContent className="flex flex-col gap-3 p-4 sm:flex-row sm:items-end">
          <div className="flex-1 space-y-1">
            <label htmlFor="backup-note" className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
              Note (optional)
            </label>
            <Input
              id="backup-note"
              placeholder="e.g. before adding GitHub server"
              value={note}
              onChange={(e) => setNote(e.target.value)}
              data-testid="backup-note"
            />
          </div>
          <Button onClick={handleCreate} disabled={creating} data-testid="backup-create">
            {creating ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Plus className="mr-2 h-4 w-4" />}
            Create backup
          </Button>
        </CardContent>
      </Card>

      {loading && !data && (
        <div className="flex items-center gap-2 text-muted-foreground">
          <div className="h-2 w-2 rounded-full bg-primary/60" />
          Loading backups...
        </div>
      )}
      {error && <p className="text-destructive">Error: {error}</p>}

      {data && data.length === 0 && (
        <div className="flex flex-col items-center justify-center rounded-lg border border-dashed py-12 text-center">
          <Archive className="mb-2 h-8 w-8 text-muted-foreground/40" />
          <h3 className="text-sm font-semibold">No backups yet</h3>
          <p className="mt-1 max-w-sm text-sm text-muted-foreground">
            Create one before making config changes you might want to roll back.
          </p>
        </div>
      )}

      {data && data.length > 0 && (
        <div className="space-y-2">
          {data.map((mf) => (
            <BackupRow
              key={mf.id}
              mf={mf}
              onRestore={() => setRestoreTarget(mf)}
              onDelete={() => setDeleteTarget(mf)}
            />
          ))}
        </div>
      )}

      <ConfirmDialog
        open={!!restoreTarget}
        onOpenChange={(open) => !open && setRestoreTarget(null)}
        title={`Restore ${restoreTarget?.id ?? ''}?`}
        description="A pre-restore snapshot will be created automatically before this runs, so you can always roll back. After restore, restart MCPlexer to pick up the new state."
        confirmLabel={restoring ? 'Restoring…' : 'Restore'}
        onConfirm={handleRestore}
      />

      <ConfirmDialog
        open={!!deleteTarget}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title="Delete backup"
        description={`Are you sure you want to delete "${deleteTarget?.id}"?`}
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={handleDelete}
      />
    </div>
  )
}

function BackupRow({
  mf,
  onRestore,
  onDelete,
}: {
  mf: BackupManifest
  onRestore: () => void
  onDelete: () => void
}) {
  return (
    <Card>
      <CardContent className="flex flex-wrap items-center gap-3 p-3">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 font-mono text-sm">
            <span className="truncate">{mf.id}</span>
            {mf.pre_restore_of && (
              <Badge variant="outline" className="text-[10px]">
                pre-restore of {mf.pre_restore_of}
              </Badge>
            )}
            {mf.includes_secrets && (
              <Badge variant="secondary" className="text-[10px]">
                includes secrets
              </Badge>
            )}
            {mf.includes_skills && (
              <Badge variant="secondary" className="text-[10px]">
                includes skills
              </Badge>
            )}
          </div>
          <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
            <span>{new Date(mf.created_at).toLocaleString()}</span>
            <span>{formatBytes(mf.size_bytes)}</span>
            {mf.mcplexer_version && <span>v{mf.mcplexer_version}</span>}
            {mf.note && <span className="italic">"{mf.note}"</span>}
          </div>
        </div>
        <div className="flex gap-1">
          <a
            href={backupDownloadURL(mf.id)}
            download={`${mf.id}.tar.gz`}
            data-testid={`backup-download-${mf.id}`}
          >
            <Button variant="ghost" size="sm" className="h-8 w-8 p-0" aria-label="Download">
              <Download className="h-4 w-4" />
            </Button>
          </a>
          <Button
            variant="ghost"
            size="sm"
            className="h-8 gap-1 text-xs"
            onClick={onRestore}
            data-testid={`backup-restore-${mf.id}`}
          >
            <RotateCcw className="h-3.5 w-3.5" />
            Restore
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="h-8 w-8 p-0 hover:bg-destructive/10 hover:text-destructive"
            onClick={onDelete}
            aria-label="Delete"
            data-testid={`backup-delete-${mf.id}`}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}
