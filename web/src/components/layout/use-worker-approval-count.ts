// useWorkerApprovalCount — polls the worker-approvals surface every
// 5s and returns the count of pending approvals (M1). Wired into the
// sidebar's Workers nav entry so a red dot shows up the moment a
// propose-mode run lands an approval. Same 5s cadence as
// useWorkerLiveCount — SSE replacement lands in M2.

import { useState } from 'react'

import { listWorkerApprovals } from '@/api/workers'
import { usePolling } from '@/hooks/use-polling'

const POLL_MS = 5_000

export function useWorkerApprovalCount(): number {
  const [count, setCount] = useState(0)

  usePolling(async () => {
    try {
      const rows = await listWorkerApprovals({ status: 'pending' })
      setCount(rows.length)
    } catch {
      // Silent — same rationale as useWorkerLiveCount.
    }
  }, POLL_MS)

  return count
}
