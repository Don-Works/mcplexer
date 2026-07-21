// WaitingSecretsSection surfaces secret__prompt requests an agent is blocked
// on, alongside tool approvals. Display-only: the secret is still provided
// through the native secret prompt — this panel gives the human visibility
// plus a live countdown to the auto-expiry (defaultPromptTimeout, 2m).
import { AlertTriangle, KeyRound } from 'lucide-react'
import type { SecretPrompt } from '@/hooks/use-secret-prompt-stream'

function remaining(
  expiresAt: string,
  now: number,
): { label: string; urgent: boolean; expired: boolean } {
  if (!expiresAt) return { label: '', urgent: false, expired: false }
  const ms = new Date(expiresAt).getTime() - now
  if (Number.isNaN(ms)) return { label: '', urgent: false, expired: false }
  if (ms <= 0) return { label: 'expired', urgent: true, expired: true }
  const total = Math.floor(ms / 1000)
  const m = Math.floor(total / 60)
  const s = total % 60
  return {
    label: `${m}:${String(s).padStart(2, '0')}`,
    urgent: total <= 20,
    expired: false,
  }
}

interface Props {
  prompts: SecretPrompt[]
  now: number
}

export function WaitingSecretsSection({ prompts, now }: Props) {
  if (prompts.length === 0) return null
  return (
    <section className="space-y-3" data-testid="waiting-secrets">
      <h2 className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
        Waiting on secrets ({prompts.length})
      </h2>
      <div className="grid gap-4 md:grid-cols-2">
        {prompts.map((p) => {
          const r = remaining(p.expires_at, now)
          return (
            <div
              key={p.id}
              className="space-y-2 rounded-lg border border-amber-500/30 bg-card p-4"
            >
              <div className="flex items-center justify-between gap-3">
                <div className="flex min-w-0 items-center gap-2">
                  <KeyRound className="h-4 w-4 shrink-0 text-amber-400" />
                  <span className="truncate font-mono text-sm">{p.label || p.id}</span>
                </div>
                {r.label && (
                  <span
                    className={`flex shrink-0 items-center gap-1 text-xs tabular-nums ${
                      r.urgent ? 'text-red-400' : 'text-muted-foreground'
                    }`}
                  >
                    {r.urgent && <AlertTriangle aria-hidden="true" className="h-3.5 w-3.5" />}
                    {r.expired ? r.label : `${r.urgent ? 'Urgent: ' : ''}${r.label} left`}
                  </span>
                )}
              </div>
              {p.reason && <p className="text-sm text-muted-foreground">{p.reason}</p>}
              {p.requester && (
                <p className="text-xs text-muted-foreground">Requested by {p.requester}</p>
              )}
              <p className="text-xs text-muted-foreground">
                An agent is blocked waiting for this secret. Provide it in the secret prompt
                when it appears; it auto-expires when the countdown ends.
              </p>
            </div>
          )
        })}
      </div>
    </section>
  )
}
