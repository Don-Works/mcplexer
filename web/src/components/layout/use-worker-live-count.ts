import { useWorkersRealtime } from '@/pages/workers/use-workers-realtime'
import { liveDelegationWorkers, liveDurableWorkers } from '@/pages/workers/worker-utils'

// useWorkerLiveCount / useActiveWorkers derive sidebar state from the shared
// realtime Workers snapshot. The snapshot is seeded by /workers and updated
// by the multiplexed "workers" SSE channel, so the layout no longer runs its
// own 5s poller.
export function useWorkerLiveCount(): number {
  return liveDurableWorkers(useWorkersRealtime().rows).length
}

export function useActiveWorkers() {
  return liveDurableWorkers(useWorkersRealtime().rows)
}

export function useLiveDelegationCount(): number {
  return liveDelegationWorkers(useWorkersRealtime().rows).length
}
