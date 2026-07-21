import { useState } from 'react'
import { toast } from 'sonner'
import { resolveApproval } from '@/api/client'

// useResolveApproval owns the approve/deny interaction shared by the
// pending card and the detail drawer: the reason field, the in-flight
// flag, the deny-requires-a-reason guard, and the toast + callback on
// success. Keeping it in one place means both surfaces behave identically.
export function useResolveApproval(approvalID: string, onResolved: () => void) {
  const [reason, setReason] = useState('')
  const [resolving, setResolving] = useState(false)

  async function resolve(approved: boolean) {
    if (!approved && !reason.trim()) {
      toast.error('A reason is required when denying')
      return
    }
    setResolving(true)
    try {
      await resolveApproval(approvalID, { approved, reason })
      toast.success(approved ? 'Approved' : 'Denied')
      onResolved()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to resolve')
    } finally {
      setResolving(false)
    }
  }

  return { reason, setReason, resolving, resolve }
}
