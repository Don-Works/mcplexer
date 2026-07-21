// MemoryEmbeddingsPage — Settings → Memory → Embeddings. Wires a vector
// provider so recall works by MEANING, not just keywords. Without one,
// recall silently degrades to FTS5 keyword search; this page makes turning
// it on a one-click, in-product action with auto-detection + a visible
// backfill of the existing corpus.
//
// Backs the REST surface in internal/api/memory_embeddings_handler.go:
//   GET  /memory/embeddings/status
//   POST /memory/embeddings/detect | configure | backfill

import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { ArrowLeft, BrainCircuit, Check, Loader2, RefreshCw, Search, Zap } from 'lucide-react'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { toast } from 'sonner'
import {
  backfillEmbeddings,
  configureEmbeddings,
  detectEmbeddings,
  getEmbeddingsStatus,
  type EmbeddingsStatus,
} from '@/api/memory'

type Provider = 'auto' | 'local' | 'openai' | 'none'

export function MemoryEmbeddingsPage() {
  const [status, setStatus] = useState<EmbeddingsStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [provider, setProvider] = useState<Provider>('auto')
  const [baseURL, setBaseURL] = useState('')
  const [model, setModel] = useState('')
  const [openaiKey, setOpenaiKey] = useState('')
  const [busy, setBusy] = useState<'' | 'save' | 'detect' | 'backfill'>('')

  const apply = useCallback((s: EmbeddingsStatus) => {
    setStatus(s)
    setProvider((s.provider as Provider) || 'auto')
    setBaseURL(s.base_url || '')
    setModel(s.model || '')
  }, [])

  const refetch = useCallback(async () => {
    setLoading(true)
    try {
      apply(await getEmbeddingsStatus())
    } catch (err) {
      toast.error('Failed to load embeddings status: ' + String(err))
    } finally {
      setLoading(false)
    }
  }, [apply])

  useEffect(() => {
    void refetch()
  }, [refetch])

  // While a backfill is running, poll so the progress bar advances live.
  useEffect(() => {
    if (!status?.running) return
    const t = setInterval(() => {
      void getEmbeddingsStatus()
        .then(setStatus)
        .catch(() => {})
    }, 2000)
    return () => clearInterval(t)
  }, [status?.running])

  const onDetect = async () => {
    setBusy('detect')
    try {
      const d = await detectEmbeddings()
      if (d.found) {
        setProvider('local')
        setBaseURL(d.base_url)
        setModel(d.model)
        toast.success(`Found a local endpoint: ${d.model} @ ${d.base_url}`)
      } else {
        toast.message('No local embeddings endpoint found', {
          description: 'Start LM Studio / Ollama with an embedding model, or use OpenAI.',
        })
      }
    } catch (err) {
      toast.error('Detect failed: ' + String(err))
    } finally {
      setBusy('')
    }
  }

  const onSave = async () => {
    setBusy('save')
    try {
      const s = await configureEmbeddings({
        provider,
        base_url: baseURL.trim() || undefined,
        model: model.trim() || undefined,
        openai_key: openaiKey.trim() || undefined,
      })
      apply(s)
      setOpenaiKey('')
      toast.success(
        s.embedder_active
          ? 'Embeddings active — backfilling existing memories'
          : 'Saved (no active provider — recall stays keyword-only)',
      )
    } catch (err) {
      toast.error('Save failed: ' + String(err))
    } finally {
      setBusy('')
    }
  }

  const onBackfill = async () => {
    setBusy('backfill')
    try {
      setStatus(await backfillEmbeddings())
      toast.success('Backfill started')
    } catch (err) {
      toast.error('Backfill failed: ' + String(err))
    } finally {
      setBusy('')
    }
  }

  const pct =
    status && status.total > 0 ? Math.round((status.embedded / status.total) * 100) : 0

  return (
    <div className="space-y-5">
      <Link
        to="/memory"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
      >
        <ArrowLeft className="h-3 w-3" />
        Memory
      </Link>
      <header className="space-y-1.5">
        <h1 className="flex items-center gap-2 text-2xl font-semibold tracking-tight">
          <BrainCircuit className="h-5 w-5 text-primary" />
          Embeddings
        </h1>
        <p className="max-w-2xl text-sm text-muted-foreground">
          Semantic recall finds memories by <em>meaning</em>, not just keywords. It needs a
          vector provider — a local model server (LM Studio / Ollama / llama.cpp) or OpenAI.
          Without one, recall falls back to keyword (FTS5) search. Any local model with ≤1536
          dimensions works (smaller vectors are zero-padded, preserving similarity).
        </p>
      </header>

      {loading && (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          Loading…
        </div>
      )}

      {!loading && status && (
        <>
          <Card>
            <CardContent className="space-y-4 p-5">
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <span className="text-[13px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
                    Status
                  </span>
                  <Badge
                    variant="outline"
                    tone={status.embedder_active ? 'success' : 'warn'}
                    className="text-[10px] uppercase tracking-wider"
                  >
                    {status.embedder_active ? 'Vector recall active' : 'Keyword-only'}
                  </Badge>
                </div>
                <Button size="sm" variant="ghost" onClick={() => void refetch()}>
                  <RefreshCw className="mr-1.5 h-3.5 w-3.5" />
                  Refresh
                </Button>
              </div>

              {/* Backfill progress */}
              <div className="space-y-1.5">
                <div className="flex items-center justify-between text-[11px] text-muted-foreground">
                  <span>
                    {status.embedded.toLocaleString()} / {status.total.toLocaleString()} memories
                    embedded
                    {status.running && (
                      <span className="ml-2 inline-flex items-center gap-1 text-primary">
                        <Loader2 className="h-3 w-3 animate-spin" />
                        backfilling…
                      </span>
                    )}
                  </span>
                  <span className="font-mono tabular-nums">{pct}%</span>
                </div>
                <div className="h-1.5 w-full overflow-hidden rounded-full bg-border/50">
                  <div
                    className="h-full bg-primary transition-all duration-500"
                    style={{ width: `${pct}%` }}
                  />
                </div>
                {status.pending > 0 && status.embedder_active && !status.running && (
                  <Button size="sm" variant="ghost" onClick={onBackfill} disabled={busy !== ''}>
                    {busy === 'backfill' ? (
                      <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                    ) : (
                      <Zap className="mr-1.5 h-3.5 w-3.5" />
                    )}
                    Backfill {status.pending.toLocaleString()} remaining
                  </Button>
                )}
              </div>
            </CardContent>
          </Card>

          {/* Provider configuration */}
          <Card>
            <CardContent className="space-y-4 p-5">
              <span className="text-[13px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
                Provider
              </span>

              <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                {(['auto', 'local', 'openai', 'none'] as Provider[]).map((p) => (
                  <button
                    key={p}
                    type="button"
                    onClick={() => setProvider(p)}
                    className={
                      'border px-3 py-2 text-left text-xs transition-colors ' +
                      (provider === p
                        ? 'border-primary/60 bg-primary/10 text-foreground'
                        : 'border-border text-muted-foreground hover:border-primary/40 hover:text-foreground')
                    }
                  >
                    <div className="flex items-center gap-1.5 font-semibold capitalize">
                      {provider === p && <Check className="h-3 w-3 text-primary" />}
                      {p}
                    </div>
                    <div className="mt-0.5 text-[10px] text-muted-foreground/70">
                      {p === 'auto'
                        ? 'Detect a local endpoint'
                        : p === 'local'
                          ? 'LM Studio / Ollama'
                          : p === 'openai'
                            ? 'text-embedding-3-small'
                            : 'Keyword-only'}
                    </div>
                  </button>
                ))}
              </div>

              {(provider === 'auto' || provider === 'local') && (
                <div className="space-y-2">
                  <Button size="sm" variant="ghost" onClick={onDetect} disabled={busy !== ''}>
                    {busy === 'detect' ? (
                      <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                    ) : (
                      <Search className="mr-1.5 h-3.5 w-3.5" />
                    )}
                    Auto-detect local endpoint
                  </Button>
                  {provider === 'local' && (
                    <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                      <Field label="Base URL" value={baseURL} onChange={setBaseURL} placeholder="http://localhost:1234/v1" />
                      <Field label="Model" value={model} onChange={setModel} placeholder="text-embedding-…" />
                    </div>
                  )}
                </div>
              )}

              {provider === 'openai' && (
                <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                  <Field
                    label="API key"
                    value={openaiKey}
                    onChange={setOpenaiKey}
                    placeholder={status.embedder_active ? '•••• (set)' : 'sk-…'}
                    type="password"
                  />
                  <Field label="Model (optional)" value={model} onChange={setModel} placeholder="text-embedding-3-small" />
                  <p className="col-span-full text-[10px] text-muted-foreground/70">
                    The key is used live and never stored in the settings file — re-enter it after
                    a restart, or set <span className="font-mono">MCPLEXER_OPENAI_API_KEY</span>.
                  </p>
                </div>
              )}

              <div className="flex items-center gap-2 border-t border-border/40 pt-3">
                <Button size="sm" onClick={onSave} disabled={busy !== ''}>
                  {busy === 'save' ? (
                    <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                  ) : (
                    <Check className="mr-1.5 h-3.5 w-3.5" />
                  )}
                  Save &amp; apply
                </Button>
                <span className="text-[11px] text-muted-foreground">
                  Applies live — no restart needed. Existing memories backfill automatically.
                </span>
              </div>
            </CardContent>
          </Card>
        </>
      )}
    </div>
  )
}

function Field({
  label,
  value,
  onChange,
  placeholder,
  type = 'text',
}: {
  label: string
  value: string
  onChange: (v: string) => void
  placeholder?: string
  type?: string
}) {
  return (
    <label className="block space-y-1">
      <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/70">
        {label}
      </span>
      <input
        type={type}
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        className="w-full border border-border bg-card/40 px-2.5 py-1.5 font-mono text-xs text-foreground outline-none transition-colors focus:border-primary/50"
      />
    </label>
  )
}
