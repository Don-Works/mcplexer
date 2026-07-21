// use-task-offers-count — polls /tasks/offers every 30s for the count
// of incoming offers still awaiting the operator. Drives the info-badge
// on the Tasks sidebar entry. Silent on transient errors.

import { useState } from 'react'

import { listTaskOffers } from '@/api/tasks'
import { usePolling } from '@/hooks/use-polling'

const POLL_MS = 30_000

export function useTaskOffersCount(): number {
  const [count, setCount] = useState(0)

  usePolling(async () => {
    try {
      const rows = await listTaskOffers({
        direction: 'incoming',
        state: 'pending',
        limit: 100,
      })
      setCount(rows.length)
    } catch {
      // Silent — sidebar shouldn't toast on transient blips.
    }
  }, POLL_MS)

  return count
}
