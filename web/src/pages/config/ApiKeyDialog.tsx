import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Badge } from '@/components/ui/badge'
import { deleteSecret, listSecretKeys, putSecret } from '@/api/client'
import { Eye, EyeOff, Lock, Trash2 } from 'lucide-react'
import { toast } from 'sonner'

interface Props {
  open: boolean
  onClose: () => void
  authScopeId: string
  authScopeName: string
  serverName: string
  envFields?: { key: string; label: string; secret: boolean }[]
}

export function ApiKeyDialog({
  open,
  onClose,
  authScopeId,
  authScopeName,
  serverName,
  envFields,
}: Props) {
  const [keys, setKeys] = useState<string[]>([])
  const [loading, setLoading] = useState(false)
  const [newKey, setNewKey] = useState('')
  const [newValue, setNewValue] = useState('')
  const [showValue, setShowValue] = useState(false)
  const [saving, setSaving] = useState(false)
  const [fieldValues, setFieldValues] = useState<Record<string, string>>({})
  const [fieldVisibility, setFieldVisibility] = useState<Record<string, boolean>>({})

  const hasLabeledFields = (envFields ?? []).length > 0
  const envFieldKeys = useMemo(() => new Set((envFields ?? []).map((field) => field.key)), [envFields])
  const extraKeys = useMemo(() => keys.filter((key) => !envFieldKeys.has(key)), [envFieldKeys, keys])

  const fetchKeys = useCallback(async () => {
    if (!authScopeId) return
    setLoading(true)
    try {
      const res = await listSecretKeys(authScopeId)
      setKeys(res.keys)
    } catch {
      setKeys([])
    } finally {
      setLoading(false)
    }
  }, [authScopeId])

  useEffect(() => {
    if (!open) return
    fetchKeys()
    setNewKey('')
    setNewValue('')
    setShowValue(false)
    setFieldValues({})
    setFieldVisibility({})
  }, [open, fetchKeys])

  async function handleAdd() {
    if (!newKey.trim() || !newValue.trim()) return
    setSaving(true)
    try {
      await putSecret(authScopeId, newKey.trim(), newValue.trim())
      toast.success(`Secret "${newKey.trim()}" saved`)
      setNewKey('')
      setNewValue('')
      setShowValue(false)
      fetchKeys()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to save secret')
    } finally {
      setSaving(false)
    }
  }

  async function handleSaveField(key: string) {
    const value = fieldValues[key]
    if (!value?.trim()) return
    setSaving(true)
    try {
      await putSecret(authScopeId, key, value.trim())
      toast.success('Saved')
      setFieldValues((values) => ({ ...values, [key]: '' }))
      fetchKeys()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to save')
    } finally {
      setSaving(false)
    }
  }

  async function handleSaveAllFields() {
    if (!envFields) return
    const toSave = envFields.filter((field) => fieldValues[field.key]?.trim())
    if (toSave.length === 0) return
    setSaving(true)
    try {
      for (const field of toSave) {
        await putSecret(authScopeId, field.key, fieldValues[field.key].trim())
      }
      toast.success('Credentials saved')
      setFieldValues({})
      fetchKeys()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to save')
    } finally {
      setSaving(false)
    }
  }

  async function handleDelete(key: string) {
    try {
      await deleteSecret(authScopeId, key)
      toast.success(`Secret "${key}" removed`)
      fetchKeys()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to delete secret')
    }
  }

  return (
    <Dialog open={open} onOpenChange={() => onClose()}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Lock className="h-4 w-4" />
            API Keys
          </DialogTitle>
          <DialogDescription>
            Manage encrypted environment variables for <span className="font-mono">{serverName}</span>{' '}
            via auth scope <span className="font-mono">{authScopeName}</span>.
          </DialogDescription>
        </DialogHeader>

        {loading ? (
          <div className="flex items-center gap-2 py-4 text-sm text-muted-foreground">
            <div className="h-2 w-2 rounded-full bg-primary/60" />
            Loading...
          </div>
        ) : (
          <div className="space-y-4">
            <div className="flex flex-wrap items-center gap-2 rounded-md border border-border/50 bg-muted/30 px-3 py-2">
              <Badge variant="outline" className="font-mono text-xs">
                {keys.length} stored
              </Badge>
              <p className="text-xs text-muted-foreground">
                Values stay encrypted at rest. Only variable names are shown after save.
              </p>
            </div>

            {hasLabeledFields && envFields ? (
              <div className="space-y-4">
                <div className="space-y-3">
                  {envFields.map((field) => {
                    const isSet = keys.includes(field.key)
                    const visible = fieldVisibility[field.key] ?? false
                    return (
                      <div key={field.key} className="space-y-2">
                        <div className="flex flex-wrap items-center gap-2">
                          <Label className="text-sm font-medium">{field.label}</Label>
                          <Badge variant="outline" className="font-mono text-[10px]">
                            {field.key}
                          </Badge>
                          {isSet && (
                            <Badge
                              variant="outline"
                              className="text-[10px] text-emerald-600 border-emerald-500/30"
                            >
                              stored
                            </Badge>
                          )}
                        </div>
                        <div className="relative">
                          <Input
                            className="pr-8 font-mono text-sm"
                            type={field.secret && !visible ? 'password' : 'text'}
                            value={fieldValues[field.key] ?? ''}
                            onChange={(e) =>
                              setFieldValues((values) => ({
                                ...values,
                                [field.key]: e.target.value,
                              }))
                            }
                            placeholder={
                              isSet
                                ? 'Enter a new value to rotate it'
                                : `Enter ${field.label.toLowerCase()}`
                            }
                            onKeyDown={(e) => e.key === 'Enter' && handleSaveField(field.key)}
                          />
                          {field.secret && (
                            <button
                              type="button"
                              className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                              onClick={() =>
                                setFieldVisibility((visibility) => ({
                                  ...visibility,
                                  [field.key]: !visible,
                                }))
                              }
                            >
                              {visible ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                            </button>
                          )}
                        </div>
                      </div>
                    )
                  })}
                </div>

                <Button
                  size="sm"
                  onClick={handleSaveAllFields}
                  disabled={saving || !envFields.some((field) => fieldValues[field.key]?.trim())}
                >
                  {saving ? 'Saving...' : 'Save Credentials'}
                </Button>

                <div className="space-y-3 rounded-md border border-border/50 p-3">
                  <div className="space-y-1">
                    <p className="text-sm font-medium">Additional environment variables</p>
                    <p className="text-xs text-muted-foreground">
                      Add any extra values this downstream server expects.
                    </p>
                  </div>

                  {extraKeys.length > 0 && <StoredKeys keys={extraKeys} onDelete={handleDelete} />}

                  <div className="grid gap-3 md:grid-cols-2">
                    <div className="space-y-1">
                      <Label className="text-[11px] text-muted-foreground">Variable name</Label>
                      <Input
                        className="font-mono text-sm"
                        value={newKey}
                        onChange={(e) =>
                          setNewKey(
                            e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, ''),
                          )
                        }
                        placeholder="EXTRA_API_KEY"
                      />
                    </div>
                    <div className="space-y-1">
                      <Label className="text-[11px] text-muted-foreground">Value</Label>
                      <div className="relative">
                        <Input
                          className="pr-8 font-mono text-sm"
                          type={showValue ? 'text' : 'password'}
                          value={newValue}
                          onChange={(e) => setNewValue(e.target.value)}
                          placeholder="sk-..."
                          onKeyDown={(e) => e.key === 'Enter' && handleAdd()}
                        />
                        <button
                          type="button"
                          className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                          onClick={() => setShowValue((current) => !current)}
                        >
                          {showValue ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                        </button>
                      </div>
                    </div>
                  </div>

                  <Button
                    size="sm"
                    variant="outline"
                    onClick={handleAdd}
                    disabled={saving || !newKey.trim() || !newValue.trim()}
                  >
                    {saving ? 'Saving...' : 'Add Variable'}
                  </Button>
                </div>
              </div>
            ) : (
              <div className="space-y-3">
                <StoredKeys keys={keys} onDelete={handleDelete} />

                <div className="space-y-3 rounded-md border border-border/50 p-3">
                  <div className="space-y-1">
                    <p className="text-sm font-medium">Add environment variable</p>
                    <p className="text-xs text-muted-foreground">
                      Variable names are normalized to uppercase to match common MCP server
                      expectations.
                    </p>
                  </div>

                  <div className="grid gap-3 md:grid-cols-2">
                    <div className="space-y-1">
                      <Label className="text-[11px] text-muted-foreground">Variable name</Label>
                      <Input
                        className="font-mono text-sm"
                        value={newKey}
                        onChange={(e) =>
                          setNewKey(
                            e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, ''),
                          )
                        }
                        placeholder="AIKIDO_API_KEY"
                      />
                    </div>
                    <div className="space-y-1">
                      <Label className="text-[11px] text-muted-foreground">Value</Label>
                      <div className="relative">
                        <Input
                          className="pr-8 font-mono text-sm"
                          type={showValue ? 'text' : 'password'}
                          value={newValue}
                          onChange={(e) => setNewValue(e.target.value)}
                          placeholder="sk-..."
                          onKeyDown={(e) => e.key === 'Enter' && handleAdd()}
                        />
                        <button
                          type="button"
                          className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                          onClick={() => setShowValue((current) => !current)}
                        >
                          {showValue ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                        </button>
                      </div>
                    </div>
                  </div>

                  <Button
                    size="sm"
                    onClick={handleAdd}
                    disabled={saving || !newKey.trim() || !newValue.trim()}
                  >
                    {saving ? 'Saving...' : 'Save'}
                  </Button>
                </div>
              </div>
            )}
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Done
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function StoredKeys({
  keys,
  onDelete,
}: {
  keys: string[]
  onDelete: (key: string) => void
}) {
  if (keys.length === 0) {
    return (
      <div className="rounded-md border border-dashed border-border/60 bg-muted/20 px-4 py-3 text-sm text-muted-foreground">
        Nothing stored yet.
      </div>
    )
  }

  return (
    <div className="space-y-1.5">
      {keys.map((key) => (
        <div
          key={key}
          className="flex items-center justify-between rounded-md border border-border/50 px-3 py-2"
        >
          <div className="flex items-center gap-2">
            <span className="font-mono text-sm">{key}</span>
            <Badge
              variant="outline"
              className="text-[10px] text-emerald-600 border-emerald-500/30"
            >
              configured
            </Badge>
          </div>
          <Button
            variant="ghost"
            size="sm"
            className="h-6 w-6 p-0 hover:bg-destructive/10 hover:text-destructive"
            onClick={() => onDelete(key)}
          >
            <Trash2 className="h-3 w-3" />
          </Button>
        </div>
      ))}
    </div>
  )
}
