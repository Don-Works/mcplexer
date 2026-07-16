import { useEffect, useMemo, useState } from 'react'
import { Eye, EyeOff, Loader2 } from 'lucide-react'
import { toast } from 'sonner'
import { putSecret } from '@/api/client'
import type { AuthScope } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

function defaultKey(scope: AuthScope): string {
  if (scope.type === 'header') return 'Authorization'
  if (scope.type === 'client_credentials') return 'client_secret'
  if (scope.type === 'hawk') return 'key'
  return ''
}

export function InlineSecretEditor({ scope, onSaved }: { scope: AuthScope; onSaved: () => void }) {
  const hintedKeys = useMemo(
    () => (scope.env_fields ?? []).map((field) => field.key).filter(Boolean),
    [scope.env_fields],
  )
  const [open, setOpen] = useState(false)
  const [customKey, setCustomKey] = useState(() => defaultKey(scope))
  const [values, setValues] = useState<Record<string, string>>({})
  const [visible, setVisible] = useState(false)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    setOpen(false)
    setCustomKey(defaultKey(scope))
    setValues({})
  }, [scope])

  const entries = hintedKeys.length > 0
    ? hintedKeys.map((key) => [key, values[key] ?? ''] as const)
    : [[customKey, values.__custom__ ?? ''] as const]
  const canSave = entries.length > 0 && entries.every(([key, value]) => key.trim() && value.trim())

  async function save() {
    if (!canSave) return
    setSaving(true)
    try {
      for (const [key, value] of entries) await putSecret(scope.id, key.trim(), value.trim())
      toast.success('Credential saved')
      setOpen(false)
      onSaved()
    } catch (error) {
      toast.error(error instanceof Error ? error.message : 'Failed to save credential')
    } finally {
      setSaving(false)
    }
  }

  if (!open) {
    return <Button type="button" variant="outline" size="sm" onClick={() => setOpen(true)}>Add secret</Button>
  }

  return (
    <div className="mt-3 space-y-3 border-t border-amber-500/20 pt-3" data-testid="inline-secret-editor">
      {hintedKeys.length === 0 && (
        <div className="space-y-1">
          <Label htmlFor="inline-secret-key">Secret name</Label>
          <Input
            id="inline-secret-key"
            className="font-mono text-sm"
            value={customKey}
            onChange={(event) => setCustomKey(event.target.value)}
            placeholder={scope.type === 'env' ? 'API_KEY' : 'Authorization'}
          />
        </div>
      )}
      {entries.map(([key], index) => (
        <div key={key || index} className="space-y-1">
          <Label htmlFor={`inline-secret-value-${index}`} className="font-mono text-xs">{key || 'Secret value'}</Label>
          <div className="relative">
            <Input
              id={`inline-secret-value-${index}`}
              type={visible ? 'text' : 'password'}
              className="pr-9 font-mono text-sm"
              value={hintedKeys.length > 0 ? values[key] ?? '' : values.__custom__ ?? ''}
              onChange={(event) => setValues((current) => ({
                ...current,
                [hintedKeys.length > 0 ? key : '__custom__']: event.target.value,
              }))}
              placeholder={scope.type === 'header' ? 'Bearer …' : 'Paste secret'}
              autoComplete="off"
            />
            <button
              type="button"
              className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
              onClick={() => setVisible((current) => !current)}
              aria-label={visible ? 'Hide secret' : 'Show secret'}
            >
              {visible ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
            </button>
          </div>
        </div>
      ))}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={() => setOpen(false)} disabled={saving}>Cancel</Button>
        <Button type="button" size="sm" onClick={() => void save()} disabled={!canSave || saving}>
          {saving && <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />}
          Save credential
        </Button>
      </div>
    </div>
  )
}
