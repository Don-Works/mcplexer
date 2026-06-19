import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
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
import { Badge } from '@/components/ui/badge'
import { Link } from 'react-router-dom'
import { deleteSecret, listSecretKeys, putSecret } from '@/api/client'
import { ChevronDown, ChevronRight, Eye, EyeOff } from 'lucide-react'
import { toast } from 'sonner'
import {
  type HeaderMode,
  credentialTypeOptions,
  headerModeOptions,
  buildHeaderSecret,
  SectionHeading,
  StoredSecretList,
} from './AuthScopeDialogParts'

export interface AuthScopeFormData {
  name: string
  display_name: string
  type: 'env' | 'header' | 'hawk' | 'oauth2' | 'client_credentials'
  oauth_provider_id: string
  redaction_hints: string[]
}

export const emptyAuthScopeForm: AuthScopeFormData = {
  name: '',
  display_name: '',
  type: 'env',
  oauth_provider_id: '',
  redaction_hints: [],
}

export function AuthScopeDialog({
  open,
  onClose,
  form,
  setForm,
  onSave,
  saving,
  editing,
  editingId,
  envFields,
  providers,
  saveError,
}: {
  open: boolean
  onClose: () => void
  form: AuthScopeFormData
  setForm: React.Dispatch<React.SetStateAction<AuthScopeFormData>>
  onSave: () => void
  saving: boolean
  editing: boolean
  editingId?: string
  envFields?: { key: string; label: string; secret: boolean }[]
  saveError: string | null
  providers: { id: string; name: string }[]
}) {
  const [hintInput, setHintInput] = useState('')
  const [showAdvanced, setShowAdvanced] = useState(false)

  const [secretKeys, setSecretKeys] = useState<string[]>([])
  const [fieldValues, setFieldValues] = useState<Record<string, string>>({})
  const [fieldVisibility, setFieldVisibility] = useState<Record<string, boolean>>({})
  const [newKey, setNewKey] = useState('')
  const [newValue, setNewValue] = useState('')
  const [showValue, setShowValue] = useState(false)
  const [savingSecret, setSavingSecret] = useState(false)

  const [headerMode, setHeaderMode] = useState<HeaderMode>('bearer')
  const [headerName, setHeaderName] = useState('Authorization')
  const [headerValue, setHeaderValue] = useState('')
  const [headerPrefix, setHeaderPrefix] = useState('Bearer ')
  const [showHeaderValue, setShowHeaderValue] = useState(false)

  const isEnvLike = form.type === 'env' || form.type === 'client_credentials' || form.type === 'hawk'
  const supportsSecretMaterial = isEnvLike || form.type === 'header' || form.type === 'hawk'
  const hasLabeledFields = editing && isEnvLike && envFields && envFields.length > 0
  const envFieldKeys = useMemo(() => new Set((envFields ?? []).map((field) => field.key)), [envFields])
  const extraEnvKeys = useMemo(
    () => secretKeys.filter((key) => !envFieldKeys.has(key)),
    [envFieldKeys, secretKeys],
  )
  const headerDraft = useMemo(
    () => buildHeaderSecret(headerMode, headerName, headerValue, headerPrefix),
    [headerMode, headerName, headerPrefix, headerValue],
  )

  const fetchSecretKeys = useCallback(async () => {
    if (!editingId || !supportsSecretMaterial) return
    try {
      const res = await listSecretKeys(editingId)
      setSecretKeys(res.keys)
    } catch {
      setSecretKeys([])
    }
  }, [editingId, supportsSecretMaterial])

  useEffect(() => {
    if (open && editing) {
      fetchSecretKeys()
    }
  }, [open, editing, fetchSecretKeys])

  useEffect(() => {
    if (!open) return
    setHintInput('')
    setShowAdvanced((form.redaction_hints ?? []).length > 0)
    setFieldValues({})
    setFieldVisibility({})
    setNewKey('')
    setNewValue('')
    setShowValue(false)
    setHeaderMode('bearer')
    setHeaderName('Authorization')
    setHeaderValue('')
    setHeaderPrefix('Bearer ')
    setShowHeaderValue(false)
  }, [open])

  useEffect(() => {
    if (!open || form.type !== 'header') return
    setHeaderMode('bearer')
    setHeaderName('Authorization')
    setHeaderValue('')
    setHeaderPrefix('Bearer ')
    setShowHeaderValue(false)
  }, [open, form.type])

  function selectHeaderMode(mode: HeaderMode) {
    setHeaderMode(mode)
    if (mode === 'bearer') {
      setHeaderName('Authorization')
      setHeaderPrefix('Bearer ')
      return
    }
    if (mode === 'apiKey') {
      setHeaderPrefix('')
      setHeaderName((current) => {
        const trimmed = current.trim()
        if (!trimmed || trimmed === 'Authorization') return 'X-API-Key'
        return current
      })
      return
    }
    setHeaderPrefix((current) => (current === 'Bearer ' ? '' : current))
    setHeaderName((current) => (current === 'Authorization' ? '' : current))
  }

  async function handleSaveField(key: string) {
    const value = fieldValues[key]
    if (!editingId || !value?.trim()) return
    setSavingSecret(true)
    try {
      await putSecret(editingId, key, value.trim())
      toast.success('Saved')
      setFieldValues((values) => ({ ...values, [key]: '' }))
      fetchSecretKeys()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to save')
    } finally {
      setSavingSecret(false)
    }
  }

  async function handleSaveAllFields() {
    if (!editingId || !envFields) return
    const toSave = envFields.filter((field) => fieldValues[field.key]?.trim())
    if (toSave.length === 0) return
    setSavingSecret(true)
    try {
      for (const field of toSave) {
        await putSecret(editingId, field.key, fieldValues[field.key].trim())
      }
      toast.success('Credentials saved')
      setFieldValues({})
      fetchSecretKeys()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to save')
    } finally {
      setSavingSecret(false)
    }
  }

  async function handleAddEnvSecret() {
    if (!editingId || !newKey.trim() || !newValue.trim()) return
    setSavingSecret(true)
    try {
      await putSecret(editingId, newKey.trim(), newValue.trim())
      toast.success(`Secret "${newKey.trim()}" saved`)
      setNewKey('')
      setNewValue('')
      setShowValue(false)
      fetchSecretKeys()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to save secret')
    } finally {
      setSavingSecret(false)
    }
  }

  async function handleAddHeader() {
    if (!editingId || !headerDraft.key || !headerDraft.value) return
    setSavingSecret(true)
    try {
      await putSecret(editingId, headerDraft.key, headerDraft.value)
      toast.success(`Header "${headerDraft.key}" saved`)
      setHeaderValue('')
      setShowHeaderValue(false)
      if (headerMode === 'custom') {
        setHeaderPrefix('')
      }
      fetchSecretKeys()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to save header')
    } finally {
      setSavingSecret(false)
    }
  }

  async function handleDeleteSecret(key: string) {
    if (!editingId) return
    try {
      await deleteSecret(editingId, key)
      toast.success(`Secret "${key}" removed`)
      fetchSecretKeys()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to delete secret')
    }
  }

  function addHint() {
    const nextHint = hintInput.trim()
    if (!nextHint) return
    setForm((current) => ({
      ...current,
      redaction_hints: current.redaction_hints.includes(nextHint)
        ? current.redaction_hints
        : [...current.redaction_hints, nextHint],
    }))
    setHintInput('')
  }

  function removeHint(index: number) {
    setForm((current) => ({
      ...current,
      redaction_hints: (current.redaction_hints ?? []).filter((_, currentIndex) => currentIndex !== index),
    }))
  }

  const canSaveDefinition =
    !!form.name.trim() && (form.type !== 'oauth2' || !!form.oauth_provider_id.trim())

  return (
    <Dialog open={open} onOpenChange={() => onClose()}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{editing ? 'Edit Credential' : 'Add Credential'}</DialogTitle>
          <DialogDescription>
            Define how MCPlexer should authenticate, then manage the encrypted secret material
            in the same place.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-5">
          <section className="space-y-4 rounded-md border border-border/50 p-4">
            <SectionHeading
              step="1"
              title="Credential definition"
              description="Pick the auth model first. Header and env credentials become usable as soon as you add secret material."
            />

            <div className="space-y-2">
              <Label className="text-xs text-muted-foreground">Display name</Label>
              <Input
                value={form.display_name}
                onChange={(e) =>
                  setForm((current) => ({ ...current, display_name: e.target.value }))
                }
                placeholder="e.g. ClickUp or Linear"
              />
              <p className="text-[11px] text-muted-foreground">
                Shown in the credentials list and route picker. Leave blank to use a
                tidied version of the slug below.
              </p>
            </div>

            <div className="space-y-2">
              <Label className="text-xs text-muted-foreground">
                Slug
                <span className="ml-2 font-normal text-muted-foreground/70">
                  stable handle for route + secret wiring
                </span>
              </Label>
              <Input
                className="font-mono text-sm"
                value={form.name}
                onChange={(e) => setForm((current) => ({ ...current, name: e.target.value }))}
                placeholder="e.g. github_headers or openai_api_key"
              />
            </div>

            <div className="space-y-2">
              <Label className="text-xs text-muted-foreground">Type</Label>
              <div className="grid gap-2 md:grid-cols-3">
                {credentialTypeOptions.map((option) => (
                  <button
                    key={option.value}
                    type="button"
                    className={`rounded-md border px-3 py-3 text-left transition-colors ${
                      form.type === option.value
                        ? 'border-primary bg-primary/5'
                        : 'border-border/50 hover:border-border hover:bg-muted/40'
                    }`}
                    onClick={() =>
                      setForm((current) => ({
                        ...current,
                        type: option.value,
                        oauth_provider_id: option.value === 'oauth2' ? current.oauth_provider_id : '',
                      }))
                    }
                  >
                    <div className="text-sm font-medium">{option.label}</div>
                    <p className="mt-1 text-xs text-muted-foreground">{option.description}</p>
                  </button>
                ))}
              </div>
            </div>

            {supportsSecretMaterial && !editing && (
              <div className="rounded-md border border-primary/20 bg-primary/5 px-3 py-2 text-sm text-muted-foreground">
                Save this credential definition first. The dialog stays open so you can add
                encrypted secret values immediately after.
              </div>
            )}

            {form.type === 'oauth2' && (
              <div className="space-y-3 rounded-md border border-border/50 p-3">
                <div className="space-y-2">
                  <Label className="text-xs text-muted-foreground">OAuth Provider</Label>
                  <Select
                    value={form.oauth_provider_id}
                    onValueChange={(value) =>
                      setForm((current) => ({ ...current, oauth_provider_id: value }))
                    }
                  >
                    <SelectTrigger>
                      <SelectValue placeholder="Select a provider..." />
                    </SelectTrigger>
                    <SelectContent>
                      {providers.map((provider) => (
                        <SelectItem key={provider.id} value={provider.id}>
                          {provider.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>

                {!editing && (
                  <p className="text-xs text-muted-foreground">
                    Need a provider first? Use{' '}
                    <Link to="/setup" className="text-primary hover:underline" onClick={onClose}>
                      Quick Setup
                    </Link>{' '}
                    for template-based OAuth configuration.
                  </p>
                )}
              </div>
            )}

            <button
              type="button"
              className="flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
              onClick={() => setShowAdvanced((current) => !current)}
            >
              {showAdvanced ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
              Advanced redaction hints
              {!showAdvanced && (form.redaction_hints ?? []).length > 0 && (
                <span className="text-muted-foreground/70">
                  {form.redaction_hints.length} configured
                </span>
              )}
            </button>

            {showAdvanced && (
              <div className="space-y-3 rounded-md border border-border/50 p-3">
                <p className="text-xs text-muted-foreground">
                  Add match patterns for sensitive values that should always be redacted in audit
                  logs.
                </p>
                <div className="flex flex-wrap gap-1">
                  {(form.redaction_hints ?? []).map((hint, index) => (
                    <Badge
                      key={`${hint}-${index}`}
                      variant="outline"
                      className="cursor-pointer font-mono text-xs hover:bg-destructive/10 hover:text-destructive"
                      onClick={() => removeHint(index)}
                    >
                      {hint} x
                    </Badge>
                  ))}
                </div>
                <div className="flex gap-2">
                  <Input
                    className="font-mono text-sm"
                    placeholder="e.g. *token*"
                    value={hintInput}
                    onChange={(e) => setHintInput(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') {
                        e.preventDefault()
                        addHint()
                      }
                    }}
                  />
                  <Button type="button" variant="outline" size="sm" onClick={addHint}>
                    Add
                  </Button>
                </div>
              </div>
            )}
          </section>

          {supportsSecretMaterial && (
            <section className="space-y-4 rounded-md border border-border/50 p-4">
              <SectionHeading
                step="2"
                title="Secret material"
                description={
                  editingId
                    ? `Values are encrypted at rest. After saving, you only see the key names so you can rotate or remove them safely.`
                    : `Save the credential definition first, then add the encrypted secret values here.`
                }
              />

              {editingId ? (
                <>
                  <div className="flex flex-wrap items-center gap-2 rounded-md border border-border/50 bg-muted/30 px-3 py-2">
                    <Badge variant="outline" className="font-mono text-xs">
                      {secretKeys.length} stored
                    </Badge>
                    <p className="text-xs text-muted-foreground">
                      {form.type === 'header'
                        ? 'Headers are injected into every request to the downstream HTTP server.'
                        : form.type === 'hawk'
                          ? 'HAWK_ID and HAWK_KEY are used to sign each outbound HTTP request.'
                        : 'Values are injected into the downstream process environment.'}
                    </p>
                  </div>

                  {isEnvLike && hasLabeledFields && envFields && (
                    <div className="space-y-4">
                      <div className="space-y-3">
                        {envFields.map((field) => {
                          const isSet = secretKeys.includes(field.key)
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
                        disabled={savingSecret || !envFields.some((field) => fieldValues[field.key]?.trim())}
                      >
                        {savingSecret ? 'Saving...' : 'Save Credentials'}
                      </Button>

                      <div className="space-y-3 rounded-md border border-border/50 p-3">
                        <div className="space-y-1">
                          <p className="text-sm font-medium">Additional environment variables</p>
                          <p className="text-xs text-muted-foreground">
                            Use this when the built-in fields are not enough.
                          </p>
                        </div>

                        {extraEnvKeys.length > 0 && (
                          <StoredSecretList keys={extraEnvKeys} onDelete={handleDeleteSecret} />
                        )}

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
                                onKeyDown={(e) => e.key === 'Enter' && handleAddEnvSecret()}
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
                          onClick={handleAddEnvSecret}
                          disabled={savingSecret || !newKey.trim() || !newValue.trim()}
                        >
                          {savingSecret ? 'Saving...' : 'Add Variable'}
                        </Button>
                      </div>
                    </div>
                  )}

                  {isEnvLike && !hasLabeledFields && (
                    <div className="space-y-3">
                      <StoredSecretList keys={secretKeys} onDelete={handleDeleteSecret} />

                      <div className="space-y-3 rounded-md border border-border/50 p-3">
                        <div className="space-y-1">
                          <p className="text-sm font-medium">
                            {form.type === 'hawk' ? 'Add Hawk credential' : 'Add environment variable'}
                          </p>
                          <p className="text-xs text-muted-foreground">
                            {form.type === 'hawk'
                              ? 'Store HAWK_ID and HAWK_KEY. HAWK_ALGORITHM is optional and defaults to sha256.'
                              : 'Variable names are normalized to uppercase to match common MCP server expectations.'}
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
                              placeholder="API_KEY"
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
                                onKeyDown={(e) => e.key === 'Enter' && handleAddEnvSecret()}
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
                          onClick={handleAddEnvSecret}
                          disabled={savingSecret || !newKey.trim() || !newValue.trim()}
                        >
                          {savingSecret ? 'Saving...' : 'Add Variable'}
                        </Button>
                      </div>
                    </div>
                  )}

                  {form.type === 'header' && (
                    <div className="space-y-4">
                      <StoredSecretList keys={secretKeys} onDelete={handleDeleteSecret} />

                      <div className="space-y-3 rounded-md border border-border/50 p-3">
                        <div className="space-y-1">
                          <p className="text-sm font-medium">Add header</p>
                          <p className="text-xs text-muted-foreground">
                            Choose a common auth pattern or drop down to a custom header when you
                            need full control.
                          </p>
                        </div>

                        <div className="grid gap-2 md:grid-cols-3">
                          {headerModeOptions.map((option) => (
                            <button
                              key={option.value}
                              type="button"
                              className={`rounded-md border px-3 py-3 text-left transition-colors ${
                                headerMode === option.value
                                  ? 'border-primary bg-primary/5'
                                  : 'border-border/50 hover:border-border hover:bg-muted/40'
                              }`}
                              onClick={() => selectHeaderMode(option.value)}
                            >
                              <div className="text-sm font-medium">{option.label}</div>
                              <p className="mt-1 text-xs text-muted-foreground">
                                {option.description}
                              </p>
                            </button>
                          ))}
                        </div>

                        {headerMode === 'bearer' && (
                          <div className="space-y-3">
                            <div className="space-y-1">
                              <Label className="text-[11px] text-muted-foreground">Header</Label>
                              <Input value="Authorization" disabled className="font-mono text-sm" />
                            </div>
                            <div className="space-y-1">
                              <Label className="text-[11px] text-muted-foreground">Bearer token</Label>
                              <div className="relative">
                                <Input
                                  className="pr-8 font-mono text-sm"
                                  type={showHeaderValue ? 'text' : 'password'}
                                  value={headerValue}
                                  onChange={(e) => setHeaderValue(e.target.value)}
                                  placeholder="sk-..."
                                  onKeyDown={(e) => e.key === 'Enter' && handleAddHeader()}
                                />
                                <button
                                  type="button"
                                  className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                                  onClick={() => setShowHeaderValue((current) => !current)}
                                >
                                  {showHeaderValue ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                                </button>
                              </div>
                            </div>
                          </div>
                        )}

                        {headerMode === 'apiKey' && (
                          <div className="grid gap-3 md:grid-cols-2">
                            <div className="space-y-1">
                              <Label className="text-[11px] text-muted-foreground">Header name</Label>
                              <Input
                                className="font-mono text-sm"
                                value={headerName}
                                onChange={(e) => setHeaderName(e.target.value)}
                                placeholder="X-API-Key"
                              />
                            </div>
                            <div className="space-y-1">
                              <Label className="text-[11px] text-muted-foreground">Header value</Label>
                              <div className="relative">
                                <Input
                                  className="pr-8 font-mono text-sm"
                                  type={showHeaderValue ? 'text' : 'password'}
                                  value={headerValue}
                                  onChange={(e) => setHeaderValue(e.target.value)}
                                  placeholder="sk-..."
                                  onKeyDown={(e) => e.key === 'Enter' && handleAddHeader()}
                                />
                                <button
                                  type="button"
                                  className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                                  onClick={() => setShowHeaderValue((current) => !current)}
                                >
                                  {showHeaderValue ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                                </button>
                              </div>
                            </div>
                          </div>
                        )}

                        {headerMode === 'custom' && (
                          <div className="space-y-3">
                            <div className="grid gap-3 md:grid-cols-2">
                              <div className="space-y-1">
                                <Label className="text-[11px] text-muted-foreground">Header name</Label>
                                <Input
                                  className="font-mono text-sm"
                                  value={headerName}
                                  onChange={(e) => setHeaderName(e.target.value)}
                                  placeholder="Authorization"
                                />
                              </div>
                              <div className="space-y-1">
                                <Label className="text-[11px] text-muted-foreground">
                                  Optional prefix
                                </Label>
                                <Input
                                  className="font-mono text-sm"
                                  value={headerPrefix}
                                  onChange={(e) => setHeaderPrefix(e.target.value)}
                                  placeholder="Token "
                                />
                              </div>
                            </div>
                            <div className="space-y-1">
                              <Label className="text-[11px] text-muted-foreground">Header value</Label>
                              <div className="relative">
                                <Input
                                  className="pr-8 font-mono text-sm"
                                  type={showHeaderValue ? 'text' : 'password'}
                                  value={headerValue}
                                  onChange={(e) => setHeaderValue(e.target.value)}
                                  placeholder="your-secret"
                                  onKeyDown={(e) => e.key === 'Enter' && handleAddHeader()}
                                />
                                <button
                                  type="button"
                                  className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                                  onClick={() => setShowHeaderValue((current) => !current)}
                                >
                                  {showHeaderValue ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                                </button>
                              </div>
                            </div>
                          </div>
                        )}

                        <div className="rounded-md border border-border/50 bg-muted/30 px-3 py-2">
                          <div className="text-[11px] uppercase tracking-wide text-muted-foreground">
                            Preview
                          </div>
                          <code className="mt-1 block text-xs text-foreground">
                            {headerDraft.preview || 'Header-Name: ••••••'}
                          </code>
                        </div>

                        <Button
                          size="sm"
                          variant="outline"
                          onClick={handleAddHeader}
                          disabled={savingSecret || !headerDraft.key || !headerDraft.value}
                        >
                          {savingSecret ? 'Saving...' : secretKeys.includes(headerDraft.key) ? 'Replace Header' : 'Add Header'}
                        </Button>
                      </div>
                    </div>
                  )}
                </>
              ) : (
                <div className="rounded-md border border-dashed border-border/60 bg-muted/20 px-4 py-5 text-sm text-muted-foreground">
                  Save this credential definition first, then come straight back here to add the
                  encrypted secret values.
                </div>
              )}
            </section>
          )}
        </div>

        {saveError && <p className="text-sm text-destructive">{saveError}</p>}

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={onSave} disabled={saving || !canSaveDefinition}>
            {saving ? 'Saving...' : editing ? 'Save Changes' : 'Save Definition'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
