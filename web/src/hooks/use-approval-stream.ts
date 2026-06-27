import { useSyncExternalStore } from 'react'
import type { ApprovalEvent, ToolApproval } from '@/api/types'
import { subscribeEvent, useEventStreamStatus } from '@/hooks/use-event-stream'
import { fireApprovalPending } from '@/components/notifications/use-os-notifications'

// Approval pending-queue store. Every useApprovalStream() consumer shares ONE
// subscription to the multiplexed event hub's 'approvals' channel (see
// hooks/use-event-stream.ts) — which in turn shares ONE network connection
// with all the other always-on streams. This replaced a dedicated
// /approvals/stream EventSource; the rationale (Chrome's 6-per-origin
// HTTP/1.1 cap) is unchanged, just solved one level up.
//
// State lives in module scope; React subscribers read it via
// useSyncExternalStore so unrelated state changes don't force a re-render.

interface State {
  pending: ToolApproval[]
  connected: boolean
  resolvedIds: string[]
}

let state: State = { pending: [], connected: false, resolvedIds: [] }
const storeListeners = new Set<() => void>()

let unsubHub: (() => void) | null = null
let refcount = 0

function emit() {
  for (const l of storeListeners) l()
}

function setState(patch: Partial<State>) {
  state = { ...state, ...patch }
  emit()
}

function handleApproval(data: unknown) {
  const evt = data as ApprovalEvent
  if (evt.type === 'pending') {
    setState({
      pending: [...state.pending.filter((a) => a.id !== evt.approval.id), evt.approval],
      resolvedIds: state.resolvedIds.filter((id) => id !== evt.approval.id),
    })
    // OS-notification fan-out lives here so it stays on regardless of which
    // page is mounted.
    fireApprovalPending({
      id: evt.approval.id,
      tool_name: evt.approval.tool_name,
    })
  } else if (evt.type === 'resolved') {
    const id = evt.approval.id
    setState({
      pending: state.pending.filter((a) => a.id !== id),
      resolvedIds: state.resolvedIds.includes(id)
        ? state.resolvedIds
        : [...state.resolvedIds, id].slice(-300),
    })
  }
}

function subscribe(l: () => void): () => void {
  storeListeners.add(l)
  refcount++
  if (refcount === 1) {
    unsubHub = subscribeEvent('approvals', handleApproval)
    setState({ connected: true })
  }
  return () => {
    storeListeners.delete(l)
    refcount--
    if (refcount === 0) {
      unsubHub?.()
      unsubHub = null
      setState({ connected: false })
    }
  }
}

export function useApprovalStream() {
  const snap = useSyncExternalStore(subscribe, () => state, () => state)
  const streamStatus = useEventStreamStatus()
  return { ...snap, connected: streamStatus === 'open' }
}
