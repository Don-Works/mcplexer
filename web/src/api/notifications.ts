import { request } from './client'

export type NotificationKind = 'mesh' | 'approval' | 'system' | 'secret' | string
export type NotificationPriority = 'critical' | 'high' | 'normal' | 'low' | string

export interface StoredNotification {
  id: number
  message_id: string
  source: NotificationKind
  agent_name: string
  role: string
  kind: string
  priority: NotificationPriority
  title: string
  body: string
  tags: string
  link: string
  created_at: string
  read_at?: string | null
}

export interface NotificationListResponse {
  notifications: StoredNotification[]
  unread_count: number
}

export interface ListParams {
  source?: string
  kind?: string
  priority?: string
  unread?: boolean
  before?: number
  limit?: number
}

export function listNotifications(p: ListParams = {}): Promise<NotificationListResponse> {
  const q = new URLSearchParams()
  if (p.source) q.set('source', p.source)
  if (p.kind) q.set('kind', p.kind)
  if (p.priority) q.set('priority', p.priority)
  if (p.unread) q.set('unread', 'true')
  if (p.before) q.set('before', String(p.before))
  if (p.limit) q.set('limit', String(p.limit))
  const qs = q.toString()
  return request(`/notifications${qs ? `?${qs}` : ''}`)
}

export function unreadNotificationCount(): Promise<{ unread_count: number }> {
  return request('/notifications/unread-count')
}

export function markNotificationRead(id: number): Promise<{ ok: boolean }> {
  return request(`/notifications/${id}/read`, { method: 'POST' })
}

export function markNotificationsRead(ids: number[]): Promise<{ ok: boolean; unread_count: number }> {
  return request('/notifications/read', {
    method: 'POST',
    body: JSON.stringify({ ids }),
  })
}

export function markAllNotificationsRead(): Promise<{ ok: boolean; unread_count: number }> {
  return request('/notifications/read', {
    method: 'POST',
    body: JSON.stringify({ all: true }),
  })
}

export interface PushPublicKeyResponse {
  public_key: string
  supported: boolean
}

export interface PushStatusResponse {
  subscription_count: number
}

export interface BrowserPushSubscriptionJSON {
  endpoint?: string
  keys?: {
    p256dh?: string
    auth?: string
  }
}

export function getPushPublicKey(): Promise<PushPublicKeyResponse> {
  return request('/push/public-key')
}

export function getPushStatus(): Promise<PushStatusResponse> {
  return request('/push/status')
}

export function subscribePush(
  subscription: BrowserPushSubscriptionJSON,
  deviceLabel?: string,
): Promise<{ ok: boolean }> {
  return request('/push/subscribe', {
    method: 'POST',
    body: JSON.stringify({ subscription, device_label: deviceLabel || '' }),
  })
}

export function unsubscribePush(endpoint: string): Promise<{ ok: boolean }> {
  return request('/push/unsubscribe', {
    method: 'POST',
    body: JSON.stringify({ endpoint }),
  })
}

export function sendTestPush(): Promise<{ ok: boolean }> {
  return request('/push/test', { method: 'POST' })
}
