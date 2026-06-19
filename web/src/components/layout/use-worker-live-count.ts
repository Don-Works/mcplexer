import { useWorkersRealtime } from '@/pages/workers/use-workers-realtime'
import { runningCount, runningWorkers } from '@/pages/workers/worker-utils'

// useWorkerLiveCount / useActiveWorkers derive sidebar state from the shared
// realtime Workers snapshot. The snapshot is seeded by /workers and updated
// by the multiplexed "workers" SSE channel, so the layout no longer runs its
// own 5s poller.
export function useWorkerLiveCount(): number {
  return runningCount(useWorkersRealtime().rows)
}

export function useActiveWorkers() {
  return runningWorkers(useWorkersRealtime().rows)
}
