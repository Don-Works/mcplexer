// HammerspoonPanel — render + happy-path interaction tests.
//
// The panel is the user-facing surface for the three hammerspoon endpoints
// (snippet/install/probe) plus the exec_lua toggle. These tests stub the
// shared api/client module so we never touch the network, and assert that:
//   - the cached CapabilitiesCache.health controls the badge,
//   - clicking "Probe now" calls the API and re-renders the checks list,
//   - clicking "Install bridge" calls the API + toasts,
//   - the regenerate-password confirmation is required before the rotate fires.
//
// We don't mock toast assertions tightly — sonner renders to a portal that
// jsdom finds painful — but we assert call shape on the API stubs, which is
// the unambiguous signal of correctness.
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

import { HammerspoonPanel, HealthBadge } from '@/pages/config/HammerspoonPanel'
import type { DownstreamServer } from '@/api/types'
import * as apiClient from '@/api/client'

const fakeServer: DownstreamServer = {
  id: 'hammerspoon',
  name: 'Hammerspoon (macOS automation)',
  transport: 'internal',
  command: '',
  args: [],
  url: null,
  tool_namespace: 'hammerspoon',
  capabilities_cache: {
    health: 'degraded',
    checks: {
      app_running: { ok: true, duration_ms: 12 },
      bridge_reachable: { ok: true, duration_ms: 4 },
      auth_ok: { ok: true, duration_ms: 28, detail: 'accessibility=false' },
      accessibility: { ok: false, duration_ms: 0, detail: 'skipped' },
      smoke: { ok: false, duration_ms: 0, detail: 'skipped (bridge/auth not ready)' },
    },
    probed_at: '2026-05-25T12:00:00Z',
  },
  idle_timeout_sec: 300,
  max_instances: 1,
  restart_policy: 'on-failure',
  disabled: false,
  source: 'default',
  created_at: '2026-05-25T11:00:00Z',
  updated_at: '2026-05-25T12:00:00Z',
}

describe('HealthBadge', () => {
  it('renders the "Healthy" pill for health=ok', () => {
    render(<HealthBadge health="ok" />)
    expect(screen.getByTestId('hammerspoon-health-ok')).toHaveTextContent('Healthy')
  })
  it('renders the "Degraded" pill for health=degraded', () => {
    render(<HealthBadge health="degraded" />)
    expect(screen.getByTestId('hammerspoon-health-degraded')).toHaveTextContent('Degraded')
  })
  it('renders the "Broken" pill for health=broken', () => {
    render(<HealthBadge health="broken" />)
    expect(screen.getByTestId('hammerspoon-health-broken')).toHaveTextContent('Broken')
  })
  it('renders the grey "Not probed" pill when health is null', () => {
    render(<HealthBadge health={null} />)
    expect(screen.getByTestId('hammerspoon-health-unknown')).toHaveTextContent('Not probed')
  })
})

describe('HammerspoonPanel', () => {
  beforeEach(() => {
    // listSecretKeys is fired by useEffect on open — return empty so the
    // allow-exec checkbox starts unchecked. Tests that need it checked
    // override this.
    vi.spyOn(apiClient, 'listSecretKeys').mockResolvedValue({ keys: [] })
  })

  it('renders cached probe checks from the server row without auto-probing', async () => {
    const probeSpy = vi.spyOn(apiClient, 'probeHammerspoon')
    render(<HammerspoonPanel open={true} onClose={() => {}} server={fakeServer} />)

    // Initial render shows the cached degraded badge.
    expect(screen.getAllByTestId('hammerspoon-health-degraded')).not.toHaveLength(0)

    // All five checks are rendered in the canonical order, from cache.
    await waitFor(() => {
      expect(screen.getByTestId('hammerspoon-check-app_running')).toBeInTheDocument()
    })
    expect(screen.getByTestId('hammerspoon-check-bridge_reachable')).toBeInTheDocument()
    expect(screen.getByTestId('hammerspoon-check-auth_ok')).toBeInTheDocument()
    expect(screen.getByTestId('hammerspoon-check-accessibility')).toBeInTheDocument()
    expect(screen.getByTestId('hammerspoon-check-smoke')).toBeInTheDocument()

    // The probe endpoint was not called on mount.
    expect(probeSpy).not.toHaveBeenCalled()
  })

  it('clicks "Probe now" → calls probeHammerspoon + replaces the checks list', async () => {
    vi.spyOn(apiClient, 'probeHammerspoon').mockResolvedValue({
      health: 'ok',
      probed_at: '2026-05-25T12:05:00Z',
      checks: {
        app_running: { ok: true, duration_ms: 9 },
        bridge_reachable: { ok: true, duration_ms: 3 },
        auth_ok: { ok: true, duration_ms: 14, detail: 'accessibility=true' },
        accessibility: { ok: true, duration_ms: 0 },
        smoke: { ok: true, duration_ms: 23, detail: '7 windows' },
      },
      remediation: [],
    })

    render(<HammerspoonPanel open={true} onClose={() => {}} server={fakeServer} />)
    fireEvent.click(screen.getByTestId('hammerspoon-probe'))

    await waitFor(() => {
      // After the probe resolves, all checks go green.
      const smoke = screen.getByTestId('hammerspoon-check-smoke')
      expect(smoke).toHaveTextContent('7 windows')
    })
    expect(apiClient.probeHammerspoon).toHaveBeenCalledTimes(1)
  })

  it('clicks "Install bridge" → calls installHammerspoon', async () => {
    vi.spyOn(apiClient, 'installHammerspoon').mockResolvedValue({
      ok: true,
      files_written: ['~/.hammerspoon/hammerspoon-mcp.lua', '~/.hammerspoon/.mcp-password'],
      init_lua_modified: true,
      init_lua_backup: 'init.lua.mcplexer-bak.20260525T120000Z',
      reload_attempted: true,
      next_steps: [],
    })

    render(<HammerspoonPanel open={true} onClose={() => {}} server={fakeServer} />)
    fireEvent.click(screen.getByTestId('hammerspoon-install'))

    await waitFor(() => {
      expect(apiClient.installHammerspoon).toHaveBeenCalledTimes(1)
    })
  })

  it('clicks "Regenerate password" → confirm → calls installHammerspoon once confirmed', async () => {
    const installSpy = vi
      .spyOn(apiClient, 'installHammerspoon')
      .mockResolvedValue({
        ok: true,
        files_written: ['~/.hammerspoon/.mcp-password'],
        init_lua_modified: false,
        reload_attempted: true,
        next_steps: [],
      })

    render(<HammerspoonPanel open={true} onClose={() => {}} server={fakeServer} />)
    fireEvent.click(screen.getByTestId('hammerspoon-rotate'))

    // The confirm dialog renders into a Radix portal — find the action via
    // its text rather than testid, which the AlertDialog primitive doesn't
    // expose.
    const confirmButton = await screen.findByRole('button', { name: 'Regenerate' })
    fireEvent.click(confirmButton)

    await waitFor(() => {
      expect(installSpy).toHaveBeenCalledTimes(1)
    })
  })

  it('renders the "Not probed" pill when the server has no cache.health', async () => {
    const blank = { ...fakeServer, capabilities_cache: {} }
    render(<HammerspoonPanel open={true} onClose={() => {}} server={blank} />)
    await waitFor(() => {
      expect(screen.getAllByTestId('hammerspoon-health-unknown').length).toBeGreaterThan(0)
    })
    expect(screen.getByTestId('hammerspoon-no-probe')).toBeInTheDocument()
  })
})
