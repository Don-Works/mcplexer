import type {
  AuthScope,
  OAuthProvider,
  OAuthQuickSetupRequest,
  OAuthQuickSetupResponse,
  OAuthStatus,
  OAuthTemplate,
} from './types'
import { request } from './transport'

// Auth Scopes
export function listAuthScopes(init?: RequestInit): Promise<AuthScope[]> {
  return request('/auth-scopes', init)
}

export function getAuthScope(id: string): Promise<AuthScope> {
  return request(`/auth-scopes/${id}`)
}

export function createAuthScope(
  data: Omit<AuthScope, 'id' | 'has_secrets' | 'source' | 'created_at' | 'updated_at'>,
): Promise<AuthScope> {
  return request('/auth-scopes', {
    method: 'POST',
    body: JSON.stringify(data),
  })
}

export function updateAuthScope(
  id: string,
  data: Partial<Omit<AuthScope, 'id' | 'created_at' | 'updated_at'>>,
): Promise<AuthScope> {
  return request(`/auth-scopes/${id}`, {
    method: 'PUT',
    body: JSON.stringify(data),
  })
}

export function deleteAuthScope(id: string): Promise<void> {
  return request(`/auth-scopes/${id}`, { method: 'DELETE' })
}

// Secrets (env-type auth scopes)
export function listSecretKeys(scopeId: string): Promise<{ keys: string[] }> {
  return request(`/auth-scopes/${scopeId}/secrets`)
}

export function putSecret(scopeId: string, key: string, value: string): Promise<{ status: string }> {
  return request(`/auth-scopes/${scopeId}/secrets`, {
    method: 'PUT',
    body: JSON.stringify({ key, value }),
  })
}

export function deleteSecret(scopeId: string, key: string): Promise<void> {
  return request(`/auth-scopes/${scopeId}/secrets/${encodeURIComponent(key)}`, {
    method: 'DELETE',
  })
}

// OAuth Providers
export function listOAuthProviders(): Promise<OAuthProvider[]> {
  return request('/oauth-providers')
}

export function getOAuthProvider(id: string): Promise<OAuthProvider> {
  return request(`/oauth-providers/${id}`)
}

export function createOAuthProvider(
  data: Omit<OAuthProvider, 'id' | 'has_client_secret' | 'source' | 'created_at' | 'updated_at'> & { client_secret?: string },
): Promise<OAuthProvider> {
  return request('/oauth-providers', {
    method: 'POST',
    body: JSON.stringify(data),
  })
}

export function updateOAuthProvider(
  id: string,
  data: Partial<Omit<OAuthProvider, 'id' | 'has_client_secret' | 'source' | 'created_at' | 'updated_at'>>,
): Promise<OAuthProvider> {
  return request(`/oauth-providers/${id}`, {
    method: 'PUT',
    body: JSON.stringify(data),
  })
}

export function deleteOAuthProvider(id: string): Promise<void> {
  return request(`/oauth-providers/${id}`, { method: 'DELETE' })
}

// OAuth Templates
export function listOAuthTemplates(): Promise<OAuthTemplate[]> {
  return request('/oauth-templates')
}

// OIDC Discovery
export function discoverOIDC(issuerURL: string): Promise<{
  authorize_url: string
  token_url: string
  scopes: string[]
  use_pkce: boolean
  issuer: string
}> {
  return request('/oauth-providers/discover', {
    method: 'POST',
    body: JSON.stringify({ issuer_url: issuerURL }),
  })
}

// OAuth Quick Setup
export function oauthQuickSetup(data: OAuthQuickSetupRequest): Promise<OAuthQuickSetupResponse> {
  return request(
    '/auth-scopes/oauth-quick-setup',
    { method: 'POST', body: JSON.stringify(data) },
    { timeoutMs: 90_000 },
  )
}

// OAuth Flow
export function getOAuthAuthorizeURL(scopeId: string): Promise<{ authorize_url: string }> {
  return request(`/auth-scopes/${scopeId}/oauth/authorize`)
}

export function getOAuthStatus(scopeId: string): Promise<OAuthStatus> {
  return request(`/auth-scopes/${scopeId}/oauth/status`)
}

export function revokeOAuthToken(scopeId: string): Promise<void> {
  return request(`/auth-scopes/${scopeId}/oauth/revoke`, { method: 'POST' })
}
