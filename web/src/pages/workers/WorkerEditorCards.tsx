// Editor cards — the visual sections of WorkerEditorPage broken out so
// the page itself fits the 300-line budget. Each card is a pure
// presentational component that takes a slice of EditorState plus a
// setter. The editor page owns the actual state.
//
// Execution + Output cards live in WorkerEditorExecCard.tsx; Row +
// RadioRow primitives live in WorkerEditorFormBits.tsx. The tool-
// selector + skill autocomplete data plumbing lives in
// WorkerEditorPage.tsx, which fetches /api/v1/tools and the skill
// registry once on mount and passes the rows through.

import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import type { Workspace, AuthScope } from '@/api/types'
import type { SkillRegistryEntry } from '@/api/client'
import type { EditorState } from './worker-editor-state'
import { EnableSwitch } from './WorkersListPage'
import { Row } from './WorkerEditorFormBits'
import { useState } from 'react'
import { Plus } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { WorkerSkillRefsField } from './WorkerSkillRefsField'
import { WorkerScheduleField } from './WorkerScheduleField'
import { WorkerModelField } from './WorkerModelField'
import { InlineAddSecretDialog } from './InlineAddSecretDialog'
import { normalizeWorkspaceAccess } from './worker-editor-state'
import type { WorkerWorkspaceAccess } from '@/api/workers'

type Setter = <K extends keyof EditorState>(key: K, value: EditorState[K]) => void

interface BasicsProps {
  state: EditorState
  set: Setter
  workspaces: Workspace[] | null
}

export function BasicsCard({ state, set, workspaces }: BasicsProps) {
  const setPreferredWorkspace = (workspaceID: string) => {
    set('workspaceID', workspaceID)
    set('workspaceAccess', normalizeWorkspaceAccess(workspaceID, state.workspaceAccess))
  }
  const setWorkspaceGrant = (workspaceID: string, enabled: boolean) => {
    const current = state.workspaceAccess.filter((g) => g.workspace_id !== workspaceID)
    const next = enabled
      ? [...current, { workspace_id: workspaceID, access: 'read' as const }]
      : current
    set('workspaceAccess', normalizeWorkspaceAccess(state.workspaceID, next))
  }
  const setWorkspaceAccessMode = (
    workspaceID: string,
    access: WorkerWorkspaceAccess['access'],
  ) => {
    const current = state.workspaceAccess.filter((g) => g.workspace_id !== workspaceID)
    set(
      'workspaceAccess',
      normalizeWorkspaceAccess(state.workspaceID, [
        ...current,
        { workspace_id: workspaceID, access },
      ]),
    )
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Basics</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <Row label="Name" htmlFor="w-name" required>
          <Input
            id="w-name"
            value={state.name}
            onChange={(e) => set('name', e.target.value)}
            placeholder="hourly-digest"
            data-testid="worker-name"
          />
        </Row>
        <Row label="Description" htmlFor="w-desc">
          <Textarea
            id="w-desc"
            rows={2}
            value={state.description}
            onChange={(e) => set('description', e.target.value)}
            placeholder="Summarises new mesh messages every hour."
          />
        </Row>
        <Row label="Preferred workspace" htmlFor="w-workspace" required>
          <Select value={state.workspaceID} onValueChange={setPreferredWorkspace}>
            <SelectTrigger id="w-workspace" className="w-full">
              <SelectValue placeholder="Pick a workspace" />
            </SelectTrigger>
            <SelectContent>
              {workspaces?.map((ws) => (
                <SelectItem key={ws.id} value={ws.id}>
                  {ws.name} <span className="text-muted-foreground/60">({ws.id})</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {workspaces && workspaces.length > 1 && (
            <p className="text-[10px] text-muted-foreground/70">
              Defaulted to your first workspace — change above if this worker belongs elsewhere.
            </p>
          )}
        </Row>
        <Row label="Workspace access" htmlFor="w-workspace-access">
          <div id="w-workspace-access" className="space-y-1.5">
            {workspaces?.map((ws) => {
              const primary = ws.id === state.workspaceID
              const grant = state.workspaceAccess.find((g) => g.workspace_id === ws.id)
              const checked = primary || Boolean(grant)
              const access = primary ? 'write' : grant?.access ?? 'read'
              return (
                <div
                  key={ws.id}
                  className="grid grid-cols-[auto_1fr_112px] items-center gap-2 border border-border/50 px-2 py-1.5 text-xs"
                >
                  <Checkbox
                    id={`w-access-${ws.id}`}
                    checked={checked}
                    disabled={primary}
                    onCheckedChange={(v) => setWorkspaceGrant(ws.id, v === true)}
                  />
                  <label htmlFor={`w-access-${ws.id}`} className="min-w-0">
                    <span className="block truncate text-foreground">{ws.name}</span>
                    <span className="block truncate font-mono text-[10px] text-muted-foreground/70">
                      {ws.id}
                    </span>
                  </label>
                  <Select
                    value={access}
                    disabled={!checked || primary}
                    onValueChange={(v) =>
                      setWorkspaceAccessMode(ws.id, v as WorkerWorkspaceAccess['access'])
                    }
                  >
                    <SelectTrigger className="h-8 w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="read">Read</SelectItem>
                      <SelectItem value="write">Write</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              )
            })}
          </div>
        </Row>
        <Row label="Enabled" htmlFor="w-enabled">
          <div className="flex items-center gap-2">
            <EnableSwitch
              enabled={state.enabled}
              busy={false}
              onToggle={() => set('enabled', !state.enabled)}
            />
            <span className="text-xs text-muted-foreground">
              {state.enabled
                ? 'Scheduler will dispatch this worker.'
                : 'Paused — scheduler skips it.'}
            </span>
          </div>
        </Row>
      </CardContent>
    </Card>
  )
}

interface ModelProps {
  state: EditorState
  set: Setter
  authScopes: AuthScope[] | null
  // onSecretCreated lets the editor refresh its scope list after a new
  // scope is created inline. Called with the new scope id so the editor
  // can immediately select it without waiting on a list re-fetch.
  onSecretCreated?: (scopeID: string) => void
}

export function ModelCard({ state, set, authScopes, onSecretCreated }: ModelProps) {
  const [addingSecret, setAddingSecret] = useState(false)
  const hasScopes = (authScopes?.length ?? 0) > 0
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Model</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <WorkerModelField
          provider={state.provider}
          modelID={state.modelID}
          endpointURL={state.endpointURL}
          onChange={({ provider, modelID, endpointURL, secretScopeID }) => {
            set('provider', provider)
            set('modelID', modelID)
            set('endpointURL', endpointURL)
            if (secretScopeID !== undefined) set('secretScopeID', secretScopeID)
          }}
        />
        <Row label="Secret scope" htmlFor="w-scope" required>
          <div className="flex items-center gap-2">
            <Select
              value={state.secretScopeID}
              onValueChange={(v) => set('secretScopeID', v)}
            >
              <SelectTrigger id="w-scope" className="w-full flex-1">
                <SelectValue
                  placeholder={
                    hasScopes
                      ? 'Auth scope holding the API key'
                      : 'No scopes yet — click "+ Paste new key"'
                  }
                />
              </SelectTrigger>
              <SelectContent>
                {authScopes?.map((s) => (
                  <SelectItem key={s.id} value={s.id}>
                    {s.id}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-9 shrink-0 gap-1.5"
              onClick={() => setAddingSecret(true)}
              data-testid="add-secret-inline"
            >
              <Plus className="h-3.5 w-3.5" />
              Paste new key
            </Button>
          </div>
          <p className="text-[10px] text-muted-foreground/70">
            Stores the key as <code className="font-mono">api_key</code> in an
            age-encrypted auth scope — works for Anthropic, OpenAI, OpenRouter,
            Minimax, and any OpenAI-compatible endpoint.
          </p>
        </Row>
        <InlineAddSecretDialog
          open={addingSecret}
          onOpenChange={setAddingSecret}
          defaultName={defaultScopeNameForProvider(state.provider)}
          onCreated={(scopeID) => {
            set('secretScopeID', scopeID)
            onSecretCreated?.(scopeID)
          }}
        />
      </CardContent>
    </Card>
  )
}

function defaultScopeNameForProvider(provider: string): string {
  switch (provider) {
    case 'anthropic':
      return 'Anthropic'
    case 'openai':
      return 'OpenAI'
    case 'openai_compat':
      return 'OpenAI-compatible'
    case 'claude_cli':
      return 'Claude CLI'
    case 'opencode_cli':
      return 'OpenCode CLI'
    case 'grok_cli':
      return 'xAI Grok CLI'
    case 'mimo_cli':
      return 'Xiaomi MiMo CLI'
    default:
      return ''
  }
}

interface SkillCardProps {
  state: EditorState
  set: Setter
  skills: SkillRegistryEntry[] | null
}

export function SkillCard({ state, set, skills }: SkillCardProps) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Skills (optional)</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <p className="text-xs text-muted-foreground">
          Attach one or more Skills — their bodies are joined in order and
          prepended to the rendered prompt as the system context.
        </p>
        <WorkerSkillRefsField
          refs={state.skillRefs}
          onChange={(next) => set('skillRefs', next)}
          skills={skills}
        />
        <details className="text-xs">
          <summary className="cursor-pointer text-muted-foreground hover:text-foreground">
            Advanced
          </summary>
          <div className="mt-2.5">
            <Row label="Memory scope" htmlFor="w-memory">
              <Input
                id="w-memory"
                value={state.memoryScopeID}
                onChange={(e) => set('memoryScopeID', e.target.value)}
                placeholder="(none)"
                autoComplete="off"
              />
              <p className="text-[10px] text-muted-foreground/70">
                Placeholder for the memory subsystem (not active yet).
              </p>
            </Row>
          </div>
        </details>
      </CardContent>
    </Card>
  )
}

export function PromptCard({ state, set }: { state: EditorState; set: Setter }) {
  const parseError = parametersParseError(state.parametersJSON)
  const handleParamsBlur = () => {
    const t = state.parametersJSON.trim()
    if (!t) return
    try {
      const parsed = JSON.parse(t)
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
        set('parametersJSON', JSON.stringify(parsed, null, 2))
      }
    } catch {
      // leave as-is — inline error shows below
    }
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Prompt &amp; parameters</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <Row label="Prompt template" htmlFor="w-prompt" required>
          <Textarea
            id="w-prompt"
            rows={8}
            value={state.promptTemplate}
            onChange={(e) => set('promptTemplate', e.target.value)}
            placeholder="Summarise the {since} window of mesh activity. Highlight critical signals."
            className="font-mono text-xs"
          />
          <p className="text-[10px] text-muted-foreground/70">
            Use <code>{'{placeholder}'}</code> tokens; they're rendered against the
            parameters JSON at run-time.
          </p>
        </Row>
        <Row label="Parameters (JSON)" htmlFor="w-params">
          <Textarea
            id="w-params"
            rows={4}
            value={state.parametersJSON}
            onChange={(e) => set('parametersJSON', e.target.value)}
            onBlur={handleParamsBlur}
            placeholder='{"since":"1h"}'
            className={
              'font-mono text-xs' +
              (parseError ? ' border-destructive/60 focus-visible:ring-destructive/40' : '')
            }
          />
          {parseError ? (
            <p className="text-[10px] text-destructive">{parseError}</p>
          ) : (
            <p className="text-[10px] text-muted-foreground/70">
              JSON object. Auto-formats on blur. Empty <code>{'{}'}</code> is fine.
            </p>
          )}
        </Row>
      </CardContent>
    </Card>
  )
}

function parametersParseError(raw: string): string | null {
  const t = raw.trim()
  if (!t) return null
  try {
    const v = JSON.parse(t)
    if (v === null || typeof v !== 'object' || Array.isArray(v)) {
      return 'Must be a JSON object (e.g. {"since":"1h"}), not an array or scalar.'
    }
    return null
  } catch (e) {
    return e instanceof Error ? `Invalid JSON: ${e.message}` : 'Invalid JSON'
  }
}

interface ScheduleProps {
  state: EditorState
  set: Setter
}

export function ScheduleCard({ state, set }: ScheduleProps) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Schedule</CardTitle>
      </CardHeader>
      <CardContent>
        <WorkerScheduleField
          value={state.scheduleSpec}
          onChange={(v) => set('scheduleSpec', v)}
        />
      </CardContent>
    </Card>
  )
}

// ToolsCard lives in WorkerEditorToolsCard.tsx so this file stays
// under the 300-line cap; re-exported from there.
export { ToolsCard } from './WorkerEditorToolsCard'
