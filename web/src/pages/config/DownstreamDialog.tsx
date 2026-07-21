import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { ChevronDown, ChevronRight } from 'lucide-react'
import type { ServerCacheConfig } from '@/api/types'

export interface DownstreamFormData {
  name: string
  transport: 'stdio' | 'http' | 'internal'
  command: string
  args: string[]
  url: string | null
  tool_namespace: string
  idle_timeout_sec: number
  max_instances: number
  restart_policy: string
  disabled: boolean
  cache_config?: ServerCacheConfig
}

export const emptyDownstreamForm: DownstreamFormData = {
  name: '',
  transport: 'stdio',
  command: '',
  args: [],
  url: null,
  tool_namespace: '',
  idle_timeout_sec: 300,
  max_instances: 1,
  restart_policy: 'on-failure',
  disabled: false,
}

export function DownstreamDialog({
  open,
  onClose,
  form,
  setForm,
  onSave,
  saving,
  editing,
  saveError,
}: {
  open: boolean
  onClose: () => void
  form: DownstreamFormData
  setForm: React.Dispatch<React.SetStateAction<DownstreamFormData>>
  onSave: () => void
  saving: boolean
  editing: boolean
  saveError: string | null
}) {
  const [showCaching, setShowCaching] = useState(false)

  const cacheEnabled = form.cache_config?.enabled ?? true
  const cacheTTL = form.cache_config?.read_ttl_sec ?? 1800
  const cacheMaxEntries = form.cache_config?.max_entries ?? 1000

  function updateCache(patch: Partial<ServerCacheConfig>) {
    setForm((current) => ({
      ...current,
      cache_config: { ...current.cache_config, ...patch },
    }))
  }

  function formatTTLHint(sec: number): string {
    if (sec === 0) return 'indefinite'
    if (sec < 60) return `${sec}s`
    if (sec < 3600) return `${Math.round(sec / 60)}m`
    return `${(sec / 3600).toFixed(1)}h`
  }

  const previewCommand = [form.command, ...(form.args ?? [])].filter(Boolean).join(' ')

  return (
    <Dialog open={open} onOpenChange={() => onClose()}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{editing ? 'Edit Server' : 'Add Server'}</DialogTitle>
          <DialogDescription>
            Define how MCPlexer should launch or reach this downstream MCP server.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-5">
          <section className="space-y-4 rounded-md border border-border/50 p-4">
            <div className="space-y-2">
              <Label className="text-xs text-muted-foreground">Name</Label>
              <Input
                value={form.name}
                onChange={(e) => setForm((current) => ({ ...current, name: e.target.value }))}
                placeholder="e.g. GitHub, Linear, Internal Gateway"
              />
            </div>

            <div className="grid gap-4 md:grid-cols-2">
              <div className="space-y-2">
                <Label className="text-xs text-muted-foreground">Transport</Label>
                <Select
                  value={form.transport}
                  onValueChange={(value) =>
                    setForm((current) => ({ ...current, transport: value as 'stdio' | 'http' }))
                  }
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="stdio">stdio</SelectItem>
                    <SelectItem value="http">http</SelectItem>
                  </SelectContent>
                </Select>
                <p className="text-xs text-muted-foreground/70">
                  Use `stdio` for local processes and `http` for remote MCP endpoints.
                </p>
              </div>

              <div className="space-y-2">
                <Label className="text-xs text-muted-foreground">Tool Namespace</Label>
                <Input
                  className="font-mono text-sm"
                  value={form.tool_namespace}
                  onChange={(e) =>
                    setForm((current) => ({ ...current, tool_namespace: e.target.value }))
                  }
                  placeholder="github"
                />
                <p className="text-xs text-muted-foreground/70">
                  Tools appear as <span className="font-mono">{form.tool_namespace || 'namespace'}__tool</span>.
                </p>
              </div>
            </div>

            {form.transport === 'stdio' ? (
              <div className="space-y-4 rounded-md border border-border/50 p-3">
                <div className="space-y-2">
                  <Label className="text-xs text-muted-foreground">Command</Label>
                  <Input
                    className="font-mono text-sm"
                    value={form.command}
                    onChange={(e) =>
                      setForm((current) => ({ ...current, command: e.target.value }))
                    }
                    placeholder="npx"
                  />
                </div>

                <div className="space-y-2">
                  <Label className="text-xs text-muted-foreground">Arguments</Label>
                  <Textarea
                    className="min-h-24 font-mono text-sm"
                    value={(form.args ?? []).join('\n')}
                    onChange={(e) =>
                      setForm((current) => ({
                        ...current,
                        args: e.target.value
                          .split('\n')
                          .map((line) => line.trim())
                          .filter(Boolean),
                      }))
                    }
                    placeholder={'-y\n@modelcontextprotocol/server-github'}
                  />
                  <p className="text-xs text-muted-foreground/70">
                    One argument per line. This avoids quoting issues and preserves spaces safely.
                  </p>
                </div>

                {previewCommand && (
                  <div className="rounded-md border border-border/50 bg-muted/30 px-3 py-2 font-mono text-xs text-muted-foreground">
                    <span className="text-primary">$</span> {previewCommand}
                  </div>
                )}
              </div>
            ) : (
              <div className="space-y-2">
                <Label className="text-xs text-muted-foreground">URL</Label>
                <Input
                  className="font-mono text-sm"
                  value={form.url ?? ''}
                  onChange={(e) =>
                    setForm((current) => ({ ...current, url: e.target.value || null }))
                  }
                  placeholder="https://example.com/mcp"
                />
                <p className="text-xs text-muted-foreground/70">
                  MCPlexer will connect directly to this HTTP endpoint and can inject headers when
                  a header or OAuth credential is attached via routing.
                </p>
              </div>
            )}
          </section>

          <section className="space-y-4 rounded-md border border-border/50 p-4">
            <div className="space-y-1">
              <h3 className="text-sm font-semibold">Runtime</h3>
              <p className="text-xs text-muted-foreground">
                Control process lifecycle and connection reuse.
              </p>
            </div>

            <div className="grid gap-4 md:grid-cols-3">
              <div className="space-y-2">
                <Label className="text-xs text-muted-foreground">Idle Timeout (sec)</Label>
                <Input
                  type="number"
                  min={0}
                  value={form.idle_timeout_sec}
                  onChange={(e) =>
                    setForm((current) => ({
                      ...current,
                      idle_timeout_sec: Number(e.target.value),
                    }))
                  }
                />
              </div>
              <div className="space-y-2">
                <Label className="text-xs text-muted-foreground">Max Instances</Label>
                <Input
                  type="number"
                  min={1}
                  value={form.max_instances}
                  onChange={(e) =>
                    setForm((current) => ({
                      ...current,
                      max_instances: Number(e.target.value),
                    }))
                  }
                />
              </div>
              <div className="space-y-2">
                <Label className="text-xs text-muted-foreground">Restart Policy</Label>
                <Select
                  value={form.restart_policy}
                  onValueChange={(value) =>
                    setForm((current) => ({ ...current, restart_policy: value }))
                  }
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="never">never</SelectItem>
                    <SelectItem value="on-failure">on-failure</SelectItem>
                    <SelectItem value="always">always</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>

            <label className="flex items-start gap-3 rounded-md border border-border/50 p-3">
              <Checkbox
                checked={form.disabled}
                onCheckedChange={(checked) =>
                  setForm((current) => ({ ...current, disabled: checked === true }))
                }
              />
              <div className="space-y-1">
                <div className="text-sm font-medium">Create as disabled</div>
                <p className="text-xs text-muted-foreground">
                  Keep the server configured but inactive until you are ready to route traffic to
                  it.
                </p>
              </div>
            </label>
          </section>

          <section className="space-y-3 rounded-md border border-border/50 p-4">
            <button
              type="button"
              className="flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
              onClick={() => setShowCaching((current) => !current)}
            >
              {showCaching ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
              Caching
              {!showCaching && (
                <span className="text-muted-foreground/70">
                  {cacheEnabled ? `${formatTTLHint(cacheTTL)} TTL` : 'disabled'}
                </span>
              )}
            </button>

            {showCaching && (
              <div className="space-y-3 rounded-md border border-border/50 p-3">
                <label className="flex items-center gap-2">
                  <Checkbox
                    checked={cacheEnabled}
                    onCheckedChange={(checked) => updateCache({ enabled: checked === true })}
                  />
                  <span className="text-sm">Enable caching</span>
                </label>

                {cacheEnabled && (
                  <div className="grid gap-4 md:grid-cols-2">
                    <div className="space-y-2">
                      <Label className="text-xs text-muted-foreground">TTL (seconds)</Label>
                      <Input
                        type="number"
                        min={0}
                        value={cacheTTL}
                        onChange={(e) => updateCache({ read_ttl_sec: Number(e.target.value) })}
                      />
                      <p className="text-xs text-muted-foreground/70">
                        `0` keeps entries until invalidated.
                      </p>
                    </div>

                    <div className="space-y-2">
                      <Label className="text-xs text-muted-foreground">Max Entries</Label>
                      <Input
                        type="number"
                        min={1}
                        value={cacheMaxEntries}
                        onChange={(e) => updateCache({ max_entries: Number(e.target.value) })}
                      />
                    </div>
                  </div>
                )}
              </div>
            )}
          </section>
        </div>

        {saveError && <p className="text-sm text-destructive">{saveError}</p>}

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={onSave} disabled={saving || !form.name.trim() || !form.tool_namespace.trim()}>
            {saving ? 'Saving...' : 'Save'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
