// dangerous-mode — provider + toggle + chrome wash.
//
// Walks the happy path the user sees on the dashboard:
//   1. mount the provider, get the toggle (off, neutral styling),
//   2. click → confirmation modal opens,
//   3. confirm → settings PUT fires, toggle flips on, banner +
//      viewport frame appear, ARIA + data-state reflect "on".
//   4. click again → no confirm (off has no friction), settings PUT
//      fires with dangerous_mode_enabled=false.
//
// We stub the api/client module so no network ever fires. Toast +
// optimistic UI are exercised by the same calls — assertion shape
// focuses on the visible state transitions.

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

import {
  DangerousModeBanner,
  DangerousModeProvider,
  DangerousModeToggle,
  DangerousModeViewportFrame,
} from '@/components/layout/dangerous-mode'
import * as apiClient from '@/api/client'
import type { Settings, SettingsResponse } from '@/api/types'

const baseSettings: Settings = {
  slim_tools: true,
  slim_surface: true,
  compact_responses: true,
  tools_cache_ttl_sec: 15,
  log_level: 'info',
  code_mode_timeout_sec: 30,
  code_mode_max_output_bytes: 24 * 1024,
  code_mode_max_heap_growth_mb: 2048,
  mesh_enabled: false,
  mesh_receive_max_results: 20,
  mesh_receive_preview_bytes: 512,
  mesh_send_max_content_bytes: 64 * 1024,
  display_name: 'test-host',
  description_refinement_mode: 'manual',
  tool_description_overrides: {},
  dangerous_mode_enabled: false,
  auto_update_bootstrap: true,
}

function makeResponse(overrides?: Partial<Settings>): SettingsResponse {
  return {
    settings: { ...baseSettings, ...overrides },
    builtin_tool_defaults: {},
  }
}

function Harness() {
  return (
    <DangerousModeProvider>
      <DangerousModeToggle />
      <DangerousModeBanner />
      <DangerousModeViewportFrame />
    </DangerousModeProvider>
  )
}

describe('dangerous-mode', () => {
  beforeEach(() => {
    vi.spyOn(apiClient, 'getSettings').mockResolvedValue(makeResponse())
    vi.spyOn(apiClient, 'updateSettings').mockImplementation(
      async (next: Settings) => makeResponse({ dangerous_mode_enabled: next.dangerous_mode_enabled }),
    )
  })

  it('renders the toggle in off state and hides chrome wash', async () => {
    render(<Harness />)
    const toggle = await screen.findByTestId('dangerous-mode-toggle')
    expect(toggle).toHaveAttribute('aria-checked', 'false')
    expect(toggle).toHaveAttribute('data-state', 'off')
    expect(toggle).toHaveTextContent(/dangerous mode/i)
    expect(toggle).toHaveTextContent(/off/i)
    expect(screen.queryByTestId('dangerous-mode-banner')).toBeNull()
    expect(screen.queryByTestId('dangerous-mode-frame')).toBeNull()
  })

  it('turning ON opens the confirm modal and applies chrome on confirm', async () => {
    render(<Harness />)
    const toggle = await screen.findByTestId('dangerous-mode-toggle')

    fireEvent.click(toggle)
    // Confirm dialog should appear with destructive copy.
    const confirmBtn = await screen.findByRole('button', { name: /enable dangerous mode$/i })
    expect(confirmBtn).toBeInTheDocument()
    // Sanity: PUT must NOT have fired yet — we're waiting on the user.
    expect(apiClient.updateSettings).not.toHaveBeenCalled()

    fireEvent.click(confirmBtn)

    await waitFor(() => {
      expect(apiClient.updateSettings).toHaveBeenCalledTimes(1)
    })
    const sent = (apiClient.updateSettings as ReturnType<typeof vi.fn>).mock.calls[0][0]
    expect(sent.dangerous_mode_enabled).toBe(true)

    // Chrome flips ON.
    await waitFor(() => {
      expect(screen.getByTestId('dangerous-mode-toggle')).toHaveAttribute('data-state', 'on')
    })
    expect(screen.getByTestId('dangerous-mode-toggle')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByTestId('dangerous-mode-banner')).toBeInTheDocument()
    expect(screen.getByTestId('dangerous-mode-frame')).toBeInTheDocument()
  })

  it('turning OFF is one-click — no modal, immediate PUT', async () => {
    vi.spyOn(apiClient, 'getSettings').mockResolvedValue(
      makeResponse({ dangerous_mode_enabled: true }),
    )
    render(<Harness />)
    const toggle = await screen.findByTestId('dangerous-mode-toggle')
    await waitFor(() => expect(toggle).toHaveAttribute('aria-checked', 'true'))

    fireEvent.click(toggle)

    // No modal should be open — disabling has no friction.
    expect(screen.queryByRole('button', { name: /enable dangerous mode$/i })).toBeNull()
    await waitFor(() => {
      expect(apiClient.updateSettings).toHaveBeenCalledTimes(1)
    })
    const sent = (apiClient.updateSettings as ReturnType<typeof vi.fn>).mock.calls[0][0]
    expect(sent.dangerous_mode_enabled).toBe(false)

    await waitFor(() => {
      expect(screen.getByTestId('dangerous-mode-toggle')).toHaveAttribute('data-state', 'off')
    })
    expect(screen.queryByTestId('dangerous-mode-banner')).toBeNull()
    expect(screen.queryByTestId('dangerous-mode-frame')).toBeNull()
  })

  it('cancelling the confirm modal leaves the toggle off', async () => {
    render(<Harness />)
    const toggle = await screen.findByTestId('dangerous-mode-toggle')

    fireEvent.click(toggle)
    const cancelBtn = await screen.findByRole('button', { name: /keep approvals on/i })
    fireEvent.click(cancelBtn)

    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /enable dangerous mode$/i })).toBeNull()
    })
    expect(apiClient.updateSettings).not.toHaveBeenCalled()
    expect(screen.getByTestId('dangerous-mode-toggle')).toHaveAttribute('aria-checked', 'false')
  })
})
