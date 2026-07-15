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
export const ackTemplate = (id: string, note?: string) =>
  request<{ acked: boolean }>(`/monitoring/templates/${encodeURIComponent(id)}/ack`, {
    method: 'POST', body: JSON.stringify({ note: note ?? '' }),
  })
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
