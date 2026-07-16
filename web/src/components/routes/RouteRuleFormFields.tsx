import { useEffect, useState, type Dispatch, type ReactNode, type SetStateAction } from 'react'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import type { AuthScope, DownstreamServer, Workspace } from '@/api/types'
import { scopeLabel } from '@/lib/scope-label'
import { ChevronDown, ChevronRight, X } from 'lucide-react'
import type { RouteFormData } from './route-form-model'

interface RouteRuleFormFieldsProps {
  form: RouteFormData
  setForm: Dispatch<SetStateAction<RouteFormData>>
  visible: boolean
  resetKey: string
  workspaces: Pick<Workspace, 'id' | 'name'>[]
  downstreams: Pick<DownstreamServer, 'id' | 'name' | 'tool_namespace'>[]
  authScopes: AuthScope[]
  saveError?: string | null
  authScopeExtras?: ReactNode
}

function isGitHubServer(
  downstreams: Pick<DownstreamServer, 'id' | 'tool_namespace'>[],
  serverId: string,
): boolean {
  const ds = downstreams.find((d) => d.id === serverId)
  return ds?.tool_namespace === 'github'
}

function scopeConstraintCount(policy: Record<string, string[]>): number {
  return Object.values(policy).reduce((sum, arr) => sum + arr.length, 0)
}

export function RouteRuleFormFields({
  form,
  setForm,
  visible,
  resetKey,
  workspaces,
  downstreams,
  authScopes,
  saveError,
  authScopeExtras,
}: RouteRuleFormFieldsProps) {
  const [showAdvanced, setShowAdvanced] = useState(false)
  const [showScope, setShowScope] = useState(false)
  const [chipInput, setChipInput] = useState('')
  const [orgInput, setOrgInput] = useState('')
  const [repoInput, setRepoInput] = useState('')

  const hasNonDefaultAdvanced = form.path_glob !== '**' || form.tool_match.length > 0
  const hasScopePolicy = scopeConstraintCount(form.scope_policy) > 0
  const isGitHub = isGitHubServer(downstreams, form.downstream_server_id)

  useEffect(() => {
    if (!visible) return
    setChipInput('')
    setOrgInput('')
    setRepoInput('')
    setShowAdvanced(form.path_glob !== '**' || form.tool_match.length > 0)
    setShowScope(scopeConstraintCount(form.scope_policy) > 0)
    // resetKey marks a new route/edit target; form is intentionally read at
    // that moment rather than re-running while the user types.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visible, resetKey])

  function addChip() {
    const value = chipInput.trim()
    if (!value) return
    setForm((current) => ({ ...current, tool_match: [...current.tool_match, value] }))
    setChipInput('')
  }

  function removeChip(index: number) {
    setForm((current) => ({
      ...current,
      tool_match: current.tool_match.filter((_, currentIndex) => currentIndex !== index),
    }))
  }

  function addScopeValue(key: string, value: string) {
    const trimmed = value.trim()
    if (!trimmed) return
    setForm((current) => {
      const existing = current.scope_policy[key] ?? []
      if (existing.includes(trimmed)) return current
      return {
        ...current,
        scope_policy: { ...current.scope_policy, [key]: [...existing, trimmed] },
      }
    })
  }

  function removeScopeValue(key: string, index: number) {
    setForm((current) => {
      const updated = (current.scope_policy[key] ?? []).filter((_, i) => i !== index)
      const newPolicy = { ...current.scope_policy }
      if (updated.length === 0) {
        delete newPolicy[key]
      } else {
        newPolicy[key] = updated
      }
      return { ...current, scope_policy: newPolicy }
    })
  }

  return (
    <div className="space-y-4">
      <div className="space-y-2">
        <Label className="text-xs text-muted-foreground">Name (optional)</Label>
        <Input
          value={form.name}
          onChange={(e) => setForm((current) => ({ ...current, name: e.target.value }))}
          placeholder="e.g. GitHub allow-all"
          data-testid="route-form-name"
        />
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-2">
          <Label className="text-xs text-muted-foreground">Workspace</Label>
          <Select
            value={form.workspace_id}
            onValueChange={(value) => setForm((current) => ({ ...current, workspace_id: value }))}
          >
            <SelectTrigger data-testid="route-form-workspace">
              <SelectValue placeholder="Select workspace..." />
            </SelectTrigger>
            <SelectContent>
              {workspaces.map((workspace) => (
                <SelectItem key={workspace.id} value={workspace.id}>
                  {workspace.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-2">
          <Label
            className={`text-xs text-muted-foreground ${form.policy === 'deny' ? 'opacity-50' : ''}`}
          >
            Server
          </Label>
          <Select
            value={form.downstream_server_id}
            onValueChange={(value) =>
              setForm((current) => ({ ...current, downstream_server_id: value }))
            }
            disabled={form.policy === 'deny'}
          >
            <SelectTrigger data-testid="route-form-downstream">
              <SelectValue
                placeholder={form.policy === 'deny' ? 'N/A for deny rules' : 'Select server...'}
              />
            </SelectTrigger>
            <SelectContent>
              {downstreams.map((downstream) => (
                <SelectItem key={downstream.id} value={downstream.id}>
                  {downstream.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>

      <div className="space-y-2">
        <Label
          className={`text-xs text-muted-foreground ${form.policy === 'deny' ? 'opacity-50' : ''}`}
        >
          Credential
        </Label>
        <Select
          value={form.auth_scope_id || 'none'}
          onValueChange={(value) =>
            setForm((current) => ({ ...current, auth_scope_id: value === 'none' ? '' : value }))
          }
          disabled={form.policy === 'deny'}
        >
          <SelectTrigger data-testid="route-form-auth-scope">
            <SelectValue placeholder={form.policy === 'deny' ? 'N/A for deny rules' : 'None'} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="none">None</SelectItem>
            {authScopes.map((scope) => (
              <SelectItem key={scope.id} value={scope.id}>
                <span className="flex items-baseline gap-2">
                  <span>{scopeLabel(scope)}</span>
                  {scopeLabel(scope) !== scope.name && (
                    <code className="font-mono text-[10px] text-muted-foreground/70">
                      {scope.name}
                    </code>
                  )}
                </span>
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {authScopeExtras}
      </div>

      <div className="grid grid-cols-2 gap-4">
        <div className="space-y-2">
          <Label className="text-xs text-muted-foreground">Policy</Label>
          <Select
            value={form.policy}
            onValueChange={(value) => {
              const policy = value as 'allow' | 'deny'
              if (policy === 'deny') {
                setForm((current) => ({
                  ...current,
                  policy,
                  downstream_server_id: '',
                  auth_scope_id: '',
                }))
                return
              }
              setForm((current) => ({ ...current, policy }))
            }}
          >
            <SelectTrigger data-testid="route-form-policy">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="allow">Allow</SelectItem>
              <SelectItem value="deny">Deny</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-2">
          <Label className="text-xs text-muted-foreground">Priority</Label>
          <Input
            type="number"
            value={form.priority}
            onChange={(e) =>
              setForm((current) => ({ ...current, priority: Number(e.target.value) }))
            }
            data-testid="route-form-priority"
          />
        </div>
      </div>

      {form.policy === 'allow' && (
        <div className="space-y-3 rounded-md border border-border/50 p-3">
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Approval</Label>
            <Select
              value={form.approval_mode}
              onValueChange={(value) =>
                setForm((current) => ({
                  ...current,
                  approval_mode: value as 'none' | 'write' | 'all',
                }))
              }
            >
              <SelectTrigger className="w-48" data-testid="route-form-approval-mode">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">None</SelectItem>
                <SelectItem value="write">Write only (destructive)</SelectItem>
                <SelectItem value="all">All tool calls</SelectItem>
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground/60">
              {form.approval_mode === 'none' && 'Tool calls execute without approval.'}
              {form.approval_mode === 'write' &&
                'Read-only tools execute freely; write or destructive tools require approval.'}
              {form.approval_mode === 'all' &&
                'Every tool call requires approval before execution.'}
            </p>
          </div>

          {form.approval_mode !== 'none' && (
            <div className="space-y-2">
              <Label className="text-xs text-muted-foreground">Timeout (seconds)</Label>
              <Input
                type="number"
                min={0}
                value={form.approval_timeout}
                onChange={(e) =>
                  setForm((current) => ({
                    ...current,
                    approval_timeout: Number(e.target.value),
                  }))
                }
                className="w-32"
                data-testid="route-form-approval-timeout"
              />
              <p className="text-xs text-muted-foreground/60">
                How long to wait for approval before auto-denying the call.
              </p>
            </div>
          )}
        </div>
      )}

      {form.policy === 'allow' && form.downstream_server_id && (
        <>
          <button
            type="button"
            className="flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
            onClick={() => setShowScope((current) => !current)}
            data-testid="route-form-scope-toggle"
          >
            {showScope ? (
              <ChevronDown className="h-3.5 w-3.5" />
            ) : (
              <ChevronRight className="h-3.5 w-3.5" />
            )}
            Scope Policy
            {!showScope && !hasScopePolicy && (
              <span className="text-muted-foreground/70">unrestricted</span>
            )}
            {!showScope && hasScopePolicy && (
              <Badge variant="secondary" className="ml-1 text-[10px] px-1.5 py-0">
                {scopeConstraintCount(form.scope_policy)} constraint
                {scopeConstraintCount(form.scope_policy) !== 1 ? 's' : ''}
              </Badge>
            )}
          </button>

          {showScope && (
            <div className="space-y-4 rounded-md border border-border/50 p-3">
              {isGitHub ? (
                <>
                  <div className="space-y-2">
                    <Label className="text-xs text-muted-foreground">
                      Allowed Organisations
                    </Label>
                    <div className="mb-2 flex flex-wrap gap-1">
                      {(form.scope_policy.org ?? []).map((org, index) => (
                        <Badge
                          key={`org-${org}-${index}`}
                          variant="outline"
                          className="gap-1 font-mono text-xs"
                        >
                          {org}
                          <button
                            type="button"
                            className="ml-0.5 hover:text-destructive"
                            onClick={() => removeScopeValue('org', index)}
                          >
                            <X className="h-3 w-3" />
                          </button>
                        </Badge>
                      ))}
                    </div>
                    <Input
                      value={orgInput}
                      onChange={(e) => setOrgInput(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') {
                          e.preventDefault()
                          addScopeValue('org', orgInput)
                          setOrgInput('')
                        }
                      }}
                      className="font-mono text-sm"
                      placeholder="acme-corp (press Enter to add)"
                    />
                  </div>

                  <div className="space-y-2">
                    <Label className="text-xs text-muted-foreground">
                      Allowed Repositories
                    </Label>
                    <div className="mb-2 flex flex-wrap gap-1">
                      {(form.scope_policy.repo ?? []).map((repo, index) => (
                        <Badge
                          key={`repo-${repo}-${index}`}
                          variant="outline"
                          className="gap-1 font-mono text-xs"
                        >
                          {repo}
                          <button
                            type="button"
                            className="ml-0.5 hover:text-destructive"
                            onClick={() => removeScopeValue('repo', index)}
                          >
                            <X className="h-3 w-3" />
                          </button>
                        </Badge>
                      ))}
                    </div>
                    <Input
                      value={repoInput}
                      onChange={(e) => setRepoInput(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') {
                          e.preventDefault()
                          addScopeValue('repo', repoInput)
                          setRepoInput('')
                        }
                      }}
                      className="font-mono text-sm"
                      placeholder="acme-corp/api (press Enter to add)"
                    />
                  </div>

                  <p className="text-xs text-muted-foreground/60">
                    Leave blank to allow all. When set, tool calls targeting repos or orgs not
                    in these lists will be blocked.
                  </p>
                </>
              ) : (
                <>
                  <p className="text-xs text-muted-foreground/60">
                    Scope policy restricts which resources this route can access. Format:{' '}
                    <code className="font-mono">
                      {'{"resource_type": ["allowed_value"]}'}
                    </code>
                  </p>
                  <textarea
                    className="w-full rounded-md border border-border bg-background p-2 font-mono text-sm"
                    rows={4}
                    value={JSON.stringify(form.scope_policy, null, 2)}
                    onChange={(e) => {
                      try {
                        const parsed = JSON.parse(e.target.value) as Record<string, string[]>
                        setForm((current) => ({ ...current, scope_policy: parsed }))
                      } catch {
                        // Allow typing invalid JSON while editing.
                      }
                    }}
                    placeholder='{"channel": ["#engineering"]}'
                    data-testid="route-form-scope-json"
                  />
                </>
              )}
            </div>
          )}
        </>
      )}

      <button
        type="button"
        className="flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
        onClick={() => setShowAdvanced((current) => !current)}
        data-testid="route-form-advanced-toggle"
      >
        {showAdvanced ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
        Advanced matching
        {!showAdvanced && !hasNonDefaultAdvanced && (
          <span className="text-muted-foreground/70">all files, all tools</span>
        )}
      </button>

      {showAdvanced && (
        <div className="space-y-4 rounded-md border border-border/50 p-3">
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">File pattern</Label>
            <Input
              className="font-mono text-sm"
              value={form.path_glob}
              onChange={(e) =>
                setForm((current) => ({ ...current, path_glob: e.target.value }))
              }
              placeholder="**"
              data-testid="route-form-path-glob"
            />
            <p className="text-xs text-muted-foreground/60">
              Restrict this route to specific files. Default <code className="font-mono">**</code>{' '}
              matches everything.
            </p>
          </div>

          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Tool filter</Label>
            <div className="mb-2 flex flex-wrap gap-1">
              {form.tool_match.map((pattern, index) => (
                <Badge key={`${pattern}-${index}`} variant="outline" className="gap-1 font-mono text-xs">
                  {pattern}
                  <button
                    type="button"
                    className="ml-0.5 hover:text-destructive"
                    onClick={() => removeChip(index)}
                  >
                    <X className="h-3 w-3" />
                  </button>
                </Badge>
              ))}
            </div>
            <Input
              value={chipInput}
              onChange={(e) => setChipInput(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  addChip()
                }
              }}
              className="font-mono text-sm"
              placeholder="github__* (press Enter to add)"
              data-testid="route-form-tool-filter-input"
            />
            <p className="text-xs text-muted-foreground/60">
              Leave blank to match every tool in the workspace.
            </p>
          </div>
        </div>
      )}

      {saveError && <p className="text-sm text-destructive">{saveError}</p>}
    </div>
  )
}
