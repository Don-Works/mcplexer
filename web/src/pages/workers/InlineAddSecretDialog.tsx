// InlineAddSecretDialog — the "+ Paste new key" flow on the Model card.
// Creates an AuthScope (type=env) and seeds it with key=api_key in one
// shot, so a brand-new user can wire Anthropic / OpenAI / Minimax /
// OpenRouter without leaving the editor.
//
// The dialog never shows the key after submit — once the daemon
// encrypts it, the value is gone from the wire and the UI.

import { useState } from 'react'
import { toast } from 'sonner'
import { Loader2, KeyRound } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { createAuthScope, putSecret } from '@/api/client'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  // Called with the new scope id once both the scope and the secret
  // have been persisted. Editor uses this to select the new scope in
  // the dropdown without a re-fetch round-trip.
  onCreated: (scopeID: string, scopeName: string) => void
  // Optional default scope name — when the provider is known we can
  // pre-fill ("Anthropic", "OpenRouter", etc.).
  defaultName?: string
}

const PROVIDER_HINTS = [
  { id: 'anthropic', label: 'Anthropic', placeholder: 'sk-ant-…' },
  { id: 'openai', label: 'OpenAI', placeholder: 'sk-…' },
  { id: 'openrouter', label: 'OpenRouter', placeholder: 'sk-or-v1-…' },
  { id: 'minimax', label: 'Minimax', placeholder: 'eyJ…' },
  { id: 'custom', label: 'Other', placeholder: 'paste your API key' },
]

export function InlineAddSecretDialog({ open, onOpenChange, onCreated, defaultName }: Props) {
  const [name, setName] = useState(defaultName ?? '')
  const [apiKey, setApiKey] = useState('')
  const [hint, setHint] = useState<string>('anthropic')
  const [saving, setSaving] = useState(false)

  const placeholder = PROVIDER_HINTS.find((p) => p.id === hint)?.placeholder ?? ''

  function reset() {
    setName(defaultName ?? '')
    setApiKey('')
    setHint('anthropic')
  }

  async function handleSubmit() {
    const trimmedName = name.trim()
    const trimmedKey = apiKey.trim()
    if (!trimmedName) {
      toast.error('Scope name is required')
      return
    }
    if (!trimmedKey) {
      toast.error('API key is required')
      return
    }
    const scopeID = slugify(trimmedName)
    setSaving(true)
    try {
      // The store accepts a caller-provided id; we slugify the name
      // so the dropdown shows readable ids. Conflicts surface as a
      // 409 from the daemon — we show the message.
      await createAuthScope({
        id: scopeID,
        name: trimmedName,
        display_name: '',
        type: 'env',
        oauth_provider_id: '',
        redaction_hints: [],
      } as never)
      await putSecret(scopeID, 'api_key', trimmedKey)
      toast.success(`Created ${trimmedName}`)
      onCreated(scopeID, trimmedName)
      reset()
      onOpenChange(false)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : 'Failed to create scope')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <KeyRound className="h-4 w-4" /> Paste a new API key
          </DialogTitle>
          <DialogDescription>
            Creates an env-type AuthScope and stores the value as <code className="text-[10px]">api_key</code>.
            The Worker runner reads exactly this key name.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="ak-provider" className="text-xs">Provider hint</Label>
            <select
              id="ak-provider"
              value={hint}
              onChange={(e) => setHint(e.target.value)}
              className="h-8 w-full rounded border border-border/60 bg-background px-2 text-xs"
            >
              {PROVIDER_HINTS.map((p) => (
                <option key={p.id} value={p.id}>{p.label}</option>
              ))}
            </select>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="ak-name" className="text-xs">Scope name</Label>
            <Input
              id="ak-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="My Anthropic key"
              data-testid="inline-secret-name"
            />
            <p className="text-[10px] text-muted-foreground/70">
              Stored as id <code className="font-mono">{slugify(name || 'my-key')}</code>.
            </p>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="ak-key" className="text-xs">API key</Label>
            <Input
              id="ak-key"
              type="password"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              placeholder={placeholder}
              data-testid="inline-secret-value"
              className="font-mono text-xs"
            />
            <p className="text-[10px] text-muted-foreground/70">
              Encrypted at rest with age. Never re-displayed after save.
            </p>
          </div>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={saving}>
            Cancel
          </Button>
          <Button onClick={handleSubmit} disabled={saving} data-testid="inline-secret-save">
            {saving ? <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" /> : null}
            Save & select
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function slugify(s: string): string {
  return s
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9-]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 64) || 'scope'
}
