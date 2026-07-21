// Compatibility facade for the dashboard API.
//
// Keep imports from '@/api/client' stable while implementations live beside
// the domain they serve. New domain clients should import request/apiURL from
// './transport' and be re-exported here.
export { ApiClientError, apiURL, request } from './transport'
export type { RequestOptions } from './transport'

export * from './workspaces'
export * from './auth'
export * from './connections'
export * from './audit'
export * from './dashboard'
export * from './approvals'
export * from './system'
export * from './descriptions'
export * from './mesh'
export * from './addons'
export * from './p2p'
export * from './backups'
export * from './skill-registry'
export * from './guards'
export * from './model-profiles'
export * from './hammerspoon'
export * from './harness-setup'
