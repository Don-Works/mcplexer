/* eslint-disable react-refresh/only-export-components */
import {
  Brain,
  CalendarClock,
  Globe,
  KeyRound,
  Link as LinkIcon,
  ListChecks,
  MessagesSquare,
  Plug,
  ShieldAlert,
  Sparkles,
  Terminal,
  type LucideIcon,
} from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import type { ToolApproval } from '@/api/types'

// Surface = which Guard raised the approval. Empty string is the legacy
// "mcp" default (see internal/api/approval_handler + migration 047). Icon
// carries the surface; we keep the palette tinted-neutral, not loud.
const SURFACE_META: Record<string, { label: string; Icon: LucideIcon }> = {
  mcp: { label: 'mcp', Icon: Plug },
  shell: { label: 'shell', Icon: Terminal },
  schedule: { label: 'schedule', Icon: CalendarClock },
  network: { label: 'network', Icon: Globe },
  sanitizer: { label: 'sanitizer', Icon: ShieldAlert },
}

export function surfaceMeta(surface?: string): { label: string; Icon: LucideIcon } {
  return SURFACE_META[surface || 'mcp'] ?? SURFACE_META.mcp
}

// Kind classifies cross-boundary share approvals (migration 081). When
// present the row is a share envelope, not a raw tool call, and we lead
// with the kind + summary instead of a tool name.
const KIND_META: Record<string, { label: string; Icon: LucideIcon }> = {
  skill_share: { label: 'skill share', Icon: Sparkles },
  memory_share: { label: 'memory share', Icon: Brain },
  task_offer: { label: 'task offer', Icon: ListChecks },
  mesh_direct: { label: 'mesh message', Icon: MessagesSquare },
  mesh_grant_consent: { label: 'scope grant', Icon: KeyRound },
}

export function kindMeta(kind?: string): { label: string; Icon: LucideIcon } | null {
  if (!kind) return null
  return KIND_META[kind] ?? { label: kind.replace(/_/g, ' '), Icon: KeyRound }
}

// primaryLabel is the one line that identifies an approval in a row or
// header: the share summary for envelopes, otherwise the tool name.
export function primaryLabel(a: ToolApproval): string {
  if (a.kind) return a.summary || kindMeta(a.kind)?.label || a.kind
  return a.tool_name || '(unnamed call)'
}

// statusTone maps a terminal status to the shared Badge tone vocabulary.
// timeout reads as warn ("you didn't get to it"); cancelled is muted
// (the request withdrew itself, no operator signal needed).
export function StatusBadge({ status }: { status: string }) {
  switch (status) {
    case 'approved':
      return <Badge tone="success">approved</Badge>
    case 'denied':
      return <Badge tone="critical">denied</Badge>
    case 'timeout':
      return <Badge tone="warn">timeout</Badge>
    case 'cancelled':
      return <Badge tone="muted">cancelled</Badge>
    case 'pending':
      return <Badge tone="warn">pending</Badge>
    default:
      return <Badge tone="muted">{status}</Badge>
  }
}

// ApproverChip renders who (or what) resolved the approval.
//   afk-policy + rule:<id> → a trusted-allowlist rule fired (linked chip)
//   dashboard              → a human clicked approve/deny
//   dangerous-mode         → the global bypass auto-approved it
//   system / timeout       → the gateway resolved it on a timer
export function ApproverChip({
  approverType,
  approverSessionID,
}: {
  approverType: string
  approverSessionID: string
}) {
  if (approverType === 'afk-policy' && approverSessionID.startsWith('rule:')) {
    const ruleID = approverSessionID.slice('rule:'.length)
    return (
      <Badge tone="info" className="font-mono lowercase tracking-normal" title={`Auto-approved by rule ${ruleID}`}>
        <LinkIcon className="h-2.5 w-2.5" />
        rule:{ruleID.slice(0, 8)}
      </Badge>
    )
  }
  if (approverType === 'afk-policy') return <Badge tone="info">auto-rule</Badge>
  if (approverType === 'dashboard') return <Badge tone="success">human</Badge>
  if (approverSessionID === 'dangerous-mode') return <Badge tone="critical">dangerous-mode</Badge>
  if (approverType) return <Badge tone="muted">{approverType}</Badge>
  return <span className="text-muted-foreground">—</span>
}

// formatWait turns the created→resolved gap into a terse duration. For a
// still-pending row it returns ''. Timeouts show the full window elapsed.
export function formatWait(a: ToolApproval): string {
  if (!a.resolved_at) return ''
  const ms = new Date(a.resolved_at).getTime() - new Date(a.created_at).getTime()
  if (!Number.isFinite(ms) || ms < 0) return ''
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const rem = s % 60
  return rem ? `${m}m ${rem}s` : `${m}m`
}

// formatTimeRemaining renders the live countdown until a pending
// approval times out. `now` is passed in from a 1s ticker so the label
// updates without re-fetching. Returns 'expired' once the window closes.
export function formatTimeRemaining(createdAt: string, timeoutSec: number, now: number): string {
  const elapsed = (now - new Date(createdAt).getTime()) / 1000
  const remaining = Math.max(0, timeoutSec - elapsed)
  if (remaining <= 0) return 'expired'
  const mins = Math.floor(remaining / 60)
  const secs = Math.floor(remaining % 60)
  return mins > 0 ? `${mins}m ${secs}s` : `${secs}s`
}

// prettyJSON best-effort pretty-prints a JSON argument blob, falling back
// to the raw string when it isn't valid JSON.
export function prettyJSON(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}
