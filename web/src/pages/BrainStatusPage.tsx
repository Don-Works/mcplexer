import { useCallback, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { useApi } from '@/hooks/use-api'
import {
  getBrainStatus,
  listBrainErrors,
  pushBrain,
  syncBrain,
} from '@/api/brain'
import type { BrainVerifyResult } from '@/api/brain'
import {
  AlertTriangle,
  Brain,
  CheckCircle2,
  CloudUpload,
  ExternalLink,
  GitBranch,
  Loader2,
  RefreshCw,
} from 'lucide-react'
import { toast } from 'sonner'

export function BrainStatusPage() {
  const statusFetcher = useCallback(() => getBrainStatus(), [])
  const { data: status, loading, error, refetch } = useApi(statusFetcher)

  const errorsFetcher = useCallback(() => listBrainErrors(), [])
  const { data: errors, refetch: refetchErrors } = useApi(errorsFetcher)

  const [pushing, setPushing] = useState(false)
  const [syncing, setSyncing] = useState(false)
  const [verify, setVerify] = useState<BrainVerifyResult | null>(null)

  async function handlePush() {
    setPushing(true)
    try {
      const res = await pushBrain()
      if (res.conflict) {
        toast.error('Push hit a rebase conflict — resolve in VSCode, commit, then push again.', {
          duration: 12000,
        })
      } else {
        toast.success('Brain pushed to origin.')
      }
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Push failed')
    } finally {
      setPushing(false)
    }
  }

  async function handleSync() {
    setSyncing(true)
    try {
      const res = await syncBrain()
      setVerify(res)
      if (res.ok) {
        toast.success(`Verified ${res.files_checked} files — no drift.`)
      } else {
        toast.warning(`Found ${res.drifts.length} drift(s) across ${res.files_checked} files.`)
      }
      refetchErrors()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Verify failed')
    } finally {
      setSyncing(false)
    }
  }

  const vscodeURL = status?.dir ? `vscode://file/${status.dir}` : undefined

  return (
    <div className="max-w-4xl space-y-5">
      <div>
        <h1 className="flex items-center gap-2 text-2xl font-bold">
          <Brain className="h-6 w-6" /> Brain
        </h1>
        <p className="mt-1 text-sm text-muted-foreground">
          The git-backed, Markdown-canonical state repository. Tasks, memories, workspace config and skills
          live as editable <span className="font-mono text-xs">.md</span> files that the gateway indexes into
          SQLite. Commits happen automatically on idle; push is manual (deploy-hygiene). Agents use{' '}
          <span className="font-mono text-xs">mcplexer__brain_init</span> /{' '}
          <span className="font-mono text-xs">brain_import</span> /{' '}
          <span className="font-mono text-xs">brain_status</span> for the same flow.
        </p>
      </div>

      {loading && !status && (
        <div className="flex items-center gap-2 text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" /> Loading brain status...
        </div>
      )}
      {error && <p className="text-destructive">Error: {error}</p>}

      {status && !status.enabled && (
        <Card>
          <CardContent className="flex flex-col items-center justify-center py-10 text-center">
            <Brain className="mb-2 h-8 w-8 text-muted-foreground/40" />
            <h3 className="text-sm font-semibold">Brain is disabled</h3>
            <p className="mt-1 max-w-md text-sm text-muted-foreground">
              Enable it with <span className="font-mono text-xs">MCPLEXER_BRAIN_ENABLED=1</span> (or
              settings.brain_enabled), restart the daemon, then run{' '}
              <span className="font-mono text-xs">mcplexer__brain_init</span> +{' '}
              <span className="font-mono text-xs">brain_import</span> from a terminal under{' '}
              <span className="font-mono text-xs">~/.mcplexer</span>.
            </p>
          </CardContent>
        </Card>
      )}

      {status && status.enabled && (
        <>
          <Card>
            <CardContent className="space-y-3 p-4">
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <GitBranch className="h-4 w-4 text-muted-foreground" />
                  <span className="text-sm font-medium">Repository</span>
                </div>
                <div className="flex gap-2">
                  <Button variant="outline" size="sm" onClick={() => refetch()}>
                    <RefreshCw className="mr-1.5 h-3.5 w-3.5" /> Refresh
                  </Button>
                  {vscodeURL && (
                    <Button variant="outline" size="sm" asChild>
                      <a href={vscodeURL}>
                        <ExternalLink className="mr-1.5 h-3.5 w-3.5" /> Open in VSCode
                      </a>
                    </Button>
                  )}
                </div>
              </div>

              <p className="font-mono text-xs text-muted-foreground">{status.dir}</p>

              {status.git_error && (
                <p className="text-sm text-destructive">git: {status.git_error}</p>
              )}

              {status.git && (
                <div className="flex flex-wrap items-center gap-2">
                  {status.git.initialized ? (
                    <Badge variant="outline" tone="mono">{status.git.branch || 'detached'}</Badge>
                  ) : (
                    <Badge variant="outline">not initialised</Badge>
                  )}
                  {status.git.dirty ? (
                    <Badge variant="outline" tone="warn">
                      uncommitted changes
                    </Badge>
                  ) : (
                    <Badge variant="outline" tone="success">clean</Badge>
                  )}
                  {status.git.has_upstream && (
                    <>
                      <Badge variant="outline" tone="info">↑ {status.git.ahead} ahead</Badge>
                      <Badge variant="outline" tone="info">↓ {status.git.behind} behind</Badge>
                    </>
                  )}
                  {!status.git.has_remote && <Badge variant="outline">no remote</Badge>}
                </div>
              )}

              {status.git?.last_commit && (
                <p className="text-xs text-muted-foreground">
                  Last commit: <span className="font-mono">{status.git.last_commit}</span>
                </p>
              )}

              <div className="flex gap-2 pt-1">
                <Button
                  size="sm"
                  onClick={handlePush}
                  disabled={pushing || !status.git?.has_remote}
                  title={status.git?.has_remote ? 'Pull --rebase --autostash, then push' : 'No remote configured'}
                >
                  {pushing ? (
                    <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                  ) : (
                    <CloudUpload className="mr-1.5 h-3.5 w-3.5" />
                  )}
                  Push
                </Button>
                <Button size="sm" variant="outline" onClick={handleSync} disabled={syncing}>
                  {syncing ? (
                    <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                  ) : (
                    <RefreshCw className="mr-1.5 h-3.5 w-3.5" />
                  )}
                  Verify (check drift)
                </Button>
              </div>
            </CardContent>
          </Card>

          {verify && (
            <Card>
              <CardContent className="space-y-2 p-4">
                <div className="flex items-center gap-2 text-sm font-medium">
                  {verify.ok ? (
                    <CheckCircle2 className="h-4 w-4 text-emerald-500" />
                  ) : (
                    <AlertTriangle className="h-4 w-4 text-amber-500" />
                  )}
                  Verify — {verify.files_checked} files checked
                </div>
                {verify.ok ? (
                  <p className="text-sm text-muted-foreground">Index faithfully reflects the files. No drift.</p>
                ) : (
                  <ul className="space-y-1 text-xs">
                    {verify.drifts.map((d, i) => (
                      <li key={i} className="font-mono">
                        <Badge variant="outline" className="mr-1.5">
                          {d.kind}
                        </Badge>
                        {d.path}
                        {d.detail ? ` — ${d.detail}` : ''}
                      </li>
                    ))}
                  </ul>
                )}
              </CardContent>
            </Card>
          )}

          <Card>
            <CardContent className="space-y-2 p-4">
              <div className="flex items-center gap-2 text-sm font-medium">
                <AlertTriangle className="h-4 w-4 text-muted-foreground" />
                Validation errors
                {status.error_count > 0 && (
                  <Badge variant="destructive">{status.error_count}</Badge>
                )}
              </div>
              {!errors || errors.length === 0 ? (
                <p className="text-sm text-muted-foreground">
                  No files failed validation. A file that fails frontmatter validation is NOT indexed (an index
                  that lies is worse than a missing record).
                </p>
              ) : (
                <ul className="space-y-1.5">
                  {errors.map((e) => (
                    <li key={e.id} className="rounded border border-destructive/30 bg-destructive/5 p-2 text-xs">
                      <span className="font-mono">{e.path}</span>
                      <div className="mt-0.5 text-muted-foreground">
                        {e.field ? `${e.field}: ` : ''}
                        {e.reason}
                      </div>
                    </li>
                  ))}
                </ul>
              )}
            </CardContent>
          </Card>
        </>
      )}
    </div>
  )
}
