import type { SystemInfo } from '@/api/client'

export type ServerProfile = 'full' | 'skills' | 'tasks' | 'skills+tasks'

export function normalizeServerProfile(profile?: string | null): ServerProfile {
  const raw = (profile ?? '').trim().toLowerCase().replace(/,/g, '+').replace(/\s+/g, '')
  switch (raw) {
    case 'skills':
      return 'skills'
    case 'tasks':
      return 'tasks'
    case 'skills+tasks':
    case 'tasks+skills':
      return 'skills+tasks'
    default:
      return 'full'
  }
}

export function isServerProfile(system?: Pick<SystemInfo, 'server_profile'> | null): boolean {
  return normalizeServerProfile(system?.server_profile) !== 'full'
}

export function hasCapability(
  system: Pick<SystemInfo, 'server_profile' | 'capabilities'> | null | undefined,
  key: string,
): boolean {
  if (!system?.capabilities) {
    const profile = normalizeServerProfile(system?.server_profile)
    if (profile === 'full') return true
    if (key === 'server_settings' || key === 'signals') return true
    if (key === 'skills') return profile === 'skills' || profile === 'skills+tasks'
    if (key === 'tasks') return profile === 'tasks' || profile === 'skills+tasks'
    return false
  }
  return Boolean(system.capabilities[key])
}

export function serverHomePath(system?: Pick<SystemInfo, 'server_profile' | 'capabilities'> | null): string {
  if (!isServerProfile(system)) return '/'
  if (hasCapability(system, 'skills')) return '/skills'
  if (hasCapability(system, 'tasks')) return '/tasks'
  return '/'
}

export function serverProfileLabel(system?: Pick<SystemInfo, 'server_profile'> | null): string {
  switch (normalizeServerProfile(system?.server_profile)) {
    case 'skills':
      return 'Skills server'
    case 'tasks':
      return 'Tasks server'
    case 'skills+tasks':
      return 'Skills + tasks server'
    default:
      return 'MCPlexer'
  }
}
