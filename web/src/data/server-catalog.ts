// server-catalog.ts — frontend catalog types and helpers.
//
// The catalog is now fetched from GET /api/v1/catalog (served by the Go
// backend, which embeds the defaults or fetches from MCPLEXER_CATALOG_URL).
// This file provides the types, category metadata, and a converter from the
// flat API CatalogEntry to the nested-preset format the UI components expect.

import type { CatalogEntry as ApiCatalogEntry } from '@/api/types'

export type ServerCategory =
  | 'dev'
  | 'productivity'
  | 'data'
  | 'cloud'
  | 'observability'
  | 'search'
  | 'core'
  | 'comms'

export type ServerAuth = 'none' | 'api-key' | 'oauth' | 'config'

export interface CatalogPreset {
  transport: 'stdio' | 'http' | 'internal'
  command?: string
  args?: string[]
  url?: string | null
  tool_namespace: string
}

export interface CatalogRecipe {
  id: string
  label: string
  description: string
  scopes: string[]
}

export interface CatalogEntry {
  id: string
  name: string
  description: string
  category: ServerCategory
  tags: string[]
  auth: ServerAuth
  preset: CatalogPreset
  recipes?: CatalogRecipe[]
}

export const CATEGORY_LABELS: Record<ServerCategory, string> = {
  dev: 'Developer Tools',
  productivity: 'Productivity',
  data: 'Databases',
  cloud: 'Cloud & Infra',
  observability: 'Observability',
  search: 'Search & Web',
  core: 'Core / Filesystem',
  comms: 'Communication',
}

export const CATEGORY_ORDER: ServerCategory[] = [
  'dev',
  'productivity',
  'cloud',
  'observability',
  'data',
  'search',
  'comms',
  'core',
]

/** Convert a flat API CatalogEntry to the nested-preset format the UI expects. */
export function toCatalogEntry(api: ApiCatalogEntry): CatalogEntry {
  return {
    id: api.id,
    name: api.name,
    description: api.description,
    category: api.category as ServerCategory,
    tags: api.tags,
    auth: api.auth as ServerAuth,
    preset: {
      transport: api.transport,
      command: api.command || undefined,
      args: api.args ?? undefined,
      url: api.url ?? null,
      tool_namespace: api.tool_namespace,
    },
    recipes: api.recipes?.map((r) => ({
      id: r.id,
      label: r.label,
      description: r.description,
      scopes: r.scopes ?? [],
    })),
  }
}
