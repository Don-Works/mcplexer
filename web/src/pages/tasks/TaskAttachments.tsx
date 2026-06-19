// TaskAttachments — the attachment row group on TaskDetailPage. Lets
// users drag-drop or click-pick files to attach to a task, lists what's
// already attached, surfaces inline preview for small text/markdown and
// thumbnails for images, and offers download/delete affordances.
//
// Backend: /api/v1/tasks/{id}/attachments (list + upload) + the new
// /api/v1/attachments/{id} (download + delete) endpoints. See
// internal/api/task_attachments_handler.go.

import { useCallback, useEffect, useRef, useState } from 'react'
import { Download, FileText, Image as ImageIcon, Loader2, Paperclip, Trash2, UploadCloud } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  deleteTaskAttachment,
  downloadTaskAttachmentURL,
  formatFileSize,
  listTaskAttachments,
  uploadTaskAttachment,
  type TaskAttachment,
} from '@/api/task-attachments'

interface TaskAttachmentsProps {
  taskId: string
}

export function TaskAttachments({ taskId }: TaskAttachmentsProps) {
  const [rows, setRows] = useState<TaskAttachment[]>([])
  const [loading, setLoading] = useState(false)
  const [uploading, setUploading] = useState(false)
  const [pendingDelete, setPendingDelete] = useState<TaskAttachment | null>(null)
  const [dragging, setDragging] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  const refetch = useCallback(async () => {
    setLoading(true)
    try {
      const list = await listTaskAttachments(taskId)
      setRows(list)
    } catch (err) {
      toast.error('Failed to load attachments: ' + String(err))
    } finally {
      setLoading(false)
    }
  }, [taskId])

  useEffect(() => {
    void refetch()
  }, [refetch])

  const handleFiles = useCallback(
    async (files: FileList | File[]) => {
      const list = Array.from(files)
      if (list.length === 0) return
      setUploading(true)
      try {
        for (const f of list) {
          await uploadTaskAttachment(taskId, f)
        }
        toast.success(list.length === 1 ? `Attached ${list[0].name}` : `Attached ${list.length} files`)
        await refetch()
      } catch (err) {
        toast.error('Upload failed: ' + String(err))
      } finally {
        setUploading(false)
      }
    },
    [taskId, refetch],
  )

  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault()
    setDragging(false)
    if (e.dataTransfer.files && e.dataTransfer.files.length > 0) {
      void handleFiles(e.dataTransfer.files)
    }
  }

  const handleDelete = async () => {
    if (!pendingDelete) return
    try {
      await deleteTaskAttachment(pendingDelete.id)
      toast.success('Removed attachment')
      setPendingDelete(null)
      await refetch()
    } catch (err) {
      toast.error('Delete failed: ' + String(err))
    }
  }

  return (
    <section>
      <div className="mb-1.5 flex items-center justify-between">
        <h3 className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/70">
          <Paperclip className="mr-1 inline h-3 w-3" />
          Attachments {rows.length > 0 && <span className="ml-1 text-muted-foreground/60">({rows.length})</span>}
        </h3>
      </div>

      {loading && rows.length === 0 ? (
        <div className="flex items-center gap-2 text-[11px] text-muted-foreground/70">
          <Loader2 className="h-3 w-3 animate-spin" />
          loading…
        </div>
      ) : (
        <>
          {rows.length > 0 && (
            <ul className="mb-3 space-y-1.5">
              {rows.map((row) => (
                <AttachmentRow
                  key={row.id}
                  row={row}
                  onDelete={() => setPendingDelete(row)}
                />
              ))}
            </ul>
          )}

          <div
            onDragOver={(e) => {
              e.preventDefault()
              setDragging(true)
            }}
            onDragLeave={() => setDragging(false)}
            onDrop={handleDrop}
            className={
              'flex flex-col items-center justify-center gap-1.5 rounded-sm border border-dashed px-3 py-4 text-[11px] text-muted-foreground/70 transition-colors ' +
              (dragging ? 'border-primary bg-primary/5' : 'border-border/60 hover:border-border')
            }
            data-testid="task-attachments-dropzone"
          >
            <UploadCloud className="h-4 w-4" />
            {uploading ? (
              <span className="flex items-center gap-1.5">
                <Loader2 className="h-3 w-3 animate-spin" /> uploading…
              </span>
            ) : (
              <>
                <span>Drag files here, or</span>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => inputRef.current?.click()}
                  data-testid="task-attachments-pick"
                >
                  pick a file
                </Button>
              </>
            )}
            <input
              ref={inputRef}
              type="file"
              multiple
              className="hidden"
              onChange={(e) => {
                if (e.target.files) {
                  void handleFiles(e.target.files)
                  e.target.value = ''
                }
              }}
            />
          </div>
        </>
      )}

      {pendingDelete && (
        <ConfirmDialog
          open
          onOpenChange={(o) => { if (!o) setPendingDelete(null) }}
          title="Remove attachment"
          description={`Remove ${pendingDelete.filename || pendingDelete.id}? The on-disk blob is preserved for the audit trail; only the index row is soft-deleted.`}
          confirmLabel="Remove"
          variant="destructive"
          onConfirm={handleDelete}
        />
      )}
    </section>
  )
}

function AttachmentRow({ row, onDelete }: { row: TaskAttachment; onDelete: () => void }) {
  const isImage = row.mime_type.startsWith('image/')
  const Icon = isImage ? ImageIcon : FileText
  return (
    <li className="group flex items-center gap-2 rounded-sm border border-border/60 bg-card/30 px-2 py-1.5 text-[12px]">
      {isImage ? (
        <img
          src={downloadTaskAttachmentURL(row.id)}
          alt={row.filename}
          className="h-8 w-8 shrink-0 rounded-sm object-cover"
          loading="lazy"
        />
      ) : (
        <Icon className="h-4 w-4 shrink-0 text-muted-foreground/70" />
      )}
      <div className="min-w-0 flex-1">
        <div className="truncate font-medium" title={row.filename}>
          {row.filename || row.id}
        </div>
        <div className="text-[10px] text-muted-foreground/60">
          {formatFileSize(row.size_bytes)} · {row.mime_type || 'unknown'} ·{' '}
          {new Date(row.created_at).toLocaleString()}
        </div>
      </div>
      <a
        href={downloadTaskAttachmentURL(row.id)}
        download={row.filename || row.id}
        className="rounded-sm p-1 text-muted-foreground/70 opacity-0 transition-opacity hover:text-foreground group-hover:opacity-100"
        title="Download"
        aria-label={`Download attachment ${row.filename || row.id}`}
        data-testid="task-attachment-download"
      >
        <Download className="h-3.5 w-3.5" />
      </a>
      <button
        onClick={onDelete}
        className="rounded-sm p-1 text-muted-foreground/70 opacity-0 transition-opacity hover:text-destructive group-hover:opacity-100"
        title="Remove"
        aria-label={`Remove attachment ${row.filename || row.id}`}
        data-testid="task-attachment-delete"
      >
        <Trash2 className="h-3.5 w-3.5" />
      </button>
    </li>
  )
}
