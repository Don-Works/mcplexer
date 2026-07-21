import { request } from './transport'

// Skills Registry — agent-facing search/publish/versioning for SKILL.md docs.
// MCP equivalents (mcpx__skill_search/get/publish/list, plus admin tools)
// are the canonical surface for agents; this is the human's view.
export interface SkillRegistryEntry {
  id: string
  name: string
  version: number
  content_hash: string
  description: string
  body: string
  metadata?: Record<string, unknown>
  tags?: string[]
  author?: string
  parent_version?: number
  deleted_at?: string | null
  published_at: string
  created_by_agent_id?: string
  workspace_id?: string | null
  source_type?: 'inline' | 'path' | 'bundle' | 'git'
  source_path?: string
  bundle_sha256?: string
}

export interface SkillSearchHit {
  name: string
  version: number
  description: string
  score: number
  scope?: string
}

export interface PublishSkillResult {
  name: string
  version: number
  content_hash: string
  action: 'created' | 'deduped'
}

export type SkillScopeFilter =
  | { mode: 'all' }
  | { mode: 'global' }
  | { mode: 'workspace'; workspaceId: string }

function skillScopeQuery(s?: SkillScopeFilter): string {
  if (!s || s.mode === 'all') return ''
  if (s.mode === 'global') return '?scope=global'
  return `?scope=workspace&workspace_id=${encodeURIComponent(s.workspaceId)}`
}

export function listSkillRegistry(scope?: SkillScopeFilter): Promise<SkillRegistryEntry[]> {
  return request(`/skill-registry${skillScopeQuery(scope)}`)
}

export function searchSkillRegistry(
  q: string,
  limit = 10,
): Promise<SkillSearchHit[]> {
  const params = new URLSearchParams({ q, limit: String(limit) })
  return request(`/skill-registry/search?${params.toString()}`)
}

export function getSkillRegistryEntry(
  name: string,
  version?: number | 'latest' | 'stable',
): Promise<SkillRegistryEntry> {
  const params = new URLSearchParams()
  if (version != null && version !== 'latest') params.set('version', String(version))
  const qs = params.toString()
  return request(`/skill-registry/${encodeURIComponent(name)}${qs ? '?' + qs : ''}`)
}

export function listSkillRegistryVersions(name: string): Promise<SkillRegistryEntry[]> {
  return request(`/skill-registry/${encodeURIComponent(name)}/versions`)
}

export interface SkillVersionDiff {
  name: string
  old_version: number
  new_version: number
  body_diff?: string
  frontmatter_diff?: string
  tree?: Array<{
    path: string
    old_sha?: string
    new_sha?: string
    status: string
  }>
  old_has_bundle: boolean
  new_has_bundle: boolean
}

export function getSkillRegistryVersionDiff(
  name: string,
  oldVersion?: number | 'latest',
  newVersion?: number | 'latest',
): Promise<SkillVersionDiff> {
  const params = new URLSearchParams()
  if (oldVersion != null) params.set('old_version', String(oldVersion))
  if (newVersion != null) params.set('new_version', String(newVersion))
  const qs = params.toString()
  return request(
    `/skill-registry/${encodeURIComponent(name)}/diff${qs ? `?${qs}` : ''}`,
  )
}

export function publishSkillRegistry(opts: {
  name: string
  body: string
  parent_version?: number
  author?: string
  scope?: 'global' | 'workspace'
  workspace_id?: string
}): Promise<PublishSkillResult> {
  return request('/skill-registry', {
    method: 'POST',
    body: JSON.stringify(opts),
  })
}

export function deleteSkillRegistry(name: string, version?: number): Promise<void> {
  const qs = version ? `?version=${version}` : ''
  return request(`/skill-registry/${encodeURIComponent(name)}${qs}`, {
    method: 'DELETE',
  })
}

export function setSkillRegistryTag(
  name: string,
  tag: string,
  version: number,
): Promise<void> {
  return request(`/skill-registry/${encodeURIComponent(name)}/tags`, {
    method: 'POST',
    body: JSON.stringify({ tag, version }),
  })
}

// Local-skills migration (W5). Walks a local directory of agentskills.io
// SKILL.md folders (default ~/.claude/skills/) and classifies each row
// against the registry: 'new' (will publish), 'duplicate' (same hash —
// nothing to do), 'version-conflict' (different hash — overwrite to bump
// the version), or 'unparseable' (bad frontmatter; surface the error).
export type LocalSkillStatus = 'new' | 'duplicate' | 'version-conflict' | 'unparseable'

export interface LocalSkill {
  dir: string
  path: string
  name: string
  description: string
  content_hash: string
  status: LocalSkillStatus
  registry_version?: number
  parse_error?: string
}

export interface LocalUnpublishedResponse {
  path: string
  skills: LocalSkill[]
}

export type LocalSkillImportAction =
  | 'imported'
  | 'skipped'
  | 'updated'
  | 'failed'

export interface LocalSkillImportResult {
  name: string
  dir: string
  path: string
  action: LocalSkillImportAction
  version?: number
  bundle_sha256?: string
  archived_to?: string
  error?: string
  dry_run?: boolean
}

export function listLocalUnpublishedSkills(
  source?: string,
): Promise<LocalUnpublishedResponse> {
  const params = new URLSearchParams()
  if (source) params.set('source', source)
  const qs = params.toString()
  return request(`/skills/local-unpublished${qs ? '?' + qs : ''}`)
}

export function importLocalSkill(req: {
  name: string
  source_dir: string
  overwrite?: boolean
}): Promise<LocalSkillImportResult> {
  return request('/skills/import', {
    method: 'POST',
    body: JSON.stringify(req),
  })
}
