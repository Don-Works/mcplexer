// task-attachments.ts — REST client for the attachment endpoints
// (C2.3 of the attachments initiative). Mirrors
// internal/api/task_attachments_handler.go.

import { request, apiURL } from './client'

export interface TaskAttachment {
  id: string
  task_id: string
  workspace_id: string
  filename?: string
  mime_type: string
  size_bytes: number
  sha256: string
  storage_path: string
  uploader_session_id?: string
  uploader_kind: string
  created_at: string
  deleted_at?: string | null
}

// listTaskAttachments returns the metadata rows for a task.
export function listTaskAttachments(taskID: string): Promise<TaskAttachment[]> {
  return request<TaskAttachment[]>(
    `/tasks/${encodeURIComponent(taskID)}/attachments`,
  )
}

// uploadTaskAttachment posts a file to the attachment endpoint.
// Uses fetch directly (rather than request<T>) because request<T> sets
// JSON content-type and stringifies the body — multipart needs neither.
export async function uploadTaskAttachment(
  taskID: string,
  file: File,
  apiToken?: string,
): Promise<TaskAttachment> {
  const fd = new FormData()
  fd.append('file', file, file.name)
  const headers: Record<string, string> = {}
  if (apiToken) headers['Authorization'] = `Bearer ${apiToken}`
  const resp = await fetch(
    apiURL(`/tasks/${encodeURIComponent(taskID)}/attachments`),
    { method: 'POST', body: fd, headers, credentials: 'same-origin' },
  )
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(`upload failed: ${resp.status} ${text}`)
  }
  return (await resp.json()) as TaskAttachment
}

// downloadTaskAttachmentURL is the URL the browser can navigate to
// directly — it sets Content-Disposition so the browser prompts for
// save. Cheaper than fetching into JS then re-blobbing.
export function downloadTaskAttachmentURL(id: string): string {
  return apiURL(`/attachments/${encodeURIComponent(id)}`)
}

export function deleteTaskAttachment(id: string): Promise<void> {
  return request<void>(`/attachments/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
}

// formatFileSize formats a byte count as a human-readable size string
// using SI (1000) units to match the dashboard's overall convention.
export function formatFileSize(bytes: number): string {
  if (bytes < 1000) return `${bytes} B`
  if (bytes < 1_000_000) return `${(bytes / 1000).toFixed(1)} KB`
  if (bytes < 1_000_000_000) return `${(bytes / 1_000_000).toFixed(1)} MB`
  return `${(bytes / 1_000_000_000).toFixed(2)} GB`
}
