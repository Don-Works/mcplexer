import { useMemo } from 'react'
import { Badge } from '@/components/ui/badge'
import { CopyButton } from '@/components/ui/copy-button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { AuditFilter, AuditRecord } from '@/api/types'
import { useNavigate } from 'react-router-dom'
import {
  Bot,
  Cpu,
  Eye,
  Filter,
  Gauge,
  HardDrive,
  KeyRound,
  Layers,
  Link2,
  Monitor,
  Route,
  ScanSearch,
  User,
  Zap,
} from 'lucide-react'
import { LinkifiedText } from '@/pages/tasks/TaskRef'
import { cn } from '@/lib/utils'
import {
  SECRET_TONE,
  classifySecretEvent,
  getErrorReason,
  isSecretsActor,
  normalizeStatus,
} from '@/lib/audit-semantics'

/**
 * SecretEventBadge — the calm-vs-attention chip that distinguishes
 * enumeration (secret.list, blue) from decryption (secret.read, amber) at a
 * glance. Renders nothing for non-secret rows, so callers can drop it in
 * unconditionally. The full explanation lives in the tooltip + the inspector.
 */
export function SecretEventBadge({
  toolName,
  className,
}: {
  toolName: string
  className?: string
}) {
  const sem = classifySecretEvent(toolName)
  if (!sem) return null
  const Icon = sem.op === 'enumerate' ? ScanSearch : sem.op === 'decrypt' ? Eye : KeyRound
  return (
    <span
      title={sem.blurb}
      className={cn(
        'inline-flex items-center gap-1 rounded-sm border px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wider align-middle',
        SECRET_TONE[sem.tone],
        className,
      )}
    >
      <Icon className="h-2.5 w-2.5" />
      {sem.label}
    </span>
  )
}

// First non-empty line, capped — keeps the row scannable even when the
// underlying error_message is a multi-KB stack trace. Full text lives in the
// tooltip + the inspector.
function shortReason(raw: string): string {
  const firstLine = raw.split('\n').find((l) => l.trim().length > 0) ?? raw
  return firstLine.length > 80 ? firstLine.slice(0, 77) + '…' : firstLine
}

export function ReasonBadge({ record }: { record: AuditRecord }) {
  const reason = getErrorReason(record)
  if (!reason) return null
  const blocked = reason === 'blocked'
  const display = blocked || reason === 'no route' ? reason : shortReason(reason)
  // Use a span, not the shadcn Badge primitive: Badge has `justify-center`
  // which clips a too-long truncate-target from BOTH sides, so the visible
  // text is the middle of the error rather than the start.
  return (
    <span
      className={cn(
        'inline-block max-w-full truncate rounded-sm border px-2 py-0.5 text-xs font-medium align-middle',
        blocked
          ? 'border-amber-500/40 text-amber-500'
          : 'border-destructive/40 text-destructive',
      )}
      title={reason}
    >
      {display}
    </span>
  )
}

/**
 * AuditInspector — the full detail body for a single audit record. Extracted
 * from AuditDetailDialog so it can render BOTH inline (in a Mission Control
 * pane) and inside the existing Sheet. It owns no chrome (no header/footer/
 * keyboard nav) — just the scrollable sections.
 *
 * Session/Execution "Filter" actions: when `onFilter` is supplied (page
 * context) it's called with the patch and `onClose` is invoked; otherwise the
 * component navigates to /audit?<facet> as a standalone fallback.
 */
export function AuditInspector({
  record,
  wsName,
  asName,
  onFilter,
  onClose,
  className,
}: {
  record: AuditRecord
  wsName: (id: string) => string
  asName: (id: string) => string
  onFilter?: (patch: Partial<AuditFilter>) => void
  onClose?: () => void
  className?: string
}) {
  const navigate = useNavigate()

  const status = record.status
  const isError = normalizeStatus(status) !== 'success'
  const paramKeys = Object.keys(record.params_redacted ?? {})
  const secret = classifySecretEvent(record.tool_name)
  const fromSecretsResolver = isSecretsActor(record)
  const scopeLabel = record.auth_scope_id ? asName(record.auth_scope_id) : ''

  const applyFilter = (patch: Partial<AuditFilter>, fallbackQuery: string) => {
    if (onFilter) {
      onFilter(patch)
      onClose?.()
    } else {
      onClose?.()
      navigate(`/audit?${fallbackQuery}`)
    }
  }

  return (
    <div className={cn('flex flex-col', className)}>
      {secret && (
        <div
          className={cn(
            'mb-1 flex gap-3 rounded-none border p-3',
            SECRET_TONE[secret.tone],
          )}
        >
          <div className="pt-0.5">
            {secret.op === 'enumerate' ? (
              <ScanSearch className="h-4 w-4" />
            ) : secret.op === 'decrypt' ? (
              <Eye className="h-4 w-4" />
            ) : (
              <KeyRound className="h-4 w-4" />
            )}
          </div>
          <div className="min-w-0 space-y-1">
            <p className="text-sm font-semibold">
              {secret.label}
              {secret.op === 'enumerate' && (
                <span className="ml-1.5 font-normal opacity-80">
                  (key names only, no value read)
                </span>
              )}
            </p>
            <p className="text-xs leading-relaxed opacity-90">{secret.blurb}</p>
            {scopeLabel && (
              <p className="text-xs opacity-90">
                Scope:{' '}
                <span className="font-mono font-medium">{scopeLabel}</span>
              </p>
            )}
          </div>
        </div>
      )}

      {isError && (
        <Section
          label="Outcome"
          accent={status === 'blocked' ? 'amber' : 'destructive'}
        >
          {record.error_code && (
            <KV label="Code">
              <code className="font-mono text-xs text-accent-foreground break-all">
                {record.error_code}
              </code>
            </KV>
          )}
          {record.error_message && (
            <div className="space-y-1.5">
              <div className="flex items-center justify-between">
                <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                  Message
                </span>
                <CopyButton value={record.error_message} />
              </div>
              <ErrorBlock message={record.error_message} />
            </div>
          )}
        </Section>
      )}

      {(record.actor_kind || record.actor_id || record.correlation_id || fromSecretsResolver) && (
        <Section label="Who / Actor">
          {record.actor_kind && (
            <KV label="Actor" icon={<User className="h-3 w-3" />}>
              <span className="font-mono text-xs text-foreground">
                {record.actor_kind}
              </span>
              {record.actor_id && record.actor_id !== record.auth_scope_id && (
                <span className="font-mono text-xs text-muted-foreground break-all">
                  {record.actor_id}
                </span>
              )}
            </KV>
          )}
          {record.correlation_id ? (
            <KV label="Correlation" icon={<Link2 className="h-3 w-3" />}>
              <span className="font-mono text-xs text-foreground break-all">
                {record.correlation_id}
              </span>
              <CopyButton value={record.correlation_id} />
            </KV>
          ) : (
            fromSecretsResolver && (
              <KV label="Correlation" icon={<Link2 className="h-3 w-3" />}>
                <span className="text-xs text-muted-foreground">
                  none recorded for this row
                </span>
              </KV>
            )
          )}
          {fromSecretsResolver && (
            <p className="rounded-none border border-border/50 bg-muted/30 p-2.5 text-xs leading-relaxed text-muted-foreground">
              Emitted by the gateway&apos;s secret resolver, attributed to
              the auth&nbsp;scope it touched
              {scopeLabel && (
                <>
                  {' '}(<span className="font-mono text-foreground">{scopeLabel}</span>)
                </>
              )}
              , not to the agent that triggered it.{' '}
              {record.correlation_id
                ? 'The triggering agent or worker shares the correlation id above.'
                : 'No correlation id was recorded here: find the trigger by matching the timestamp and scope against the session / worker activity around this time.'}
            </p>
          )}
        </Section>
      )}

      <Section label="Call">
        <KV label="Subpath">
          <code className="font-mono text-xs text-accent-foreground break-all">
            {record.subpath || '-'}
          </code>
        </KV>
        <KV label="When">
          <div className="flex flex-col gap-0.5">
            <span className="text-foreground">
              {new Date(record.timestamp).toLocaleString()}
            </span>
            <span className="font-mono text-xs text-muted-foreground">
              {record.timestamp}
            </span>
          </div>
          <CopyButton value={record.timestamp} className="ml-2" />
        </KV>
      </Section>

      <Section label="Context">
        {record.session_id && (
          <KV label="Session">
            <UuidValue value={record.session_id} accent="cyan" icon={<Monitor className="h-3 w-3" />} />
            <FilterButton
              label="Filter"
              onClick={() =>
                applyFilter(
                  { session_id: record.session_id },
                  `session_id=${record.session_id}`,
                )
              }
            />
          </KV>
        )}
        {record.execution_id && (
          <KV label="Execution">
            <UuidValue value={record.execution_id} accent="violet" icon={<Layers className="h-3 w-3" />} />
            <FilterButton
              label="Filter"
              onClick={() =>
                applyFilter(
                  { execution_id: record.execution_id },
                  `execution_id=${record.execution_id}`,
                )
              }
            />
          </KV>
        )}
        {record.client_type && (
          <KV label="Harness" icon={<Bot className="h-3 w-3" />}>
            <span className="font-mono text-xs text-foreground break-all">{record.client_type}</span>
          </KV>
        )}
        {record.model && record.model !== record.client_type && (
          <KV label="Model" icon={<Cpu className="h-3 w-3" />}>
            <span className="font-mono text-xs text-foreground break-all">{record.model}</span>
          </KV>
        )}
      </Section>

      <Section label="Routing">
        <KV label="Workspace">
          <span className="break-words text-sm text-foreground">
            {record.workspace_name || (record.workspace_id ? wsName(record.workspace_id) : '-')}
          </span>
        </KV>
        <KV label="Rule" icon={<Route className="h-3 w-3" />}>
          <div className="min-w-0 flex-1">
            <div className="break-words text-sm text-foreground">
              {record.route_rule_summary ?? record.route_rule_id ?? '-'}
            </div>
            {record.route_rule_summary && record.route_rule_id && (
              <div className="mt-0.5 font-mono text-[10px] text-muted-foreground break-all">
                {record.route_rule_id}
              </div>
            )}
          </div>
          {record.route_rule_id && <CopyButton value={record.route_rule_id} />}
        </KV>
        <KV label="Downstream">
          <div className="min-w-0 flex-1">
            <div className="break-words text-sm text-foreground">
              {record.downstream_server_name ?? record.downstream_server_id ?? '-'}
            </div>
            {record.downstream_server_name && record.downstream_server_id && (
              <div className="mt-0.5 font-mono text-[10px] text-muted-foreground break-all">
                {record.downstream_server_id}
              </div>
            )}
          </div>
          {record.downstream_server_id && <CopyButton value={record.downstream_server_id} />}
        </KV>
        <KV label="Auth scope">
          <span className="break-words text-sm text-foreground">
            {record.auth_scope_id ? asName(record.auth_scope_id) : '-'}
          </span>
        </KV>
      </Section>

      <Section label="Performance">
        <div className="grid grid-cols-3 gap-3">
          <Stat
            icon={<Zap className="h-3 w-3" />}
            label="Latency"
            value={`${record.latency_ms} ms`}
            tone={latencyTone(record.latency_ms)}
          />
          <Stat
            icon={<HardDrive className="h-3 w-3" />}
            label="Response"
            value={formatBytes(record.response_size)}
            tone="neutral"
          />
          <Stat
            icon={<Gauge className="h-3 w-3" />}
            label="Cache"
            value={record.cache_hit ? 'hit' : 'miss'}
            tone={record.cache_hit ? 'success' : 'neutral'}
          />
        </div>
      </Section>

      {paramKeys.length > 0 && (
        <Section
          label="Redacted params"
          action={
            <CopyButton value={JSON.stringify(record.params_redacted, null, 2)} />
          }
        >
          <pre className="max-h-[28rem] overflow-auto rounded-none border border-border bg-background/60 p-3 font-mono text-xs leading-relaxed text-accent-foreground">
            <LinkifiedText text={JSON.stringify(record.params_redacted, null, 2)} workspaceId={record.workspace_id} />
          </pre>
        </Section>
      )}

      <Section label="Identity" defaultMuted>
        <KV label="Record ID">
          <code className="font-mono text-xs text-muted-foreground break-all">
            {record.id}
          </code>
          <CopyButton value={record.id} />
        </KV>
      </Section>
    </div>
  )
}

// --- inspector vocabulary (shared sub-components) ------------------------

function Section({
  label,
  children,
  accent,
  defaultMuted,
  action,
}: {
  label: string
  children: React.ReactNode
  accent?: 'destructive' | 'amber'
  defaultMuted?: boolean
  action?: React.ReactNode
}) {
  const labelColor =
    accent === 'destructive'
      ? 'text-destructive'
      : accent === 'amber'
        ? 'text-amber-500'
        : defaultMuted
          ? 'text-muted-foreground/60'
          : 'text-muted-foreground'
  return (
    <section className="border-b border-border/30 py-4 first:pt-1 last:border-b-0">
      <div className="mb-2.5 flex items-center justify-between">
        <h3 className={cn('text-[10px] font-semibold uppercase tracking-[0.12em]', labelColor)}>
          {label}
        </h3>
        {action}
      </div>
      <div className="space-y-2.5">{children}</div>
    </section>
  )
}

function KV({
  label,
  children,
  icon,
}: {
  label: string
  children: React.ReactNode
  icon?: React.ReactNode
}) {
  return (
    <div className="flex items-start gap-3">
      <div className="flex w-24 shrink-0 items-center gap-1 pt-0.5 text-xs text-muted-foreground">
        {icon}
        <span>{label}</span>
      </div>
      <div className="flex min-w-0 flex-1 items-start gap-2">{children}</div>
    </div>
  )
}

// StatusChip — the normalized status badge ("ok" → success styling) used in
// the inspector header. Exported so the Sheet header can render it.
export function StatusChip({ status }: { status: AuditRecord['status'] }) {
  const styles: Record<'success' | 'error' | 'blocked', string> = {
    success: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-400',
    error: 'border-destructive/40 bg-destructive/10 text-destructive',
    blocked: 'border-amber-500/40 bg-amber-500/10 text-amber-500',
  }
  // Style by the normalized tone ("ok" → success) but keep the raw label so
  // the underlying status string stays visible/searchable.
  return (
    <Badge
      variant="outline"
      className={cn('font-mono uppercase tracking-wider', styles[normalizeStatus(status)])}
    >
      {status}
    </Badge>
  )
}

function FilterButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="inline-flex shrink-0 items-center gap-1 rounded border border-dashed border-border px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-muted-foreground transition-colors hover:border-primary/40 hover:text-foreground"
    >
      <Filter className="h-2.5 w-2.5" />
      {label}
    </button>
  )
}

function UuidValue({
  value,
  accent,
  icon,
}: {
  value: string
  accent: 'cyan' | 'violet'
  icon?: React.ReactNode
}) {
  const accentClass =
    accent === 'cyan' ? 'text-cyan-400' : 'text-violet-400'
  return (
    <div className="flex min-w-0 flex-1 items-center gap-2">
      <span className={cn('flex items-center gap-1 font-mono text-xs break-all', accentClass)}>
        {icon}
        {value}
      </span>
      <CopyButton value={value} />
    </div>
  )
}

function Stat({
  icon,
  label,
  value,
  tone,
}: {
  icon: React.ReactNode
  label: string
  value: string
  tone: 'success' | 'warn' | 'error' | 'neutral'
}) {
  const toneClass =
    tone === 'success'
      ? 'text-emerald-400'
      : tone === 'warn'
        ? 'text-amber-500'
        : tone === 'error'
          ? 'text-destructive'
          : 'text-foreground'
  return (
    <div className="flex flex-col gap-1 rounded-none border border-border/40 bg-background/40 px-3 py-2">
      <div className="flex items-center gap-1 text-[10px] uppercase tracking-wider text-muted-foreground">
        {icon}
        {label}
      </div>
      <div className={cn('font-mono text-sm tabular-nums', toneClass)}>{value}</div>
    </div>
  )
}

function ErrorBlock({ message }: { message: string }) {
  const pretty = useMemo(() => tryPrettyJson(message), [message])
  const text = pretty ?? message
  return (
    <pre className="max-h-[20rem] overflow-auto whitespace-pre-wrap break-words rounded-none border border-destructive/30 bg-destructive/5 p-3 font-mono text-xs leading-relaxed text-foreground">
      <LinkifiedText text={text} />
    </pre>
  )
}

// Timestamp — relative time with an absolute tooltip. Used by the Sheet header.
export function Timestamp({ value }: { value: string }) {
  const rel = useMemo(() => relativeTime(value), [value])
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span>{rel}</span>
      </TooltipTrigger>
      <TooltipContent>{new Date(value).toLocaleString()}</TooltipContent>
    </Tooltip>
  )
}

// --- helpers -------------------------------------------------------------

function tryPrettyJson(s: string): string | null {
  const trimmed = s.trim()
  if (!trimmed || (trimmed[0] !== '{' && trimmed[0] !== '[')) return null
  try {
    return JSON.stringify(JSON.parse(trimmed), null, 2)
  } catch {
    return null
  }
}

function relativeTime(iso: string): string {
  const then = new Date(iso).getTime()
  if (!Number.isFinite(then)) return iso
  const diff = Date.now() - then
  const abs = Math.abs(diff)
  const sign = diff < 0 ? 'in ' : ''
  const suffix = diff < 0 ? '' : ' ago'
  const sec = Math.round(abs / 1000)
  if (sec < 5) return 'just now'
  if (sec < 60) return `${sign}${sec}s${suffix}`
  const min = Math.round(sec / 60)
  if (min < 60) return `${sign}${min}m${suffix}`
  const hr = Math.round(min / 60)
  if (hr < 24) return `${sign}${hr}h${suffix}`
  const day = Math.round(hr / 24)
  if (day < 30) return `${sign}${day}d${suffix}`
  const mo = Math.round(day / 30)
  if (mo < 12) return `${sign}${mo}mo${suffix}`
  return `${sign}${Math.round(mo / 12)}y${suffix}`
}

function latencyTone(ms: number): 'success' | 'warn' | 'error' | 'neutral' {
  if (ms < 100) return 'success'
  if (ms < 1000) return 'neutral'
  if (ms < 3000) return 'warn'
  return 'error'
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / (1024 * 1024)).toFixed(2)} MB`
}
