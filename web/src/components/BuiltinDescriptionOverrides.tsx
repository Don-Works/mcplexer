import { useCallback, useEffect, useState } from 'react'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { useApi } from '@/hooks/use-api'
import { getSettings, updateSettings } from '@/api/client'
import type { Settings } from '@/api/types'
import { Loader2, RotateCcw, Save } from 'lucide-react'
import { toast } from 'sonner'

// Lets the user override the descriptions shown to MCP clients for each
// built-in mcpx__/mesh__ tool. Lives on /descriptions because that's where
// the AI-driven description refinement workflow already lives — putting it
// next to that flow makes more sense than burying it on the Settings page.

export function BuiltinDescriptionOverrides() {
  const fetcher = useCallback(() => getSettings(), [])
  const { data, loading, refetch } = useApi(fetcher)

  const [settings, setSettings] = useState<Settings | null>(null)
  const [saving, setSaving] = useState(false)
  const [dirty, setDirty] = useState(false)

  useEffect(() => {
    if (data) {
      setSettings({
        ...data.settings,
        tool_description_overrides: data.settings.tool_description_overrides ?? {},
      })
      setDirty(false)
    }
  }, [data])

  function patchOverride(toolName: string, description: string) {
    setSettings((prev) => {
      if (!prev) return prev
      const overrides = { ...prev.tool_description_overrides }
      if (description === '') {
        delete overrides[toolName]
      } else {
        overrides[toolName] = description
      }
      return { ...prev, tool_description_overrides: overrides }
    })
    setDirty(true)
  }

  function resetOverride(toolName: string) {
    patchOverride(toolName, '')
  }

  async function handleSave() {
    if (!settings) return
    setSaving(true)
    try {
      await updateSettings(settings)
      setDirty(false)
      toast.success('Description overrides saved')
      refetch()
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : 'Failed to save'
      toast.error(msg)
    } finally {
      setSaving(false)
    }
  }

  if (loading || !settings || !data) {
    return (
      <Card>
        <CardContent className="flex items-center justify-center py-12">
          <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
        </CardContent>
      </Card>
    )
  }

  const builtinDefaults = data.builtin_tool_defaults
  const builtinNames = Object.keys(builtinDefaults).sort()

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
          Built-in Tool Description Overrides
        </CardTitle>
        <Button
          size="sm"
          onClick={handleSave}
          disabled={saving || !dirty}
          data-testid="builtin-overrides-save"
        >
          {saving ? <Loader2 className="mr-2 h-3.5 w-3.5 animate-spin" /> : <Save className="mr-2 h-3.5 w-3.5" />}
          Save
        </Button>
      </CardHeader>
      <CardContent className="space-y-5">
        <p className="text-xs text-muted-foreground">
          Override the descriptions shown to MCP clients for each built-in mcpx / mesh tool. Clear a
          field to reset to the default. These travel with your config so changes are stable across
          restarts.
        </p>
        {builtinNames.map((name) => {
          const defaultDesc = builtinDefaults[name]
          const currentOverride = settings.tool_description_overrides?.[name] ?? ''
          const isOverridden = currentOverride !== '' && currentOverride !== defaultDesc

          return (
            <div key={name} className="space-y-1.5">
              <div className="flex items-center gap-2">
                <Label className="font-mono text-xs">{name}</Label>
                {isOverridden && (
                  <button
                    type="button"
                    onClick={() => resetOverride(name)}
                    aria-label={`Reset ${name} to default`}
                    data-testid={`builtin-overrides-reset-${name}`}
                    className="flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] text-muted-foreground hover:bg-muted hover:text-foreground transition-colors"
                    title="Reset to default"
                  >
                    <RotateCcw className="h-3 w-3" />
                    Reset
                  </button>
                )}
              </div>
              <Textarea
                value={currentOverride || defaultDesc}
                onChange={(e) => patchOverride(name, e.target.value)}
                className={`min-h-[60px] font-mono text-xs ${
                  isOverridden ? 'border-primary/40 bg-primary/5' : ''
                }`}
                rows={2}
              />
            </div>
          )
        })}
      </CardContent>
    </Card>
  )
}
