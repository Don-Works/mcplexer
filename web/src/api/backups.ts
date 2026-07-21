import { apiURL, request } from './transport'

// Backup / restore — full snapshots of the data dir (DB + secrets + skills).
export interface BackupManifest {
  id: string
  created_at: string
  mcplexer_version?: string
  note?: string
  db_sha256: string
  size_bytes: number
  includes_secrets: boolean
  includes_skills: boolean
  pre_restore_of?: string
}

export interface BackupRestoreResult {
  restored_from: string
  pre_restore_snapshot_id: string
  daemon_restart_required: boolean
}

export function listBackups(): Promise<BackupManifest[]> {
  return request('/backups')
}

export function createBackup(note?: string): Promise<BackupManifest> {
  return request(
    '/backups',
    { method: 'POST', body: JSON.stringify({ note: note ?? '' }) },
    { timeoutMs: 180_000 },
  )
}

export function restoreBackup(id: string): Promise<BackupRestoreResult> {
  return request(
    `/backups/${encodeURIComponent(id)}/restore`,
    { method: 'POST' },
    { timeoutMs: 180_000 },
  )
}

export function deleteBackup(id: string): Promise<void> {
  return request(`/backups/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

// Direct download URL — server streams the tarball with Content-Disposition.
// Used as <a href={...} download> rather than a fetch + blob roundtrip.
export function backupDownloadURL(id: string): string {
  return apiURL(`/backups/${encodeURIComponent(id)}/download`)
}
