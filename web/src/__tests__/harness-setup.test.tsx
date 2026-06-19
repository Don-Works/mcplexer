import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { fireEvent } from '@testing-library/react'
import { BrowserRouter } from 'react-router-dom'
import { HarnessSetupPage } from '@/pages/HarnessSetupPage'
import type { HarnessSetupRow, HarnessSetupStatusResponse, MCPInstallStatus } from '@/api/types'

const FIXTURE_ROWS: HarnessSetupRow[] = [
  {
    key: 'claude',
    mcp_wired: true,
    config_path: '~/.claude/settings.json',
    last_initialize_at: new Date(Date.now() - 3600_000).toISOString(),
    client_info: 'claude-code 2.1',
    bootstrap_installed: true,
    bootstrap_version: 3,
    registry_version: 3,
    drifted: false,
  },
  {
    key: 'codex',
    mcp_wired: false,
    config_path: '~/.codex/config.json',
    last_initialize_at: null,
    client_info: null,
    bootstrap_installed: false,
    bootstrap_version: null,
    registry_version: 2,
    drifted: false,
  },
  {
    key: 'opencode',
    mcp_wired: true,
    config_path: '~/.config/opencode/opencode.json',
    last_initialize_at: new Date(Date.now() - 7200_000).toISOString(),
    client_info: 'opencode 1.0',
    bootstrap_installed: true,
    bootstrap_version: 2,
    registry_version: 3,
    drifted: true,
  },
  {
    key: 'gemini',
    mcp_wired: true,
    config_path: '~/.gemini/settings.json',
    last_initialize_at: new Date(Date.now() - 120_000).toISOString(),
    client_info: 'gemini-cli 0.1',
    bootstrap_installed: true,
    bootstrap_version: 1,
    registry_version: 1,
    drifted: false,
  },
  {
    key: 'grok',
    mcp_wired: true,
    config_path: '~/.grok/config.toml',
    last_initialize_at: null,
    client_info: null,
    bootstrap_installed: false,
    bootstrap_version: null,
    registry_version: 1,
    drifted: false,
  },
  {
    key: 'mimo',
    mcp_wired: false,
    config_path: '~/.config/mimocode/mimocode.json',
    last_initialize_at: null,
    client_info: null,
    bootstrap_installed: false,
    bootstrap_version: null,
    registry_version: 1,
    drifted: false,
  },
  {
    key: 'pi',
    mcp_wired: false,
    config_path: '~/.pi/AGENTS.md',
    last_initialize_at: null,
    client_info: null,
    bootstrap_installed: false,
    bootstrap_version: null,
    registry_version: 1,
    drifted: false,
  },
]

const FIXTURE_STATUS: HarnessSetupStatusResponse = {
  harnesses: FIXTURE_ROWS,
}

const FIXTURE_MCP_STATUS: MCPInstallStatus = {
  binary_path: '/usr/local/bin/mcplexer',
  server_entry: { command: 'mcplexer', args: ['connect'] },
  clients: [
    {
      id: 'claude_code',
      name: 'Claude Code',
      config_path: '~/.claude.json',
      detected: true,
      configured: true,
    },
    {
      id: 'codex',
      name: 'Codex',
      config_path: '~/.codex/mcp.json',
      detected: true,
      configured: false,
    },
    {
      id: 'opencode',
      name: 'OpenCode',
      config_path: '~/.config/opencode/opencode.json',
      detected: true,
      configured: true,
    },
    {
      id: 'gemini_cli',
      name: 'Gemini CLI',
      config_path: '~/.gemini/settings.json',
      detected: true,
      configured: true,
    },
    {
      id: 'grok',
      name: 'Grok CLI',
      config_path: '~/.grok/config.toml',
      detected: true,
      configured: true,
    },
    {
      id: 'mimocode',
      name: 'MiMoCode',
      config_path: '~/.config/mimocode/mimocode.json',
      detected: true,
      configured: false,
    },
    {
      id: 'cursor',
      name: 'Cursor',
      config_path: '~/.cursor/mcp.json',
      detected: true,
      configured: false,
    },
  ],
}

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    getHarnessSetupStatus: vi.fn(),
    getMCPInstallStatus: vi.fn(),
    installHarness: vi.fn(),
    installMCP: vi.fn(),
    previewMCPInstall: vi.fn(),
    recheckHarness: vi.fn(),
    uninstallMCP: vi.fn(),
  }
})

import * as client from '@/api/client'

const mockedGetStatus = vi.mocked(client.getHarnessSetupStatus)
const mockedGetMCPStatus = vi.mocked(client.getMCPInstallStatus)
const mockedInstall = vi.mocked(client.installHarness)
const mockedRecheck = vi.mocked(client.recheckHarness)

function renderPage() {
  return render(
    <BrowserRouter>
      <HarnessSetupPage />
    </BrowserRouter>,
  )
}

describe('HarnessSetupPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockedGetStatus.mockResolvedValue(FIXTURE_STATUS)
    mockedGetMCPStatus.mockResolvedValue(FIXTURE_MCP_STATUS)
  })

  it('renders loading skeletons then rows', async () => {
    renderPage()

    await waitFor(() => {
      expect(screen.getByTestId('harness-row-claude')).toBeInTheDocument()
    })
    expect(screen.getByTestId('harness-row-codex')).toBeInTheDocument()
    expect(screen.getByTestId('harness-row-opencode')).toBeInTheDocument()
    expect(screen.getByTestId('harness-row-gemini')).toBeInTheDocument()
    expect(screen.getByTestId('harness-row-grok')).toBeInTheDocument()
    expect(screen.getByTestId('harness-row-mimo')).toBeInTheDocument()
    expect(screen.getByTestId('harness-row-pi')).toBeInTheDocument()
  })

  it('shows explicit MCP server wiring state', async () => {
    renderPage()
    await waitFor(() => {
      expect(screen.getByTestId('harness-row-claude')).toBeInTheDocument()
    })

    const claudeRow = screen.getByTestId('harness-row-claude')
    expect(claudeRow.textContent).toContain('MCP server configured')
    expect(claudeRow.textContent).not.toContain('MCP server missing')

    const codexRow = screen.getByTestId('harness-row-codex')
    expect(codexRow.textContent).toContain('MCP server missing')
    expect(screen.getByTestId('harness-configure-mcp-codex')).toBeInTheDocument()
    expect(screen.queryByTestId('harness-configure-mcp-claude')).toBeNull()
  })

  it('shows Pi as native extension setup, not MCP server wiring', async () => {
    renderPage()
    await waitFor(() => {
      expect(screen.getByTestId('harness-row-pi')).toBeInTheDocument()
    })

    const piRow = screen.getByTestId('harness-row-pi')
    expect(piRow.textContent).toContain('Native extension')
    expect(piRow.textContent).not.toContain('MCP server missing')
    expect(screen.queryByTestId('harness-configure-mcp-pi')).toBeNull()
    expect(screen.getByTestId('harness-pi-docs-pi')).toBeInTheDocument()
  })

  it('shows explicit bootstrap skill state labels', async () => {
    renderPage()
    await waitFor(() => {
      expect(screen.getByTestId('harness-row-claude')).toBeInTheDocument()
    })

    expect(screen.getByTestId('harness-row-claude').textContent).toContain('Bootstrap v3 installed')
    expect(screen.getByTestId('harness-row-codex').textContent).toContain('Bootstrap missing')
    expect(screen.getByTestId('harness-row-opencode').textContent).toContain('Bootstrap v2 outdated')
  })

  it('shows explicit bootstrap actions for missing and installed states', async () => {
    renderPage()
    await waitFor(() => {
      expect(screen.getByTestId('harness-row-claude')).toBeInTheDocument()
    })

    expect(screen.getByTestId('harness-reinstall-claude')).toBeInTheDocument()
    expect(screen.getByTestId('harness-reinstall-claude').textContent).toContain('Reinstall Bootstrap')
    expect(screen.queryByTestId('harness-install-claude')).toBeNull()

    expect(screen.getByTestId('harness-install-codex')).toBeInTheDocument()
    expect(screen.getByTestId('harness-install-codex').textContent).toContain('Install Bootstrap')
    expect(screen.queryByTestId('harness-reinstall-codex')).toBeNull()

    // MCP wired but bootstrap missing still shows Install (decoupled from mcp_wired).
    expect(screen.getByTestId('harness-install-grok')).toBeInTheDocument()
    expect(screen.queryByTestId('harness-reinstall-grok')).toBeNull()
    expect(screen.getByTestId('harness-row-grok').textContent).toContain('MCP server configured')
  })

  it('shows Check Status button on every row', async () => {
    renderPage()
    await waitFor(() => {
      expect(screen.getByTestId('harness-row-claude')).toBeInTheDocument()
    })

    for (const key of ['claude', 'codex', 'opencode', 'gemini', 'grok', 'mimo', 'pi']) {
      expect(screen.getByTestId(`harness-recheck-${key}`)).toBeInTheDocument()
      expect(screen.getByTestId(`harness-recheck-${key}`).textContent).toContain('Check Status')
    }
  })

  it('calls installHarness on Install click and refetches', async () => {
    mockedInstall.mockResolvedValue(FIXTURE_ROWS[1])

    renderPage()
    await waitFor(() => {
      expect(screen.getByTestId('harness-install-codex')).toBeInTheDocument()
    })

    fireEvent.click(screen.getByTestId('harness-install-codex'))
    expect(mockedInstall).toHaveBeenCalledWith('codex')
    await waitFor(() => {
      expect(mockedGetStatus).toHaveBeenCalledTimes(2)
    })
  })

  it('calls installHarness on Re-install click', async () => {
    mockedInstall.mockResolvedValue(FIXTURE_ROWS[0])

    renderPage()
    await waitFor(() => {
      expect(screen.getByTestId('harness-reinstall-claude')).toBeInTheDocument()
    })

    fireEvent.click(screen.getByTestId('harness-reinstall-claude'))
    expect(mockedInstall).toHaveBeenCalledWith('claude')
  })

  it('calls recheckHarness on Re-check click', async () => {
    mockedRecheck.mockResolvedValue(FIXTURE_ROWS[0])

    renderPage()
    await waitFor(() => {
      expect(screen.getByTestId('harness-recheck-claude')).toBeInTheDocument()
    })

    fireEvent.click(screen.getByTestId('harness-recheck-claude'))
    expect(mockedRecheck).toHaveBeenCalledWith('claude')
  })

  it('shows error toast on API failure with structured error', async () => {
    const { ApiClientError } = await import('@/api/client')
    mockedInstall.mockRejectedValue(
      new ApiClientError(500, JSON.stringify({
        error: { code: 'install_failed', message: 'Config not writable', hint: 'Check permissions' },
      })),
    )

    renderPage()
    await waitFor(() => {
      expect(screen.getByTestId('harness-install-codex')).toBeInTheDocument()
    })

    fireEvent.click(screen.getByTestId('harness-install-codex'))
    await waitFor(() => {
      expect(mockedInstall).toHaveBeenCalled()
    })
  })
})

describe('HarnessSetupPage — empty state', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockedGetStatus.mockResolvedValue({ harnesses: [] })
    mockedGetMCPStatus.mockResolvedValue({ ...FIXTURE_MCP_STATUS, clients: [] })
  })

  it('renders without rows when no harnesses returned', async () => {
    renderPage()
    await waitFor(() => {
      expect(mockedGetStatus).toHaveBeenCalled()
    })
    expect(screen.queryByTestId(/harness-row-/)).toBeNull()
  })
})
