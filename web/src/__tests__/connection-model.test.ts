import { describe, expect, it } from 'vitest'
import type { AuthScope, DownstreamServer, RouteRule, Workspace } from '@/api/types'
import {
  buildWorkspaceRows,
  deriveConnectionCells,
  indexRoutes,
} from '@/components/connections/connection-model'

const workspace: Workspace = {
  id: 'ws-1',
  name: 'Product',
  root_path: '/repo/product',
  default_policy: 'deny',
  tags: {},
  created_at: '',
  updated_at: '',
}

const server: DownstreamServer = {
  id: 'github',
  name: 'GitHub',
  transport: 'http',
  command: '',
  args: [],
  url: 'https://example.test/mcp',
  tool_namespace: 'github',
  capabilities_cache: {},
  idle_timeout_sec: 300,
  max_instances: 1,
  restart_policy: 'on-failure',
  disabled: false,
  source: 'user',
  created_at: '',
  updated_at: '',
}

function rule(overrides: Partial<RouteRule>): RouteRule {
  return {
    id: 'rule-1',
    name: 'GitHub read',
    priority: 100,
    workspace_id: workspace.id,
    path_glob: '**',
    tool_match: ['github__*'],
    scope_policy: {},
    downstream_server_id: server.id,
    auth_scope_id: '',
    policy: 'allow',
    log_level: 'info',
    approval_mode: 'write',
    approval_timeout: 300,
    created_at: '',
    updated_at: '',
    ...overrides,
  }
}

function scope(overrides: Partial<AuthScope>): AuthScope {
  return {
    id: 'credential-1',
    name: 'github_token',
    display_name: 'GitHub token',
    type: 'header',
    oauth_provider_id: '',
    has_secrets: true,
    redaction_hints: [],
    source: 'user',
    created_at: '',
    updated_at: '',
    ...overrides,
  }
}

describe('workspace connection model', () => {
  it('keeps every rule for one server/workspace pair', () => {
    const rules = [
      rule({ id: 'rule-low', priority: 10, path_glob: 'docs/**' }),
      rule({ id: 'rule-high', priority: 200, path_glob: 'src/**' }),
    ]
    const index = indexRoutes(rules)
    const cells = deriveConnectionCells(index, [])
    const rows = buildWorkspaceRows(workspace, [server], index, cells, [])

    expect(rows[0].routes.map((item) => item.id)).toEqual(['rule-high', 'rule-low'])
    expect(rows[0].route?.id).toBe('rule-high')
    expect(rows[0].state.kind).toBe('connected')
  })

  it('surfaces a missing credential on any matching allow rule', () => {
    const rules = [
      rule({ id: 'usable', priority: 200 }),
      rule({ id: 'missing-auth', priority: 100, auth_scope_id: 'credential-1' }),
    ]
    const index = indexRoutes(rules)
    const cells = deriveConnectionCells(index, [scope({ has_secrets: false })])
    const rows = buildWorkspaceRows(workspace, [server], index, cells, [scope({ has_secrets: false })])

    expect(rows[0].state.kind).toBe('needs-auth')
    expect(rows[0].route?.id).toBe('missing-auth')
  })

  it('treats deny-only server rules as disabled without dropping them', () => {
    const rules = [rule({ id: 'deny', policy: 'deny' })]
    const index = indexRoutes(rules)
    const cells = deriveConnectionCells(index, [])
    const rows = buildWorkspaceRows(workspace, [server], index, cells, [])

    expect(rows[0].routes).toHaveLength(1)
    expect(rows[0].state.kind).toBe('disabled')
  })

  it('leaves global deny rules for the advanced workspace rule view', () => {
    const globalDeny = rule({ id: 'global-deny', downstream_server_id: '', policy: 'deny' })
    expect(indexRoutes([globalDeny]).size).toBe(0)
  })
})
