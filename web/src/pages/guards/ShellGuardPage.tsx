import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useApi } from '@/hooks/use-api'
import {
  getShellGuardDetail,
  installShellHooks,
  uninstallShellHooks,
  listApprovalRules,
  createApprovalRule,
  deleteApprovalRule,
  type ApprovalRule,
} from '@/api/client'
import type { MCPClient } from '@/api/types'
import { AlertTriangle, ArrowLeft, Loader2, Terminal, Trash2 } from 'lucide-react'
import { toast } from 'sonner'

// Today only claude_code is wired through HookInstaller; we'll surface
// every detected client but mark the others as "future support" so
// users know what to expect.
const SUPPORTED_HOOK_CLIENTS = new Set(['claude_code'])

// anyDrifted returns true when at least one client has hooks_installed
// AND hooks_drifted set. Drives the top-level Cooperative-mode badge —
// a single drifted client should flip the whole header to red so the
// operator can't miss it on the dashboard.
function anyDrifted(clients: MCPClient[]): boolean {
  return clients.some((c) => c.hooks_installed && c.hooks_drifted)
}

export function ShellGuardPage() {
  const fetcher = useCallback(() => getShellGuardDetail(), [])
  const { data, loading, error, refetch } = useApi(fetcher)
  const [busy, setBusy] = useState<string | null>(null)

  async function handleInstall(clientId: string) {
    setBusy(clientId)
    try {
      const res = await installShellHooks(clientId)
      toast.success(res.installed ? 'Hooks installed' : 'Already installed')
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Install failed')
    } finally {
      setBusy(null)
    }
  }

  async function handleUninstall(clientId: string) {
    setBusy(clientId)
    try {
      await uninstallShellHooks(clientId)
      toast.success('Hooks removed')
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Uninstall failed')
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="space-y-5 max-w-5xl">
      <Link
        to="/guards"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
      >
        <ArrowLeft className="h-3 w-3" />
        Guards
      </Link>
      <div>
        <h1 className="text-2xl font-bold flex items-center gap-2">
          <Terminal className="h-6 w-6" /> Shell Guard
        </h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Intercepts shell commands AI clients try to run, validates them, and
          gates them behind a human-in-the-loop approval. Today this covers
          Claude Code's PreToolUse Bash hook; other clients land in M1-F+.
        </p>
      </div>

      {loading && !data ? (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" /> Loading shell guard…
        </div>
      ) : error ? (
        <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
          {error}
        </div>
      ) : data ? (
        <>
          <Card>
            <CardContent className="flex items-center justify-between gap-4 p-4">
              <div>
                <div className="text-sm font-medium">Cooperative mode</div>
                <div className="text-xs text-muted-foreground">
                  {/* When any client is drifted we replace the green check
                      with a red call-to-action so the operator knows
                      audit_records is silently empty even though the row
                      still says installed. */}
                  {anyDrifted(data.clients)
                    ? 'Hook drifted — settings.json no longer references mcplexer. Re-install below to repair.'
                    : 'Install the curl hook into client config so commands route through MCPlexer.'}
                </div>
              </div>
              <Badge
                className={
                  anyDrifted(data.clients)
                    ? 'bg-rose-500/10 text-rose-400 border-rose-500/30'
                    : data.hooks_enabled
                      ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30'
                      : 'bg-muted text-muted-foreground'
                }
                data-testid="shell-mode-badge"
              >
                {anyDrifted(data.clients)
                  ? 'drifted'
                  : data.hooks_enabled
                    ? 'active'
                    : 'inactive'}
              </Badge>
            </CardContent>
          </Card>

          <section className="space-y-2">
            <h2 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
              Clients
            </h2>
            <Card>
              <CardContent className="p-0">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Client</TableHead>
                      <TableHead>Detected</TableHead>
                      <TableHead>Hook</TableHead>
                      <TableHead className="text-right">Action</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {data.clients.map((c) => (
                      <ClientRow
                        key={c.id}
                        client={c}
                        busy={busy === c.id}
                        onInstall={() => handleInstall(c.id)}
                        onUninstall={() => handleUninstall(c.id)}
                      />
                    ))}
                    {data.clients.length === 0 && (
                      <TableRow>
                        <TableCell colSpan={4} className="text-center text-sm text-muted-foreground">
                          No MCP clients detected on this machine.
                        </TableCell>
                      </TableRow>
                    )}
                  </TableBody>
                </Table>
              </CardContent>
            </Card>
          </section>

          <ApprovalRulesSection />

          <section className="space-y-2">
            <h2 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
              Recent shell approvals
            </h2>
            {data.recent_approvals.length === 0 ? (
              <Card>
                <CardContent className="p-6 text-center text-sm text-muted-foreground">
                  No pending shell approvals. New requests will appear here.
                </CardContent>
              </Card>
            ) : (
              <Card>
                <CardContent className="p-0">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Tool</TableHead>
                        <TableHead>Session</TableHead>
                        <TableHead>Status</TableHead>
                        <TableHead>Created</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {data.recent_approvals.map((a) => (
                        <TableRow key={a.id}>
                          <TableCell className="font-mono text-xs">{a.tool_name}</TableCell>
                          <TableCell className="font-mono text-xs">
                            {a.request_session_id || '—'}
                          </TableCell>
                          <TableCell>
                            <Badge variant="secondary" className="text-[10px]">
                              {a.status}
                            </Badge>
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground">
                            {new Date(a.created_at).toLocaleTimeString()}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </CardContent>
              </Card>
            )}
          </section>
        </>
      ) : null}
    </div>
  )
}

// isWildcardRule returns true for a rule that matches every shell command:
// surface="shell", pattern="*", decision="allow". The UI surfaces these
// with an amber warning chip so the user understands the trust shift.
function isWildcardRule(r: ApprovalRule): boolean {
  return r.surface === 'shell' && r.pattern === '*' && r.decision === 'allow'
}

// ApprovalRulesSection lists every shell-surface allowlist rule plus
// an inline "+ rule" form. Rules drive the manager's PolicyResolver:
// any pending approval whose surface+pattern (and optional cwd) match
// auto-decides after the 5s grace period instead of waiting for a
// human prompt. Hot-reload: the backend's CRUD handler calls
// ReloadPolicyRules after every write so changes take effect without
// restart. We keep this scoped to surface="shell" because that's the
// only surface today that routinely creates approvals.
function ApprovalRulesSection() {
  const [rules, setRules] = useState<ApprovalRule[]>([])
  const [loading, setLoading] = useState(true)
  const [pattern, setPattern] = useState('')
  const [directory, setDirectory] = useState('')
  const [decision, setDecision] = useState<'allow' | 'deny'>('allow')
  const [priority, setPriority] = useState(100)
  const [submitting, setSubmitting] = useState(false)
  const [allowAllBusy, setAllowAllBusy] = useState(false)
  const [allowMetachars, setAllowMetachars] = useState(false)

  const reload = useCallback(async () => {
    setLoading(true)
    try {
      const res = await listApprovalRules('shell')
      setRules(res.rules ?? [])
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to load rules')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void reload()
  }, [reload])

  async function handleCreate(e: React.FormEvent) {
    e.preventDefault()
    if (!pattern.trim()) {
      toast.error('Pattern required (e.g. shell:git or shell:*)')
      return
    }
    setSubmitting(true)
    try {
      await createApprovalRule({
        surface: 'shell',
        pattern: pattern.trim(),
        directory: directory.trim(),
        ai_session_id: '',
        decision,
        priority,
        created_by: 'dashboard',
        allow_metachars: decision === 'allow' ? allowMetachars : false,
      })
      setPattern('')
      setDirectory('')
      setAllowMetachars(false)
      toast.success(`Rule added — ${decision} ${pattern.trim()}`)
      void reload()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Create failed')
    } finally {
      setSubmitting(false)
    }
  }

  async function handleDelete(id: string) {
    try {
      await deleteApprovalRule(id)
      toast.success('Rule removed')
      void reload()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Delete failed')
    }
  }

  // handleInstallAllowAll creates the wildcard "allow + audit everything" rule.
  // It posts surface="shell", pattern="*", decision="allow", priority=999 so
  // every shell command auto-approves. The audit trail still fires — the
  // approver_session_id will be "rule:<id>" so you can see which rule fired
  // for each auto-approved command in the Approvals history.
  async function handleInstallAllowAll() {
    setAllowAllBusy(true)
    try {
      await createApprovalRule({
        surface: 'shell',
        pattern: '*',
        directory: '',
        ai_session_id: '',
        decision: 'allow',
        priority: 999,
        created_by: 'ui-allow-all',
        // The whole point of this one-click button is "every shell
        // command, including multi-step ones". Without allow_metachars
        // the cheap-block in the /v1/hooks/pretool path kills
        // `ssh host 'a | b'`, `cmd 2>&1`, `a; b` etc. BEFORE the rule
        // can fire — the user installs the wildcard expecting "allow
        // everything" and gets "allow simple single commands". Always
        // setting this for the wildcard makes the button honour its
        // name. Narrower than dangerous-mode: protected paths,
        // downstream-config checks, and audit logging still apply.
        allow_metachars: true,
      })
      toast.success('Allow-all rule installed — every shell command now auto-approves and audits.')
      void reload()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to install allow-all rule')
    } finally {
      setAllowAllBusy(false)
    }
  }

  const wildcardRule = rules.find(isWildcardRule)

  return (
    <section className="space-y-2">
      <h2 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
        Auto-approve rules
      </h2>
      <Card>
        <CardContent className="space-y-4 p-4">
          <p className="text-xs text-muted-foreground">
            Tool-name patterns whose approvals are auto-decided after a 5s
            grace period. Use <code className="font-mono">shell:git</code> to
            match git commands, <code className="font-mono">shell:*</code> for
            anything. Restrict to one project by setting Directory.
          </p>

          {/* Wildcard active warning chip — shown when the allow-all rule exists */}
          {wildcardRule && (
            <div className="flex items-center justify-between gap-3 rounded-md border border-amber-500/40 bg-amber-500/[0.06] px-3 py-2">
              <div className="flex items-center gap-2 text-xs text-amber-400">
                <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
                <span>
                  <span className="font-semibold">Wildcard active</span> — every shell command
                  is auto-approved and audited (rule ID:{' '}
                  <span className="font-mono">{wildcardRule.id}</span>
                  {wildcardRule.allow_metachars
                    ? <>; <span className="font-mono">allow_metachars=on</span> so multi-step commands (<code className="font-mono">|</code>, <code className="font-mono">;</code>, <code className="font-mono">&amp;</code>) flow through</>
                    : <>; <span className="font-mono">allow_metachars=off</span> — pipes / semicolons / ampersands are still cheap-blocked. Re-install via the button below or PATCH the rule to turn this on</>
                  }). Audit
                  rows show <span className="font-mono">approver_session_id = rule:{wildcardRule.id}</span>.
                </span>
              </div>
              <Button
                variant="outline"
                size="sm"
                className="shrink-0 border-amber-500/40 text-amber-400 hover:bg-amber-500/10 hover:text-amber-300"
                onClick={() => handleDelete(wildcardRule.id)}
                data-testid="allow-all-remove"
              >
                Remove
              </Button>
            </div>
          )}

          {/* One-click "allow + audit everything" button — hidden when wildcard already exists */}
          {!wildcardRule && !loading && (
            <Button
              variant="outline"
              size="sm"
              disabled={allowAllBusy}
              onClick={handleInstallAllowAll}
              className="border-amber-500/40 bg-amber-500/[0.06] text-amber-400 hover:bg-amber-500/10 hover:text-amber-300"
              data-testid="allow-all-install"
            >
              {allowAllBusy
                ? <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
                : <AlertTriangle className="mr-1.5 h-3 w-3" />
              }
              Allow + audit everything (skip approval prompts)
            </Button>
          )}

          <form onSubmit={handleCreate} className="space-y-2">
            <div className="grid gap-2 md:grid-cols-[1fr_1fr_120px_80px_auto]">
              <Input
                placeholder="Pattern (shell:ls)"
                value={pattern}
                onChange={(e) => setPattern(e.target.value)}
                data-testid="rule-pattern"
              />
              <Input
                placeholder="Directory (optional)"
                value={directory}
                onChange={(e) => setDirectory(e.target.value)}
              />
              <select
                className="h-9 rounded-md border border-input bg-background px-2 text-sm"
                value={decision}
                onChange={(e) => setDecision(e.target.value as 'allow' | 'deny')}
                data-testid="rule-decision"
              >
                <option value="allow">allow</option>
                <option value="deny">deny</option>
              </select>
              <Input
                type="number"
                value={priority}
                onChange={(e) => setPriority(Number(e.target.value) || 100)}
                title="Priority — lower wins"
              />
              <Button type="submit" size="sm" disabled={submitting} data-testid="rule-add">
                {submitting && <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />}
                + Rule
              </Button>
            </div>
            {decision === 'allow' && (
              <label className="flex items-center gap-2 text-xs text-muted-foreground">
                <input
                  type="checkbox"
                  checked={allowMetachars}
                  onChange={(e) => setAllowMetachars(e.target.checked)}
                  className="h-3.5 w-3.5 rounded border-input"
                  data-testid="rule-allow-metachars"
                />
                <span>
                  Allow shell metacharacters (<code className="font-mono">;|&amp;</code> backtick, newlines) — needed for
                  multi-step commands like <code className="font-mono">ssh host 'a | b'</code> or
                  <code className="font-mono"> cmd 2&gt;&amp;1</code>. Off by default; opt in per rule.
                </span>
              </label>
            )}
          </form>

          {loading ? (
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <Loader2 className="h-3 w-3 animate-spin" /> Loading rules…
            </div>
          ) : rules.length === 0 ? (
            <div className="rounded-md border border-dashed border-border/60 p-4 text-center text-xs text-muted-foreground">
              No rules — every shell command will wait for human approval.
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Pattern</TableHead>
                  <TableHead>Directory</TableHead>
                  <TableHead>Decision</TableHead>
                  <TableHead className="text-right">Priority</TableHead>
                  <TableHead className="text-right">Hits</TableHead>
                  <TableHead className="text-right">—</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rules.map((r) => (
                  <TableRow key={r.id} data-testid={`rule-row-${r.id}`}>
                    <TableCell className="font-mono text-xs">{r.pattern || '*'}</TableCell>
                    <TableCell className="font-mono text-xs">
                      {r.directory || <span className="text-muted-foreground">any</span>}
                    </TableCell>
                    <TableCell>
                      <div className="flex items-center gap-1">
                        <Badge
                          className={
                            r.decision === 'allow'
                              ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30 text-[10px]'
                              : r.decision === 'deny'
                                ? 'bg-rose-500/10 text-rose-400 border-rose-500/30 text-[10px]'
                                : 'text-[10px]'
                          }
                        >
                          {r.decision}
                        </Badge>
                        {r.allow_metachars && (
                          <Badge
                            title="This rule bypasses the metachar cheap-block: `;|&` backtick newlines flow through to approval"
                            className="bg-amber-500/10 text-amber-400 border-amber-500/30 text-[10px] font-mono"
                          >
                            metachars
                          </Badge>
                        )}
                      </div>
                    </TableCell>
                    <TableCell className="text-right text-xs">{r.priority}</TableCell>
                    <TableCell className="text-right text-xs text-muted-foreground">
                      {r.hit_count}
                    </TableCell>
                    <TableCell className="text-right">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => handleDelete(r.id)}
                        data-testid={`rule-delete-${r.id}`}
                      >
                        <Trash2 className="h-3 w-3" />
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </section>
  )
}

interface ClientRowProps {
  client: MCPClient
  busy: boolean
  onInstall: () => void
  onUninstall: () => void
}

function ClientRow({ client, busy, onInstall, onUninstall }: ClientRowProps) {
  const supported = SUPPORTED_HOOK_CLIENTS.has(client.id)
  // Drift is only meaningful when hooks_installed=true. The DB stores
  // the flag but the UI defends against stale rows by gating on the
  // current installed state too.
  const drifted = Boolean(client.hooks_installed && client.hooks_drifted)
  return (
    <TableRow data-testid={`shell-client-${client.id}`}>
      <TableCell>
        <div className="font-medium">{client.name}</div>
        <div className="font-mono text-[10px] text-muted-foreground/70">{client.id}</div>
      </TableCell>
      <TableCell>
        {client.detected ? (
          <Badge variant="secondary" className="text-[10px]">detected</Badge>
        ) : (
          <Badge variant="outline" className="text-[10px] text-muted-foreground">not found</Badge>
        )}
      </TableCell>
      <TableCell>
        {drifted ? (
          <div className="flex flex-col gap-0.5">
            <Badge
              className="bg-rose-500/10 text-rose-400 border-rose-500/30 text-[10px] w-fit"
              data-testid={`shell-drift-${client.id}`}
              title={
                client.hooks_drift_error
                  ? `Drift detected: ${client.hooks_drift_error}`
                  : 'Drift detected — settings.json no longer references mcplexer'
              }
            >
              <AlertTriangle className="mr-1 h-3 w-3" />
              drifted
            </Badge>
            <span className="text-[10px] text-rose-400/80">
              {client.hooks_drift_error
                ? `parse error: ${client.hooks_drift_error}`
                : 're-install to repair'}
            </span>
          </div>
        ) : client.hooks_installed ? (
          <Badge className="bg-emerald-500/10 text-emerald-400 border-emerald-500/30 text-[10px]">
            installed
          </Badge>
        ) : (
          <Badge variant="outline" className="text-[10px] text-muted-foreground">pending</Badge>
        )}
      </TableCell>
      <TableCell className="text-right">
        {supported ? (
          drifted ? (
            // Drift repair = re-run the install endpoint. Idempotent on
            // the backend, so a happy-path file gets a no-op write and
            // the row's hooks_drifted flag is cleared on success.
            <Button
              size="sm"
              variant="outline"
              className="border-rose-500/40 text-rose-400 hover:bg-rose-500/10 hover:text-rose-300"
              disabled={busy}
              onClick={onInstall}
              data-testid={`shell-repair-${client.id}`}
            >
              {busy && <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />}
              Re-install
            </Button>
          ) : client.hooks_installed ? (
            <Button
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={onUninstall}
              data-testid={`shell-uninstall-${client.id}`}
            >
              {busy && <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />}
              Uninstall
            </Button>
          ) : (
            <Button
              size="sm"
              disabled={busy || !client.detected}
              onClick={onInstall}
              data-testid={`shell-install-${client.id}`}
            >
              {busy && <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />}
              Install
            </Button>
          )
        ) : (
          <span className="text-xs text-muted-foreground">soon</span>
        )}
      </TableCell>
    </TableRow>
  )
}
