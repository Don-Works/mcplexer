import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { ApiClientError } from '@/api/client'
import {
  saveBrainTask,
  saveBrainMemory,
  getTaskStatusVocab,
  fetchMemoryCandidates,
  fetchGuidance,
  suppressBrainCandidate,
  type BrainTaskRecord,
  type BrainMemoryRecord,
  type BrainRecordKind,
  type BrainConflictDetail,
  type AssistMemoryCandidate,
  type AssistGuidanceNudge,
} from '@/api/brainBrowser'
import { AlertTriangle, Loader2, Save } from 'lucide-react'
import { toast } from 'sonner'
import { ConflictReconciler } from './components/ConflictReconciler'
import {
  TaskFrontmatterForm,
  MemoryFrontmatterForm,
  type FieldError,
} from './components/FrontmatterForm'
import { ValidationBanner } from './components/ValidationBanner'
import { FileTruthDisclosure } from './components/FileTruthDisclosure'
import { ModelPresenceLabel } from './components/ModelPresenceLabel'
import { MemoryCandidateRail } from './components/MemoryCandidateRail'
import { GuidanceNudge } from './components/GuidanceNudge'
import { arbitratePulse } from './components/pulseArbiter'
import type { GhostState } from './components/useGhostText'

type EditorRecord =
  | { kind: 'task'; rec: BrainTaskRecord }
  | { kind: 'memory'; rec: BrainMemoryRecord }

interface Props {
  kind: BrainRecordKind
  initial: BrainTaskRecord | BrainMemoryRecord
  onSaved: (saved: BrainTaskRecord | BrainMemoryRecord) => void
  onCancel: () => void
}

// ParsedError holds the structured 422 (field + allowed vocab) / 409 (conflict
// detail) the Go handler returns, pulled out of the ApiClientError body.
interface ParsedError {
  message: string
  field?: string
  allowed?: string[]
  conflict?: boolean
  detail?: BrainConflictDetail
}

function parseError(err: unknown): ParsedError {
  if (err instanceof ApiClientError) {
    try {
      const b = JSON.parse(err.body) as {
        error?: string
        field?: string
        allowed?: string[]
        conflict?: boolean
        detail?: BrainConflictDetail
      }
      return {
        message: b.error ?? err.message,
        field: b.field,
        allowed: b.allowed,
        conflict: b.conflict,
        detail: b.detail,
      }
    } catch {
      return { message: err.body || err.message }
    }
  }
  return { message: err instanceof Error ? err.message : 'Save failed' }
}

export function BrainRecordEditor({ kind, initial, onSaved, onCancel }: Props) {
  const [draft, setDraft] = useState<EditorRecord>(seed(kind, initial))
  const [saving, setSaving] = useState(false)
  // err holds the last 422/other error; conflict holds the 409 reconciler.
  const [err, setErr] = useState<ParsedError | null>(null)
  const [conflict, setConflict] = useState<BrainConflictDetail | null>(null)
  const [vocab, setVocab] = useState<string[]>([])
  // Ghost-text presence: drives the single shared ModelPresenceLabel.
  const [ghost, setGhost] = useState<GhostState>({
    ghost: '',
    inFlight: false,
    profile: null,
    degraded: false,
  })
  // Proactive memory candidates (DESIGN §3.5) surfaced in the right rail.
  const [candidates, setCandidates] = useState<AssistMemoryCandidate[]>([])
  const candAbort = useRef<AbortController | null>(null)
  // Inline guidance nudges (DESIGN §4.4) surfaced in-field under the body.
  const [nudges, setNudges] = useState<AssistGuidanceNudge[]>([])
  // dismissedNudges holds nudge kinds the user dismissed this session so a
  // re-fetch doesn't re-raise the same nudge (session-local, not sticky).
  const [dismissedNudges, setDismissedNudges] = useState<Set<string>>(new Set())
  const guidanceAbort = useRef<AbortController | null>(null)

  // Re-seed when the parent swaps the selected record.
  useEffect(() => {
    setErr(null)
    setConflict(null)
    setNudges([])
    setDismissedNudges(new Set())
    setDraft(seed(kind, initial))
  }, [kind, initial])

  // Load the workspace status vocab so the Status control is vocab-bound.
  const ws = draft.kind === 'task' ? draft.rec.workspace : (draft.rec.workspace ?? '')
  useEffect(() => {
    if (draft.kind !== 'task' || !ws) {
      setVocab([])
      return
    }
    let cancelled = false
    getTaskStatusVocab(ws)
      .then((rows) => {
        if (!cancelled) setVocab(rows.map((r) => r.status_text))
      })
      .catch(() => {
        if (!cancelled) setVocab([])
      })
    return () => {
      cancelled = true
    }
  }, [draft.kind, ws])

  // The prose body + title the proactive-memory pass reads.
  const body = draft.kind === 'task' ? draft.rec.description : draft.rec.content
  const title = draft.kind === 'task' ? draft.rec.title : draft.rec.name

  // Proactive memory: fetch candidates at a natural idle boundary (a ~1.2s
  // typing pause), never per-keystroke (DESIGN §4.3 interruption budget). A
  // 204 (no model) leaves the rail empty silently. Cancels a stale request
  // when the body changes again.
  useEffect(() => {
    if (!body || body.trim().length < 40) {
      setCandidates([])
      return
    }
    const t = setTimeout(() => {
      candAbort.current?.abort()
      const ac = new AbortController()
      candAbort.current = ac
      fetchMemoryCandidates(
        { record_id: draft.rec.id, title, body, workspace: ws || undefined },
        ac.signal,
      )
        .then(({ candidates }) => setCandidates(candidates))
        .catch((e) => {
          if ((e as Error)?.name !== 'AbortError') setCandidates([])
        })
    }, 1200)
    return () => clearTimeout(t)
  }, [body, title, ws, draft.rec.id])

  // Status + tags feed the deterministic guidance rules (missing-criteria /
  // auto-tag). Tasks carry both; memories only have tags.
  const status = draft.kind === 'task' ? draft.rec.status : ''
  const tags = draft.rec.tags ?? []
  const tagsKey = tags.join(',')

  // Inline guidance: fetch at the same ~1.2s idle boundary as candidates
  // (DESIGN §4.4). The deterministic nudges work with no model (never 204);
  // dismissed-this-session kinds are filtered out so a re-fetch is quiet.
  useEffect(() => {
    if (!body || body.trim().length < 12) {
      setNudges([])
      return
    }
    const t = setTimeout(() => {
      guidanceAbort.current?.abort()
      const ac = new AbortController()
      guidanceAbort.current = ac
      fetchGuidance(
        { record_id: draft.rec.id, title, body, status, tags, workspace: ws || undefined },
        ac.signal,
      )
        .then(({ nudges }) =>
          setNudges(nudges.filter((n) => !dismissedNudges.has(n.kind))),
        )
        .catch((e) => {
          if ((e as Error)?.name !== 'AbortError') setNudges([])
        })
    }, 1200)
    return () => clearTimeout(t)
    // tagsKey stands in for the tags array identity; status/body/title drive it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [body, title, status, tagsKey, ws, draft.rec.id, dismissedNudges])

  // applyNudge applies a one-click guidance fix to the draft (DESIGN §4.4):
  // add a tag, insert a [[ref]] into composes/entities, or append a checklist
  // to the body. Then it drops the applied nudge from the rail.
  function applyNudge(n: AssistGuidanceNudge) {
    setDraft((d) => {
      if (d.kind === 'task') {
        const rec = { ...d.rec }
        if (n.apply.add_tag && !rec.tags.includes(n.apply.add_tag)) {
          rec.tags = [...rec.tags, n.apply.add_tag]
        }
        if (n.apply.insert_ref) {
          const composes = rec.composes ?? []
          if (!composes.includes(n.apply.insert_ref)) rec.composes = [...composes, n.apply.insert_ref]
        }
        if (n.apply.append_body) rec.description = (rec.description ?? '') + n.apply.append_body
        return { kind: 'task', rec }
      }
      const rec = { ...d.rec }
      if (n.apply.add_tag && !rec.tags.includes(n.apply.add_tag)) {
        rec.tags = [...rec.tags, n.apply.add_tag]
      }
      if (n.apply.insert_ref) {
        const entities = rec.entities ?? []
        if (!entities.some((e) => e.id === n.apply.insert_ref)) {
          rec.entities = [...entities, { kind: 'memory', id: n.apply.insert_ref }]
        }
      }
      if (n.apply.append_body) rec.content = (rec.content ?? '') + n.apply.append_body
      return { kind: 'memory', rec }
    })
    setNudges((ns) => ns.filter((x) => x.kind !== n.kind))
    setDismissedNudges((s) => new Set(s).add(n.kind))
  }

  function dismissNudge(n: AssistGuidanceNudge) {
    setNudges((ns) => ns.filter((x) => x.kind !== n.kind))
    setDismissedNudges((s) => new Set(s).add(n.kind))
  }

  // saveCandidate writes the suggested memory through the same serializer path
  // as any other memory (DESIGN §3.5 accept), then drops it from the rail.
  async function saveCandidate(c: AssistMemoryCandidate) {
    try {
      await saveBrainMemory({
        id: '',
        kind: c.kind === 'fact' ? 'fact' : 'note',
        name: deriveMemoryName(c.text),
        workspace: ws || undefined,
        tags: c.tags ?? [],
        pinned: false,
        content: c.text,
        entities: (c.refs ?? []).map((id) => ({ kind: 'task', id })),
      })
      toast.success('Saved to your facts.')
      setCandidates((cs) => cs.filter((x) => x.content_hash !== c.content_hash))
    } catch (e) {
      toast.error(parseError(e).message)
    }
  }

  function dismissCandidate(c: AssistMemoryCandidate) {
    setCandidates((cs) => cs.filter((x) => x.content_hash !== c.content_hash))
  }

  // neverCandidate records the sticky per-record suppression (the content-hash)
  // so this exact candidate never re-fires for the record (DESIGN §3.5).
  async function neverCandidate(c: AssistMemoryCandidate) {
    setCandidates((cs) => cs.filter((x) => x.content_hash !== c.content_hash))
    if (draft.rec.id) {
      try {
        await suppressBrainCandidate(draft.rec.id, c.content_hash)
      } catch {
        // A failed suppression is non-fatal; the candidate is already hidden
        // for this session.
      }
    }
  }

  async function handleSave(rec?: BrainTaskRecord | BrainMemoryRecord, ifHash?: string) {
    setSaving(true)
    setErr(null)
    setConflict(null)
    try {
      if (draft.kind === 'task') {
        const payload = { ...((rec as BrainTaskRecord | undefined) ?? draft.rec) }
        // Submit the loaded on-disk hash as the CAS token (if_hash) so a
        // concurrent disk change is caught with the field-level reconciler.
        payload.if_hash = ifHash ?? payload.on_disk_hash
        const saved = await saveBrainTask(payload)
        toast.success('Task saved.')
        onSaved(saved)
      } else {
        const payload = { ...((rec as BrainMemoryRecord | undefined) ?? draft.rec) }
        payload.if_hash = ifHash ?? payload.on_disk_hash
        const saved = await saveBrainMemory(payload)
        toast.success(`${draft.rec.kind === 'fact' ? 'Fact' : 'Note'} saved.`)
        onSaved(saved)
      }
    } catch (e) {
      const parsed = parseError(e)
      if (parsed.conflict && parsed.detail) {
        setConflict(parsed.detail)
      } else if (parsed.conflict) {
        setErr(parsed)
        toast.error('Edit conflicted. Saved to a .conflict sidecar.')
      } else {
        setErr(parsed)
        toast.error(parsed.field ? `${parsed.field}: ${parsed.message}` : parsed.message)
      }
    } finally {
      setSaving(false)
    }
  }

  // applyFix snaps an offending vocab field on the draft to a valid value
  // (DESIGN §3.7 one-click fix), then clears the matching error.
  function applyFix(field: string, value: string) {
    setDraft((d) => {
      if (d.kind === 'task') return { kind: 'task', rec: { ...d.rec, [field]: value } as BrainTaskRecord }
      return { kind: 'memory', rec: { ...d.rec, [field]: value } as BrainMemoryRecord }
    })
    setErr((e) => (e && e.field === field ? null : e))
  }

  const isNew = !draft.rec.id
  // Friendly noun for the editor heading: a memory is a note (free-form)
  // or a "fact" (auto-recalled), never the internal "memory" kind word.
  const recordNoun =
    draft.kind === 'task' ? 'task' : draft.kind === 'memory' && draft.rec.kind === 'fact' ? 'fact' : 'note'
  // The standing index-validation banner (DESIGN §3.7) is distinct from a
  // save-time 422. It uses validation_field so the inline fix targets the
  // right control even before the first save.
  const standingError: FieldError | null = draft.rec.validation_error
    ? {
        message: draft.rec.validation_error,
        field: draft.rec.validation_field,
        allowed: vocab.length > 0 ? vocab : undefined,
      }
    : null
  // The inline-at-control error is the 422 field error or the standing one.
  const fieldError: FieldError | null = err?.field
    ? { field: err.field, message: err.message, allowed: err.allowed }
    : standingError

  // One-pulse-per-record arbiter (DESIGN §3.5/§4.4): decide which single
  // surface owns the record's pulse this render. The losing surface still
  // renders (guidance nudge still actionable, candidates still listed) but
  // without its pulse marker, so the pulse vocabulary stays meaningful.
  const pulse = arbitratePulse(candidates, nudges)
  const topNudge = pulse.nudge

  return (
    <div className="flex gap-4">
      <div className="min-w-0 flex-1 space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">
          {isNew ? 'New' : 'Edit'} {recordNoun}
        </h2>
        <div className="flex items-center gap-3">
          {/* The single standing AI chrome: mono model · <profile>, shimmers
              only while a ghost completion is in flight (DESIGN §4.2). Absent
              when no profile is configured (silent degrade). */}
          <ModelPresenceLabel profile={ghost.profile} inFlight={ghost.inFlight} />
          <Badge tone="mono" className="text-xs">
            {draft.rec.id || 'unsaved'}
          </Badge>
        </div>
      </div>

      {/* Standing index-validation banner with one-click vocab fix. */}
      {standingError && (
        <ValidationBanner
          message={standingError.message}
          field={standingError.field}
          allowed={standingError.allowed}
          onFix={applyFix}
        />
      )}

      {/* Save-time 422 with a vocab fix (only when the banner above is absent
          to avoid stacking two of the same affordance). */}
      {!standingError && err?.field && err.allowed && err.allowed.length > 0 && (
        <ValidationBanner
          message={err.message}
          field={err.field}
          allowed={err.allowed}
          onFix={applyFix}
        />
      )}

      {/* Non-field errors (conflict sidecar fallback, server errors). */}
      {err && !err.field && (
        <div className="flex items-start gap-2 border border-amber-500/40 bg-amber-500/10 p-2 text-sm text-amber-300">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />
          <span>{err.message}</span>
        </div>
      )}

      {draft.kind === 'task' ? (
        <TaskFrontmatterForm
          rec={draft.rec}
          vocab={vocab}
          err={fieldError}
          onChange={(rec) => setDraft({ kind: 'task', rec })}
          onGhostState={setGhost}
        />
      ) : (
        <MemoryFrontmatterForm
          rec={draft.rec}
          err={fieldError}
          onChange={(rec) => setDraft({ kind: 'memory', rec })}
          onGhostState={setGhost}
        />
      )}

      {/* Inline guidance nudge (DESIGN §4.4): a single calm in-field line.
          At most one is shown (the highest-signal kind); it carries the pulse
          only when the arbiter says guidance owns the record's pulse. */}
      {topNudge && (
        <GuidanceNudge nudge={topNudge} onApply={applyNudge} onDismiss={dismissNudge} />
      )}

      <div className="flex items-center gap-2 pt-2">
        <Button className="rounded-none" onClick={() => handleSave()} disabled={saving}>
          {saving ? (
            <Loader2 className="mr-1.5 h-4 w-4 animate-spin" />
          ) : (
            <Save className="mr-1.5 h-4 w-4" />
          )}
          Save
        </Button>
        <Button variant="ghost" className="rounded-none" onClick={onCancel} disabled={saving}>
          Cancel
        </Button>
      </div>

      {/* FileTruthDisclosure — the verbatim .md the agent reads. */}
      <FileTruthDisclosure
        path={draft.rec.path}
        raw={draft.rec.raw}
        savedHint={draft.rec.id ? 'saved · indexed' : undefined}
      />

      {conflict && (
        <ConflictReconciler
          detail={conflict}
          kind={draft.kind}
          mine={draft.rec}
          open={Boolean(conflict)}
          onResolve={(merged, ifHash) => handleSave(merged, ifHash)}
          onReload={() => {
            // "take all theirs & re-edit": adopt the on-disk record + fresh hash.
            const theirs = draft.kind === 'task' ? conflict.on_disk_task : conflict.on_disk_memory
            if (theirs) {
              const next = { ...theirs, on_disk_hash: conflict.on_disk_hash }
              setDraft(
                draft.kind === 'task'
                  ? { kind: 'task', rec: next as BrainTaskRecord }
                  : { kind: 'memory', rec: next as BrainMemoryRecord },
              )
            }
            setConflict(null)
          }}
          onCancel={() => setConflict(null)}
        />
      )}
      </div>

      {/* Ambient memory-candidate rail (DESIGN §3.5): a quiet right column,
          never a popup. Renders nothing when there are no candidates. */}
      {candidates.length > 0 && (
        <div className="w-72 shrink-0">
          <MemoryCandidateRail
            candidates={candidates}
            pulse={pulse.owner === 'memory'}
            onSave={saveCandidate}
            onDismiss={dismissCandidate}
            onNever={neverCandidate}
          />
        </div>
      )}
    </div>
  )
}

// deriveMemoryName turns a candidate's prose into a slug suitable for the
// memory's unique-key name (mono key). Falls back to a timestamped slug when
// the text has no usable words.
function deriveMemoryName(text: string): string {
  const slug = text
    .toLowerCase()
    .replace(/[^a-z0-9\s-]/g, '')
    .trim()
    .split(/\s+/)
    .slice(0, 6)
    .join('-')
  return slug || `memory-${Date.now()}`
}

function seed(kind: BrainRecordKind, initial: BrainTaskRecord | BrainMemoryRecord): EditorRecord {
  return kind === 'task'
    ? { kind: 'task', rec: { ...(initial as BrainTaskRecord) } }
    : { kind: 'memory', rec: { ...(initial as BrainMemoryRecord) } }
}
