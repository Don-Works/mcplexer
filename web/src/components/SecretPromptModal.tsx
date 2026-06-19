import { useState } from 'react'
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
import { useSecretPromptStream, type SecretPrompt } from '@/hooks/use-secret-prompt-stream'
import { toast } from 'sonner'
import { ShieldAlert } from 'lucide-react'

const API_BASE = import.meta.env.VITE_API_BASE_URL || '/api/v1'

async function submitSecret(id: string, value: string): Promise<void> {
  const res = await fetch(`${API_BASE}/secrets/prompts/${id}/submit`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ value }),
  })
  if (!res.ok) throw new Error(await res.text())
}

async function cancelSecret(id: string): Promise<void> {
  const res = await fetch(`${API_BASE}/secrets/prompts/${id}/cancel`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: '{}',
  })
  if (!res.ok) throw new Error(await res.text())
}

interface PromptFormProps {
  prompt: SecretPrompt
  onResolved: () => void
}

function PromptForm({ prompt, onResolved }: PromptFormProps) {
  const [value, setValue] = useState('')
  const [busy, setBusy] = useState(false)

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!value) {
      toast.error('Secret value is required')
      return
    }
    setBusy(true)
    try {
      await submitSecret(prompt.id, value)
      // Wipe local state immediately. The agent now has a path; we no
      // longer need the value in this tab's memory.
      setValue('')
      toast.success('Secret submitted')
      onResolved()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Submit failed')
    } finally {
      setBusy(false)
    }
  }

  async function handleCancel() {
    setBusy(true)
    try {
      await cancelSecret(prompt.id)
      setValue('')
      onResolved()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Cancel failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div className="rounded-md border border-amber-500/30 bg-amber-500/5 p-3 text-xs text-amber-300">
        <div className="flex items-start gap-2">
          <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0" />
          <p>
            Pasted value is sent over the local API and written to a 0600 file
            owned by the daemon. The agent receives only the file path, never
            the value.
          </p>
        </div>
      </div>
      <div className="space-y-1">
        <Label className="text-xs uppercase tracking-wider text-muted-foreground">
          Reason
        </Label>
        <p className="text-sm">{prompt.reason}</p>
      </div>
      <div className="space-y-1">
        <Label htmlFor={`secret-${prompt.id}`}>{prompt.label}</Label>
        <Input
          id={`secret-${prompt.id}`}
          type="password"
          autoFocus
          autoComplete="off"
          spellCheck={false}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder="Paste secret value"
        />
      </div>
      <DialogFooter>
        <Button type="button" variant="outline" onClick={handleCancel} disabled={busy} data-testid="secret-cancel">
          Cancel
        </Button>
        <Button type="submit" disabled={busy || value === ''} data-testid="secret-submit">
          Submit
        </Button>
      </DialogFooter>
    </form>
  )
}

// SecretPromptModal renders a dialog whenever the manager has at least one
// pending prompt. The modal is non-dismissable via outside-click — the user
// must explicitly Submit or Cancel so the agent's RPC unblocks.
export function SecretPromptModal() {
  const { pending } = useSecretPromptStream()
  const head = pending[0]

  if (!head) return null

  return (
    <Dialog open onOpenChange={() => undefined}>
      <DialogContent showCloseButton={false} onEscapeKeyDown={(e) => e.preventDefault()} onPointerDownOutside={(e) => e.preventDefault()}>
        <DialogHeader>
          <DialogTitle>Secret requested</DialogTitle>
          <DialogDescription>
            An agent is asking for a secret. Submit the value to the daemon
            (the agent will only see a file path) or cancel to abort the call.
          </DialogDescription>
        </DialogHeader>
        <PromptForm prompt={head} onResolved={() => undefined} />
        {pending.length > 1 && (
          <p className="mt-2 text-xs text-muted-foreground">
            +{pending.length - 1} more pending after this one
          </p>
        )}
      </DialogContent>
    </Dialog>
  )
}
