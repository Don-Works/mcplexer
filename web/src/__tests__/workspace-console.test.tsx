import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { TooltipProvider } from '@/components/ui/tooltip'
import { ConnectionsPage } from '@/pages/ConnectionsPage'

function response(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  })
}

const workspace = {
  id: 'ws-1',
  name: 'Product app',
  root_path: '/repo/product',
  default_policy: 'deny',
  tags: { team: 'product' },
  created_at: '',
  updated_at: '',
}

const server = {
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

const credential = {
  id: 'credential-1',
  name: 'github_work',
  display_name: 'GitHub work account',
  type: 'header',
  oauth_provider_id: '',
  has_secrets: false,
  redaction_hints: [],
  source: 'user',
  created_at: '',
  updated_at: '',
}

const rules = [
  {
    id: 'rule-src', name: 'Source access', priority: 200, workspace_id: workspace.id,
    path_glob: 'src/**', tool_match: ['github__*'], scope_policy: {},
    downstream_server_id: server.id, auth_scope_id: credential.id, policy: 'allow',
    log_level: 'info', approval_mode: 'write', approval_timeout: 300,
    created_at: '', updated_at: '',
  },
  {
    id: 'rule-docs', name: 'Docs access', priority: 100, workspace_id: workspace.id,
    path_glob: 'docs/**', tool_match: ['github__*'], scope_policy: {},
    downstream_server_id: server.id, auth_scope_id: '', policy: 'allow',
    log_level: 'info', approval_mode: 'none', approval_timeout: 300,
    created_at: '', updated_at: '',
  },
]

function mockFetch() {
  return vi.fn(async (input: string | URL | Request) => {
    const url = typeof input === 'string' ? input : input.toString()
    if (/\/workspaces(?:\?|$)/.test(url)) return response([workspace])
    if (/\/downstreams(?:\?|$)/.test(url)) return response([server])
    if (/\/routes(?:\?|$)/.test(url)) return response(rules)
    if (/\/auth-scopes(?:\?|$)/.test(url)) return response([credential])
    if (url.includes('/audit/query')) return response({ data: [], total: 0 })
    return response([])
  })
}

function renderPage() {
  return render(
    <TooltipProvider>
      <MemoryRouter initialEntries={['/workspaces?workspace=ws-1']}>
        <ConnectionsPage />
      </MemoryRouter>
    </TooltipProvider>,
  )
}

describe('unified workspace console', () => {
  beforeEach(() => {
    globalThis.fetch = mockFetch() as unknown as typeof fetch
  })

  it('keeps multi-rule access visible from the simple server row', async () => {
    renderPage()
    await screen.findByRole('heading', { name: 'Product app' })

    expect(screen.getByText('2 rules')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('connection-cell-github-ws-1'))

    expect(await screen.findByText('Edit access rule')).toBeInTheDocument()
    expect(screen.getByTestId('connection-drawer-rule-select')).toBeInTheDocument()
  })

  it('switches from access to inline settings without leaving the console', async () => {
    renderPage()
    await screen.findByRole('heading', { name: 'Product app' })
    fireEvent.click(screen.getByTestId('workspace-section-settings'))

    await waitFor(() => expect(screen.getByDisplayValue('/repo/product')).toBeInTheDocument())
    expect(screen.getByTestId('workspace-settings-panel')).toBeInTheDocument()
  })

  it('reveals every raw rule in the advanced access panel', async () => {
    renderPage()
    await screen.findByRole('heading', { name: 'Product app' })
    fireEvent.click(screen.getByRole('button', { name: /advanced rules/i }))

    expect(await screen.findByText('Source access')).toBeInTheDocument()
    expect(screen.getByText('Docs access')).toBeInTheDocument()
  })

  it('repairs a missing credential without leaving the access drawer', async () => {
    renderPage()
    await screen.findByRole('heading', { name: 'Product app' })
    fireEvent.click(screen.getByTestId('connection-cell-github-ws-1'))

    fireEvent.click(await screen.findByRole('button', { name: 'Add secret' }))
    fireEvent.change(screen.getByLabelText('Authorization'), { target: { value: 'fixture-token' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save credential' }))

    await waitFor(() => {
      const secretCall = vi.mocked(globalThis.fetch).mock.calls.find(([input]) =>
        String(input).includes('/auth-scopes/credential-1/secrets'),
      )
      expect(secretCall?.[1]).toMatchObject({ method: 'PUT' })
      expect(JSON.parse(String(secretCall?.[1]?.body))).toEqual({
        key: 'Authorization',
        value: 'fixture-token',
      })
    })
  })
})
