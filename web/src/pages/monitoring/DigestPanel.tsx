// DigestPanel — the exact budget-bounded text the log-watch worker
// reads. What you preview here is what the model gets: counts ×
// templates, never raw logs.
import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { toast } from 'sonner'
import { RefreshCw } from 'lucide-react'
import { fetchDigest } from '@/api/monitoring'

const WINDOWS = ['15m', '1h', '6h', '24h']
const BUDGETS = [500, 1000, 2000, 4000]

export function DigestPanel({ workspaceId }: { workspaceId: string }) {
  const [window, setWindow] = useState('15m')
  const [budget, setBudget] = useState(2000)
  const [digest, setDigest] = useState('')
  const [tokens, setTokens] = useState(0)
  const [loading, setLoading] = useState(false)

  async function load() {
    setLoading(true)
    try {
      const res = await fetchDigest(workspaceId, window, budget)
      setDigest(res.digest)
      setTokens(res.approx_tokens)
    } catch (e) {
      toast.error(String(e))
    } finally {
      setLoading(false)
    }
  }

  return (
    <section>
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-medium uppercase tracking-wide text-muted-foreground">
          Digest preview
        </h2>
        <div className="flex items-center gap-1">
          {WINDOWS.map(w => (
            <button key={w}
              className={
                'border px-1.5 py-0.5 text-xs ' +
                (window === w ? 'border-primary/60 bg-accent' : 'border-border text-muted-foreground hover:bg-muted')
              }
              onClick={() => setWindow(w)}>
              {w}
            </button>
          ))}
          <span className="mx-1 text-xs text-muted-foreground">budget</span>
          {BUDGETS.map(b => (
            <button key={b}
              className={
                'border px-1.5 py-0.5 text-xs font-mono ' +
                (budget === b ? 'border-primary/60 bg-accent' : 'border-border text-muted-foreground hover:bg-muted')
              }
              onClick={() => setBudget(b)}>
              {b}
            </button>
          ))}
          <Button size="sm" variant="ghost" onClick={load} disabled={loading}>
            <RefreshCw className={'h-4 w-4' + (loading ? ' animate-spin' : '')} />
          </Button>
        </div>
      </div>
      {digest ? (
        <div className="mt-2 border border-border">
          <div className="border-b border-border bg-muted/40 px-3 py-1 text-[10px] uppercase tracking-wide text-muted-foreground">
            what the worker reads · ~{tokens} tokens of a {budget}-token budget
          </div>
          <pre className="max-h-80 overflow-auto p-3 font-mono text-xs leading-relaxed">
            {digest}
          </pre>
        </div>
      ) : (
        <p className="mt-2 text-sm text-muted-foreground">
          Render the window to see exactly what a triage worker would read instead of raw logs.
        </p>
      )}
    </section>
  )
}
