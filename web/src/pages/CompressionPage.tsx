import { useCallback, useEffect, useMemo, useState } from 'react'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { useApi } from '@/hooks/use-api'
import { getCompressionStats, getSettings, updateSettings } from '@/api/client'
import type { CompressionTransformAggregate, Settings } from '@/api/types'
import { Gauge, Loader2, Save } from 'lucide-react'
import { toast } from 'sonner'

type Mode = 'off' | 'shadow' | 'on'

function fmt(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

export function CompressionPage() {
  const statsFetcher = useCallback(() => getCompressionStats(30), [])
  const { data: stats, loading: statsLoading, error: statsError } = useApi(statsFetcher)
  const settingsFetcher = useCallback(() => getSettings(), [])
  const { data: settingsData, loading: settingsLoading, error: settingsError } = useApi(settingsFetcher)

  const [mode, setMode] = useState<Mode>('shadow')
  const [disabled, setDisabled] = useState<Set<string>>(new Set())
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (settingsData?.settings) {
      setMode((settingsData.settings.compression_mode as Mode) ?? 'shadow')
      setDisabled(new Set(settingsData.settings.compression_disabled_transforms ?? []))
    }
  }, [settingsData])

  const byTransform = useMemo(() => {
    const m = new Map<string, CompressionTransformAggregate>()
    for (const t of stats?.aggregate.by_transform ?? []) m.set(t.transform, t)
    return m
  }, [stats])

  const transforms = stats?.transforms ?? []
  const agg = stats?.aggregate
  const loading = statsLoading || settingsLoading
  const loadError = statsError || settingsError
  const settingsReady = !settingsLoading && !!settingsData?.settings

  function toggleTransform(name: string) {
    setDisabled((prev) => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
    setDirty(true)
  }

  function changeMode(next: string) {
    if (next !== 'off' && next !== 'shadow' && next !== 'on') return
    setMode(next)
    setDirty(true)
  }

  async function save() {
    if (!settingsData?.settings) {
      toast.error('Settings have not loaded yet — reload the page and try again.')
      return
    }
    setSaving(true)
    try {
      const payload: Settings = {
        ...settingsData.settings,
        compression_mode: mode,
        compression_disabled_transforms: Array.from(disabled),
      }
      await updateSettings(payload)
      toast.success('Compression settings saved')
      setDirty(false)
    } catch (e) {
      toast.error(`Failed to save: ${e instanceof Error ? e.message : String(e)}`)
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="space-y-6 p-1">
      <div className="flex items-center gap-2">
        <Gauge className="h-5 w-5" />
        <h1 className="text-xl font-semibold">Token compression</h1>
      </div>
      <p className="max-w-2xl text-sm text-muted-foreground">
        Compresses downstream MCP tool-result payloads before they reach the model. Start in{' '}
        <strong>dry-run</strong> to measure what each transform <em>would</em> save with zero risk, then turn on the
        ones you trust. Lossless transforms never change the answer; every number below is measured on your real
        traffic.
      </p>

      {loadError && (
        <div className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          Failed to load compression data: {loadError}
        </div>
      )}

      <Card>
        <CardHeader>
          <CardTitle>Mode</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <ToggleGroup type="single" value={mode} onValueChange={changeMode}>
            <ToggleGroupItem value="off">Off</ToggleGroupItem>
            <ToggleGroupItem value="shadow">Dry-run (measure)</ToggleGroupItem>
            <ToggleGroupItem value="on">On (apply)</ToggleGroupItem>
          </ToggleGroup>
          <p className="text-xs text-muted-foreground">
            {mode === 'off' && 'Compression is fully off — no measurement, no changes.'}
            {mode === 'shadow' &&
              'Measuring only — tool results are returned untouched while would-be savings accrue below.'}
            {mode === 'on' && 'Applying lossless transforms for real. Results are compressed; savings are realized.'}
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Observed savings (last {agg?.days ?? 30} days)</CardTitle>
        </CardHeader>
        <CardContent>
          {loading ? (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" /> Loading…
            </div>
          ) : (
            <div className="grid grid-cols-2 gap-4 text-sm sm:grid-cols-3">
              <Stat label="Tool results measured" value={fmt(agg?.samples ?? 0)} />
              <Stat label="Would-save tokens" value={fmt(agg?.would_save_tokens ?? 0)} />
              <Stat label="Applied-save tokens" value={fmt(agg?.applied_save_tokens ?? 0)} />
            </div>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Transforms</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2">
          {transforms.length === 0 && <p className="text-sm text-muted-foreground">No transforms registered.</p>}
          {transforms.map((t) => {
            const stat = byTransform.get(t.name)
            const enabled = !disabled.has(t.name)
            const pct = stat && stat.orig_bytes > 0 ? Math.round((stat.would_save_bytes / stat.orig_bytes) * 100) : 0
            return (
              <div key={t.name} className="flex items-center justify-between rounded-md border p-3">
                <div className="space-y-0.5">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-sm">{t.name}</span>
                    <span
                      className={`rounded px-1.5 py-0.5 text-[10px] ${
                        t.lossless ? 'bg-emerald-500/15 text-emerald-600' : 'bg-amber-500/15 text-amber-600'
                      }`}
                    >
                      {t.lossless ? 'lossless' : 'lossy'}
                    </span>
                    {t.verified && (
                      <span className="rounded bg-sky-500/15 px-1.5 py-0.5 text-[10px] text-sky-600" title="Passes the gimmick gate: value-lossless, secret-safe, real win">
                        gate-verified
                      </span>
                    )}
                  </div>
                  <div className="text-xs text-muted-foreground">
                    {stat
                      ? `${fmt(stat.would_save_tokens)} tokens would save · ${pct}% · ${fmt(stat.samples)} samples`
                      : 'No data yet'}
                  </div>
                </div>
                <button
                  type="button"
                  role="switch"
                  aria-checked={enabled}
                  aria-label={`${enabled ? 'Disable' : 'Enable'} ${t.name}`}
                  data-testid={`compression-toggle-${t.name}`}
                  onClick={() => toggleTransform(t.name)}
                  className={`relative inline-flex h-6 w-11 items-center rounded-full transition-colors ${
                    enabled ? 'bg-primary' : 'bg-muted'
                  }`}
                >
                  <span
                    className={`inline-block h-4 w-4 transform rounded-full bg-white transition-transform ${
                      enabled ? 'translate-x-5' : 'translate-x-1'
                    }`}
                  />
                </button>
              </div>
            )
          })}
        </CardContent>
      </Card>

      <div className="flex justify-end">
        <Button onClick={save} disabled={!dirty || saving || !settingsReady}>
          {saving ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Save className="mr-2 h-4 w-4" />}
          Save
        </Button>
      </div>
    </div>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-2xl font-semibold">{value}</div>
      <div className="text-xs text-muted-foreground">{label}</div>
    </div>
  )
}
