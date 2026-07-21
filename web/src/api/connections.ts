import type {
  CatalogResponse,
  ConnectDownstreamRequest,
  ConnectDownstreamResponse,
  DownstreamOAuthSetupResponse,
  DownstreamOAuthStatusResponse,
  DownstreamServer,
  OAuthCapabilities,
  RouteRule,
} from './types'
import { request } from './transport'

// Server Catalog
export function fetchCatalog(): Promise<CatalogResponse> {
  return request('/catalog')
}

// Downstream Servers
export function listDownstreams(init?: RequestInit): Promise<DownstreamServer[]> {
  return request('/downstreams', init)
}

export function getDownstream(id: string): Promise<DownstreamServer> {
  return request(`/downstreams/${id}`)
}

export function createDownstream(
  data: Omit<DownstreamServer, 'id' | 'created_at' | 'updated_at' | 'capabilities_cache' | 'source'> & { id?: string },
): Promise<DownstreamServer> {
  return request('/downstreams', {
    method: 'POST',
    body: JSON.stringify(data),
  })
}

export function updateDownstream(
  id: string,
  data: Partial<Omit<DownstreamServer, 'id' | 'created_at' | 'updated_at'>>,
): Promise<DownstreamServer> {
  return request(`/downstreams/${id}`, {
    method: 'PUT',
    body: JSON.stringify(data),
  })
}

export function deleteDownstream(id: string): Promise<void> {
  return request(`/downstreams/${id}`, { method: 'DELETE' })
}

// Route Rules
export function listRoutes(init?: RequestInit): Promise<RouteRule[]> {
  return request('/routes', init)
}

export function getRoute(id: string): Promise<RouteRule> {
  return request(`/routes/${id}`)
}

export function createRoute(
  data: Omit<RouteRule, 'id' | 'created_at' | 'updated_at'>,
): Promise<RouteRule> {
  return request('/routes', {
    method: 'POST',
    body: JSON.stringify(data),
  })
}

export function updateRoute(
  id: string,
  data: Partial<Omit<RouteRule, 'id' | 'created_at' | 'updated_at'>>,
): Promise<RouteRule> {
  return request(`/routes/${id}`, {
    method: 'PUT',
    body: JSON.stringify(data),
  })
}

export function deleteRoute(id: string): Promise<void> {
  return request(`/routes/${id}`, { method: 'DELETE' })
}

export function bulkCreateRoutes(
  rules: Omit<RouteRule, 'id' | 'created_at' | 'updated_at'>[],
): Promise<RouteRule[]> {
  return request('/routes/bulk', {
    method: 'POST',
    body: JSON.stringify(rules),
  })
}

// Discover Tools — spawns the downstream process and lists its tools; can
// take tens of seconds for a cold start.
export function discoverTools(id: string): Promise<DownstreamServer> {
  return request(`/downstreams/${id}/discover`, { method: 'POST' }, { timeoutMs: 120_000 })
}

// Downstream OAuth
export function setupDownstreamOAuth(
  id: string,
  scopeName?: string,
): Promise<DownstreamOAuthSetupResponse> {
  return request(
    `/downstreams/${id}/oauth-setup`,
    {
      method: 'POST',
      body: JSON.stringify(scopeName ? { auth_scope_name: scopeName } : {}),
    },
    { timeoutMs: 90_000 },
  )
}

// Connect Downstream (unified setup)
export function connectDownstream(
  id: string,
  data: ConnectDownstreamRequest,
): Promise<ConnectDownstreamResponse> {
  return request(
    `/downstreams/${id}/connect`,
    { method: 'POST', body: JSON.stringify(data) },
    { timeoutMs: 120_000 },
  )
}

export function getDownstreamOAuthStatus(
  id: string,
): Promise<DownstreamOAuthStatusResponse> {
  return request(`/downstreams/${id}/oauth-status`)
}

export function getOAuthCapabilities(
  id: string,
): Promise<OAuthCapabilities> {
  return request(`/downstreams/${id}/oauth-capabilities`)
}
