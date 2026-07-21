import type {
  CompressionStatsResponse,
  MCPClient,
  MCPInstallPreview,
  MCPInstallStatus,
  Settings,
  SettingsResponse,
} from './types'
import { request } from './transport'

// MCP Install
export function getMCPInstallStatus(): Promise<MCPInstallStatus> {
  return request('/mcp-install/status')
}

export function installMCP(clientId: string): Promise<MCPClient> {
  return request(`/mcp-install/${clientId}/install`, { method: 'POST' })
}

export function uninstallMCP(clientId: string): Promise<MCPClient> {
  return request(`/mcp-install/${clientId}/uninstall`, { method: 'POST' })
}

export function previewMCPInstall(clientId: string): Promise<MCPInstallPreview> {
  return request(`/mcp-install/${clientId}/preview`)
}

// Cache / reload
export interface CacheStats {
  hits: number
  misses: number
  evictions: number
  entries: number
  hit_rate: number
}

export interface CacheStatsResponse {
  tool_call: CacheStats
  route_resolution: CacheStats
}

export function getCacheStats(): Promise<CacheStatsResponse> {
  return request('/cache/stats')
}

export function flushCache(layer: 'tool_call' | 'route' | 'all' = 'all'): Promise<{ status: string }> {
  return request('/cache/flush', {
    method: 'POST',
    body: JSON.stringify({ layer }),
  })
}

// Health / System
export interface SystemInfo {
  mode: string
  version: string
  http_addr?: string
  public_url?: string
  socket_path?: string
  data_dir?: string
  config_file?: string
  log_path?: string
  addons_dir?: string
  p2p_enabled: boolean
  server_profile?: string
  trusted_hosts?: string[]
  capabilities?: Record<string, boolean>
}

export interface HealthResponse {
  status: string
  version: string
  uptime_seconds: number
  mode: string
  system: SystemInfo
}

export function getHealth(init?: RequestInit): Promise<HealthResponse> {
  return request('/health', init)
}

export type SystemRevealTarget = 'data_dir' | 'config_file' | 'log_path' | 'addons_dir'

export function revealSystemPath(target: SystemRevealTarget): Promise<void> {
  return request('/system/reveal', {
    method: 'POST',
    body: JSON.stringify({ target }),
  })
}

export type SystemTerminalTarget = 'data_dir' | 'addons_dir'

// Open a terminal window with cwd set to one of the daemon's known paths
// (data_dir by default). Used by the "Configure with AI" CTA — the agent
// running in that terminal then drives mcplexer's MCP tools to configure it.
export function launchSystemTerminal(target: SystemTerminalTarget = 'data_dir'): Promise<void> {
  return request('/system/launch-terminal', {
    method: 'POST',
    body: JSON.stringify({ target }),
  })
}

// Settings
export function getSettings(): Promise<SettingsResponse> {
  return request('/settings')
}

export function updateSettings(data: Settings): Promise<SettingsResponse> {
  return request('/settings', {
    method: 'PUT',
    body: JSON.stringify(data),
  })
}

// Token compression
export function getCompressionStats(days = 30): Promise<CompressionStatsResponse> {
  return request(`/compression/stats?days=${days}`)
}
