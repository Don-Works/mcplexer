import { request } from './transport'

// Custom MCP Addons (M6.2)
export type AddonAuthKind =
  | 'none'
  | 'bearer'
  | 'api_key_header'
  | 'api_key_query'
  | 'hawk'
  | 'oauth2'
  | 'oauth2_pending'

export type OAuth2GrantType = 'authorization_code' | 'client_credentials'

export interface AddonAuthSpec {
  kind: AddonAuthKind
  header_name?: string
  query_name?: string

  // OAuth2 fields. Empty unless kind is oauth2 or oauth2_pending.
  auth_url?: string
  token_url?: string
  scopes?: string[]
  client_id?: string
  use_pkce?: boolean
  grant_type?: OAuth2GrantType
}

export interface AddonParamSpec {
  name: string
  type: 'string' | 'integer' | 'number' | 'boolean'
  in: 'path' | 'query' | 'body'
  description?: string
  required?: boolean
}

export interface AddonEndpointSpec {
  name: string
  description: string
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  path: string
  params?: AddonParamSpec[]
}

export interface AddonSpec {
  name: string
  description: string
  base_url: string
  parent_server: string
  auth_scope?: string
  auth: AddonAuthSpec
  endpoints: AddonEndpointSpec[]
}

export interface AddonCreateResponse {
  name: string
  path: string
  tools: string[]
}

export function previewAddon(spec: AddonSpec): Promise<{ yaml: string }> {
  return request('/addons/preview', {
    method: 'POST',
    body: JSON.stringify(spec),
  })
}

export function createAddon(spec: AddonSpec): Promise<AddonCreateResponse> {
  return request('/addons', {
    method: 'POST',
    body: JSON.stringify(spec),
  })
}

// importAddonOpenAPI returns a draft AddonSpec parsed from an OpenAPI 3.x
// document. parent_server is intentionally left unset — the user picks one
// in the wizard's Basics step.
export function importAddonOpenAPI(input: {
  spec_url?: string
  spec_inline?: string
}): Promise<AddonSpec> {
  return request(
    '/addons/import-openapi',
    { method: 'POST', body: JSON.stringify(input) },
    { timeoutMs: 90_000 },
  )
}

export interface AddonOAuthWizardRequest {
  auth_scope_name: string
  parent_server: string
  auth_url: string
  token_url: string
  scopes: string[]
  client_id: string
  client_secret?: string
  use_pkce: boolean
  grant_type: OAuth2GrantType
}

export interface AddonOAuthWizardResponse {
  auth_scope: { id: string; name: string } | null
  provider: { id: string; name: string } | null
  authorize_url?: string
  human_approval_required: boolean
  message: string
}

export function runAddonOAuthSetup(
  req: AddonOAuthWizardRequest,
): Promise<AddonOAuthWizardResponse> {
  return request(
    '/addons/oauth-setup',
    { method: 'POST', body: JSON.stringify(req) },
    { timeoutMs: 90_000 },
  )
}

export interface AddonPreviewCallRequest {
  spec: AddonSpec
  endpoint: string
  args: Record<string, unknown>
  auth_scope_id?: string
}

export interface AddonPreviewHTTP {
  method?: string
  url?: string
  headers: Record<string, string>
  body: string
}

export interface AddonPreviewCallResponse {
  request: AddonPreviewHTTP
  response: AddonPreviewHTTP
  status: number
  duration_ms: number
  note: string
}

export function previewAddonCall(
  req: AddonPreviewCallRequest,
): Promise<AddonPreviewCallResponse> {
  return request(
    '/addons/preview-call',
    { method: 'POST', body: JSON.stringify(req) },
    { timeoutMs: 90_000 },
  )
}
