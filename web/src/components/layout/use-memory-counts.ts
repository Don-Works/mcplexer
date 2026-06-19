// use-memory-counts — polls /memory/count + the pending offers list every
// 30s so the sidebar Memory entry can show "what's incoming" and the
// dashboard vitals strip can render total memory count without re-fetching.
//
// Two reads on a single timer: total stored memories (facts + notes), and
// pending peer-offers count. Both surface a 0 cleanly so the badge logic
// can decide to hide / mute the chip.

import { useState } from 'react'

import { countMemories, listMemoryOffers } from '@/api/memory'
import { usePolling } from '@/hooks/use-polling'

const POLL_MS = 30_000

interface MemoryCounts {
  total: number
  pendingOffers: number
}

export function useMemoryCounts(): MemoryCounts {
  const [counts, setCounts] = useState<MemoryCounts>({ total: 0, pendingOffers: 0 })

  usePolling(async () => {
    try {
      const [count, offers] = await Promise.all([
        countMemories(),
        listMemoryOffers({ pending_only: true }),
      ])
      setCounts({
        total: (count.facts ?? 0) + (count.notes ?? 0),
        pendingOffers: offers.length,
      })
    } catch {
      // Silent — sidebar shouldn't toast on transient blips.
    }
  }, POLL_MS)

  return counts
}
