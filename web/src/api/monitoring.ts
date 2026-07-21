// Monitoring API client. Mirrors internal/api/monitoring_handler.go +
// monitoring_query_handler.go: remote hosts, log sources, alert
// channels, template explorer, digest preview, runner status, notify.

import { request } from './client'

export interface RemoteHost {
  id: string
  workspace_id: string
  name: string
  ssh_user: string
  ssh_host: string
  ssh_port: number
  auth_scope_id: string
  host_key_pin?: string
  enabled: boolean
  created_at: string
  updated_at: string
}

export type LogSourceKind = 'docker' | 'compose' | 'swarm' | 'journald' | 'file'

export interface LogSource {
  id: string
  workspace_id: string
  remote_host_id: string
  name: string
  kind: LogSourceKind
  selector: string
  schedule_spec: string
  max_pull_bytes: number
  retention_mb: number
  retention_days: number
  severity_rules_json?: string
  enabled: boolean
  cursor_ts?: string
  cursor_hash?: string
  consecutive_failures: number
  created_at: string
  updated_at: string
}

export type ChannelKind = 'gchat_webhook' | 'telegram' | 'whatsapp' | 'mesh'
export type Severity = 'info' | 'warn' | 'error' | 'critical'
export const SEVERITIES: Severity[] = ['info', 'warn', 'error', 'critical']

export interface MonitoringChannel {
  id: string
  workspace_id: string
  name: string
  kind: ChannelKind
  config_json: string
  min_severity: Severity
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface MonitoringTemplate {
  id: string
  source_id: string
  source_name: string
  masked: string
  severity: Severity
  count: number
  window_lines: number
  first_seen: string
  last_seen: string
  sample_first: string
  sample_last: string
  acked: boolean
  ack_note?: string
  new: boolean
}

export interface MonitoringStatus {
  gateway_hostname: string
  runner_enabled: boolean
  notify_enabled: boolean
}

const ws = (workspaceId: string) => `?workspace_id=${encodeURIComponent(workspaceId)}`

export const monitoringStatus = () =>
  request<MonitoringStatus>('/monitoring/status')

// --- hosts ---
export const listRemoteHosts = (workspaceId: string) =>
  request<RemoteHost[]>(`/remote-hosts${ws(workspaceId)}`)
export const createRemoteHost = (h: Partial<RemoteHost>) =>
  request<RemoteHost>('/remote-hosts', { method: 'POST', body: JSON.stringify(h) })
export const updateRemoteHost = (id: string, patch: Partial<RemoteHost>) =>
  request<RemoteHost>(`/remote-hosts/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(patch) })
export const deleteRemoteHost = (id: string) =>
  request<void>(`/remote-hosts/${encodeURIComponent(id)}`, { method: 'DELETE' })
export const repinRemoteHost = (id: string) =>
  request<{ status: string }>(`/remote-hosts/${encodeURIComponent(id)}/repin`, { method: 'POST' })

// --- sources ---
export const listLogSources = (workspaceId: string) =>
  request<LogSource[]>(`/log-sources${ws(workspaceId)}`)
export const createLogSource = (s: Partial<LogSource>) =>
  request<LogSource>('/log-sources', { method: 'POST', body: JSON.stringify(s) })
export const updateLogSource = (id: string, patch: Partial<LogSource>) =>
  request<LogSource>(`/log-sources/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(patch) })
export const deleteLogSource = (id: string) =>
  request<void>(`/log-sources/${encodeURIComponent(id)}`, { method: 'DELETE' })

// --- channels ---
export const listChannels = (workspaceId: string) =>
  request<MonitoringChannel[]>(`/monitoring-channels${ws(workspaceId)}`)
export const createChannel = (c: Partial<MonitoringChannel>) =>
  request<MonitoringChannel>('/monitoring-channels', { method: 'POST', body: JSON.stringify(c) })
export const updateChannel = (id: string, patch: Partial<MonitoringChannel>) =>
  request<MonitoringChannel>(`/monitoring-channels/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(patch) })
export const deleteChannel = (id: string) =>
  request<void>(`/monitoring-channels/${encodeURIComponent(id)}`, { method: 'DELETE' })

// --- explorer / digest / notify ---
export const listTemplates = (workspaceId: string, window = '24h') =>
  request<{ templates: MonitoringTemplate[]; window: string }>(
    `/monitoring/templates${ws(workspaceId)}&window=${encodeURIComponent(window)}`)
export const ackTemplate = (id: string, workspaceId: string, note?: string) =>
  request<{ acked: boolean }>(
    `/monitoring/templates/${encodeURIComponent(id)}/ack${ws(workspaceId)}`,
    {
      method: 'POST', body: JSON.stringify({ note: note ?? '' }),
    },
  )
export const fetchDigest = (workspaceId: string, window: string, budgetTokens: number) =>
  request<{ digest: string; approx_tokens: number }>(
    `/monitoring/digest${ws(workspaceId)}&window=${encodeURIComponent(window)}&budget_tokens=${budgetTokens}`)
export const sendTestNotification = (workspaceId: string, severity: Severity, remoteHostId?: string) =>
  request<{ dispatched: boolean }>('/monitoring/notify', {
    method: 'POST',
		body: JSON.stringify({
			workspace_id: workspaceId,
			severity,
			new_incident: severity === 'critical',
			title: 'test notification from the Monitoring page',
      body: 'If you can read this, the channel works. Envelope, severity floor, and secret resolution all exercised.',
      remote_host_id: remoteHostId,
      test: true,
    }),
  })

// --- incidents (read) ---
// Mirrors internal/api/monitoring_incidents_handler.go + the
// MonitoringIncidentView store shape (internal/store/monitoring_incident_read.go):
// the incident flattened together with the notification-policy fields
// (effective_severity/class_kind/active) the dispatcher already computes.

export type IncidentClassKind =
  | 'template' | 'correlation' | 'absence' | 'collection' | 'other'
export type IncidentDisposition =
  | 'actionable' | 'uncertain' | 'evidence-gap' | 'benign'

export interface MonitoringIncident {
  id: string
  workspace_id: string
  class_key: string
  task_id: string
  disposition: IncidentDisposition
  severity: Severity
  // Human-readable title. Stream A makes this the matched-signal context
  // rather than the auto/<hash> rule name — consume it verbatim.
  title: string
  occurrence_count: number
  event_count: number
  first_seen: string
  last_seen: string
  last_notified_at?: string
  last_notified_severity?: string
  created_at: string
  updated_at: string
  // Derived view fields (never null on the list endpoint).
  effective_severity: Severity
  class_kind: IncidentClassKind
  class_ref?: string
  expected_signal_id?: string
  active: boolean
  // Suppression state, two layers:
  //  - Raw attribution off the migration-150 columns (silenced_*/acked_*).
  //  - Derived, pierce/expiry-aware flags the daemon recomputes on every read
  //    AND every POST-action response. PREFER these for UI decisions: a silence
  //    whose incident escalated past the floor it was muted at reads
  //    silence_active=false even though silenced_until is still in the future,
  //    which the raw column cannot express. suppressed = ack OR silence in
  //    force right now.
  // All optional: a daemon predating the derived surface omits them and the
  // dashboard falls back to the raw columns (see isSuppressed / isSilenced).
  // There is deliberately NO dismissed_at column — dismiss resolves the row via
  // disposition='benign' (+ a resolution receipt), which the feed filters on.
  silenced_until?: string
  silenced_at?: string
  silenced_by?: string
  acked_at?: string
  acked_by?: string
  suppressed?: boolean
  ack_active?: boolean
  silence_active?: boolean
}

export interface IncidentListResponse {
  incidents: MonitoringIncident[]
  total: number
  active: number
  class_kinds: IncidentClassKind[]
}

export interface IncidentListParams {
  status?: 'active' | 'inactive'
  since?: string
  limit?: number
}

export const listIncidents = (
  workspaceId: string,
  params: IncidentListParams = {},
) => {
  const q = new URLSearchParams({ workspace_id: workspaceId })
  if (params.status) q.set('status', params.status)
  if (params.since) q.set('since', params.since)
  if (params.limit) q.set('limit', String(params.limit))
  return request<IncidentListResponse>(`/monitoring/incidents?${q.toString()}`)
}

// --- incidents (actions) ---
// The actions stream's mutation surface (team-lead-confirmed contract; these
// routes may not be live at build time — a POST to an unbuilt route hits the
// SPA fallback (HTML 200 -> request() throws on JSON parse) or a 405, so the
// caller's optimistic update rolls back and surfaces an error rather than
// faking success). Workspace-scoped like every neighbouring incident read;
// each returns the updated incident view.
//   POST /monitoring/incidents/{id}/ack        {note?}
//     Pauses re-notification; the incident stays OPEN. A later severity
//     ESCALATION pierces the ack and notifies again — "quiet unless worse".
//   POST /monitoring/incidents/{id}/silence    {duration /* Go dur */, note?}
//     Bounded + auto-expiring (populates silenced_until), reversible via
//     unsilence. Escalation pierces it too.
//   POST /monitoring/incidents/{id}/unsilence  {}
//     Lifts an active silence immediately.
//   POST /monitoring/incidents/{id}/dismiss    {note?}
//     Resolves via the terminal/benign task vocabulary (disposition -> benign).
//     A later recurrence of the class is a NEW incident and fires again — this
//     is not a permanent mute of the class.
export const ackIncident = (workspaceId: string, id: string, note?: string) =>
  request<MonitoringIncident>(
    `/monitoring/incidents/${encodeURIComponent(id)}/ack${ws(workspaceId)}`,
    { method: 'POST', body: JSON.stringify({ note: note ?? '' }) },
  )
export const silenceIncident = (
  workspaceId: string, id: string, duration: string, note?: string,
) =>
  request<MonitoringIncident>(
    `/monitoring/incidents/${encodeURIComponent(id)}/silence${ws(workspaceId)}`,
    { method: 'POST', body: JSON.stringify({ duration, note: note ?? '' }) },
  )
export const unsilenceIncident = (workspaceId: string, id: string) =>
  request<MonitoringIncident>(
    `/monitoring/incidents/${encodeURIComponent(id)}/unsilence${ws(workspaceId)}`,
    { method: 'POST', body: JSON.stringify({}) },
  )
export const dismissIncident = (workspaceId: string, id: string, note?: string) =>
  request<MonitoringIncident>(
    `/monitoring/incidents/${encodeURIComponent(id)}/dismiss${ws(workspaceId)}`,
    { method: 'POST', body: JSON.stringify({ note: note ?? '' }) },
  )
