// WorkContextCard — sidebar chip strip that shows where the work for
// this task actually lives (worktree / branch / PR / commits / peer /
// session / linear ticket / mesh thread). Each chip uses the right
// affordance for its type: PR opens GitHub, branch is mono with a
// copy-to-clipboard, peer / mesh_thread link into the dashboard's
// own views.
//
// Sourced from the typed `WorkContext` parsed off the task's `meta`
// column. The Edit button opens an inline dialog that writes back via
// `setWorkContext`, which merges the patch into meta server-side (the
// composes / composed_by / custom keys survive untouched).

import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  ExternalLink,
  GitBranch,
  GitCommit,
  Hash,
  Loader2,
  MessageSquare,
  Network,
  Pencil,
  Plus,
  Ticket,
  Workflow,
} from 'lucide-react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { CopyButton } from '@/components/ui/copy-button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  parseWorkContext,
  setWorkContext,
  type Task,
  type WorkContext,
} from '@/api/tasks'

interface Props {
  task: Task
  onUpdate: () => void
}

// shortPRLabel pulls the trailing path segment off a GitHub-style
// /pull/<n> URL so the chip stays narrow. Falls back to "PR" when the
// URL doesn't have a numeric tail (different forge style, custom path).
function shortPRLabel(prUrl: string): string {
  try {
    const u = new URL(prUrl)
    const tail = u.pathname.split('/').filter(Boolean).pop() ?? ''
    if (/^\d+$/.test(tail)) return `PR #${tail}`
    if (tail) return tail
  } catch {
    // Fall through.
  }
  return 'PR'
}

// short stops a long id from blowing out the sidebar — slicing the
// last 6 chars is enough to disambiguate at human-perception scale.
function shortPeer(peerId: string): string {
  if (peerId.length <= 6) return peerId
  return peerId.slice(-6)
}

function shortSession(s: string): string {
  if (s.length <= 8) return s
  return s.slice(0, 8)
}

function shortThread(s: string): string {
  if (s.length <= 8) return s
  return s.slice(0, 8)
}

export function WorkContextCard({ task, onUpdate }: Props) {
  const wc = parseWorkContext(task.meta)
  const hasAny =
    wc.worktree ||
    wc.branch ||
    wc.pr ||
    wc.commits ||
    wc.peer ||
    wc.session ||
    wc.linear ||
    wc.mesh_thread

  const [editOpen, setEditOpen] = useState(false)

  return (
    <section>
      <div className="mb-2 flex items-center justify-between">
        <h2 className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
          Work context
        </h2>
        <Button
          variant="ghost"
          size="sm"
          className="h-6 px-1.5 text-[10px]"
          onClick={() => setEditOpen(true)}
        >
          {hasAny ? (
            <>
              <Pencil className="h-3 w-3" />
              Edit
            </>
          ) : (
            <>
              <Plus className="h-3 w-3" />
              Add
            </>
          )}
        </Button>
      </div>
      {hasAny ? (
        <ul className="space-y-1.5 text-xs">
          {wc.pr ? (
            <Chip icon={<ExternalLink className="h-3 w-3" />}>
              <a
                href={wc.pr}
                target="_blank"
                rel="noopener noreferrer"
                className="font-mono text-primary hover:underline"
                title={wc.pr}
              >
                {shortPRLabel(wc.pr)}
              </a>
            </Chip>
          ) : null}
          {wc.branch ? (
            <Chip icon={<GitBranch className="h-3 w-3" />}>
              <span className="font-mono">{wc.branch}</span>
              <CopyButton value={`git checkout ${wc.branch}`} className="ml-1 h-5 w-5" />
            </Chip>
          ) : null}
          {wc.worktree ? (
            <Chip icon={<Workflow className="h-3 w-3" />}>
              <span className="truncate font-mono" title={wc.worktree}>
                {wc.worktree}
              </span>
              <CopyButton value={wc.worktree} className="ml-1 h-5 w-5" />
            </Chip>
          ) : null}
          {wc.commits ? (
            <Chip icon={<GitCommit className="h-3 w-3" />}>
              <span className="font-mono" title={wc.commits}>
                {wc.commits}
              </span>
            </Chip>
          ) : null}
          {wc.peer ? (
            <Chip icon={<Network className="h-3 w-3" />}>
              <Link
                to={`/peers/${encodeURIComponent(wc.peer)}`}
                className="font-mono text-primary hover:underline"
                title={wc.peer}
              >
                peer:{shortPeer(wc.peer)}
              </Link>
            </Chip>
          ) : null}
          {wc.session ? (
            <Chip icon={<Hash className="h-3 w-3" />}>
              <span className="font-mono" title={wc.session}>
                {shortSession(wc.session)}
              </span>
            </Chip>
          ) : null}
          {wc.linear ? (
            <Chip icon={<Ticket className="h-3 w-3" />}>
              <a
                href={`https://linear.app/ticket/${encodeURIComponent(wc.linear)}`}
                target="_blank"
                rel="noopener noreferrer"
                className="font-mono text-primary hover:underline"
              >
                {wc.linear}
              </a>
            </Chip>
          ) : null}
          {wc.mesh_thread ? (
            <Chip icon={<MessageSquare className="h-3 w-3" />}>
              <Link
                to={`/mesh?thread=${encodeURIComponent(wc.mesh_thread)}`}
                className="font-mono text-primary hover:underline"
                title={wc.mesh_thread}
              >
                thread:{shortThread(wc.mesh_thread)}
              </Link>
            </Chip>
          ) : null}
        </ul>
      ) : (
        <p className="text-[11px] text-muted-foreground/60">
          No work-context set. Use the Add button to point at a branch, PR, peer, or
          thread.
        </p>
      )}

      <WorkContextEditDialog
        open={editOpen}
        onOpenChange={setEditOpen}
        task={task}
        initial={wc}
        onSaved={() => {
          setEditOpen(false)
          onUpdate()
        }}
      />
    </section>
  )
}

// Chip — uniform sidebar row: icon on the left, content on the right
// inside a thin border. Kept inline rather than in its own component
// because the styling is purely visual and the variants are limited.
function Chip({ icon, children }: { icon: React.ReactNode; children: React.ReactNode }) {
  return (
    <li className="flex items-center gap-1.5 border border-border bg-card/40 px-2 py-1">
      <span className="shrink-0 text-muted-foreground">{icon}</span>
      <span className="min-w-0 flex-1 truncate">{children}</span>
    </li>
  )
}

// WorkContextEditDialog — inline form for setting all eight fields in
// one round-trip. Empty inputs are sent as empty strings, which the
// server interprets as "clear this key". Submit posts the merged
// patch and lets the parent refetch.
function WorkContextEditDialog({
  open,
  onOpenChange,
  task,
  initial,
  onSaved,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  task: Task
  initial: WorkContext
  onSaved: () => void
}) {
  const [state, setState] = useState<WorkContext>(initial)
  const [submitting, setSubmitting] = useState(false)

  // Re-seed the form whenever the dialog opens — keeps the inputs in
  // sync with whatever the latest task.meta says.
  useEffect(() => {
    if (open) setState(initial)
  }, [open, initial])

  const handleSubmit = async () => {
    setSubmitting(true)
    try {
      // Every field is sent as-is (including empty strings → clears) so
      // the server can decide what to drop. The struct contains only
      // the eight typed keys, so non-work-context lines in meta stay
      // untouched on the server side.
      await setWorkContext(task.workspace_id, task.id, {
        worktree: state.worktree ?? '',
        branch: state.branch ?? '',
        pr: state.pr ?? '',
        commits: state.commits ?? '',
        peer: state.peer ?? '',
        session: state.session ?? '',
        linear: state.linear ?? '',
        mesh_thread: state.mesh_thread ?? '',
      })
      toast.success('Work context updated')
      onSaved()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Update failed')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Work context</DialogTitle>
          <DialogDescription>
            Point this task at the branch, PR, peer, or mesh thread where the work
            actually lives. Empty fields clear the existing value.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-2.5">
          <Row label="Branch" placeholder="feat/x">
            <Input
              value={state.branch ?? ''}
              onChange={(e) => setState({ ...state, branch: e.target.value })}
              placeholder="feat/x"
            />
          </Row>
          <Row label="PR URL" placeholder="https://github.com/owner/repo/pull/42">
            <Input
              value={state.pr ?? ''}
              onChange={(e) => setState({ ...state, pr: e.target.value })}
              placeholder="https://github.com/owner/repo/pull/42"
            />
          </Row>
          <Row label="Worktree" placeholder="/abs/path/to/worktree">
            <Input
              value={state.worktree ?? ''}
              onChange={(e) => setState({ ...state, worktree: e.target.value })}
              placeholder="/abs/path/to/worktree"
            />
          </Row>
          <Row label="Commits" placeholder="abc1234..def5678">
            <Input
              value={state.commits ?? ''}
              onChange={(e) => setState({ ...state, commits: e.target.value })}
              placeholder="abc1234..def5678"
            />
          </Row>
          <Row label="Linear" placeholder="ENG-123">
            <Input
              value={state.linear ?? ''}
              onChange={(e) => setState({ ...state, linear: e.target.value })}
              placeholder="ENG-123"
            />
          </Row>
          <Row label="Peer" placeholder="libp2p peer id (46-52 chars)">
            <Input
              value={state.peer ?? ''}
              onChange={(e) => setState({ ...state, peer: e.target.value })}
              placeholder="QmYwAPJzv5..."
            />
          </Row>
          <Row label="Session" placeholder="session id">
            <Input
              value={state.session ?? ''}
              onChange={(e) => setState({ ...state, session: e.target.value })}
              placeholder="dashboard-9f9f"
            />
          </Row>
          <Row label="Mesh thread" placeholder="thread root id">
            <Input
              value={state.mesh_thread ?? ''}
              onChange={(e) => setState({ ...state, mesh_thread: e.target.value })}
              placeholder="01THREADROOT"
            />
          </Row>
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={submitting}>
            Cancel
          </Button>
          <Button onClick={handleSubmit} disabled={submitting}>
            {submitting ? <Loader2 className="h-3 w-3 animate-spin" /> : null}
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function Row({
  label,
  children,
}: {
  label: string
  placeholder?: string
  children: React.ReactNode
}) {
  return (
    <div className="space-y-1">
      <Label className="text-[10px] uppercase tracking-wider text-muted-foreground">
        {label}
      </Label>
      {children}
    </div>
  )
}
