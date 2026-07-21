package scheduler

import (
	"container/heap"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// pendingJob is one entry in the scheduler's min-heap.
type pendingJob struct {
	job     store.ScheduledJob
	nextRun time.Time
	index   int
}

// jobHeap is a min-heap of pendingJob ordered by nextRun ascending.
type jobHeap []*pendingJob

func (h jobHeap) Len() int           { return len(h) }
func (h jobHeap) Less(i, j int) bool { return h[i].nextRun.Before(h[j].nextRun) }
func (h jobHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

// Push appends an item to the heap. Called by container/heap.
func (h *jobHeap) Push(x any) {
	pj := x.(*pendingJob)
	pj.index = len(*h)
	*h = append(*h, pj)
}

// Pop removes the last entry. Called by container/heap (after swap).
func (h *jobHeap) Pop() any {
	old := *h
	n := len(old)
	pj := old[n-1]
	old[n-1] = nil
	pj.index = -1
	*h = old[0 : n-1]
	return pj
}

// peek returns the smallest entry without removing it. Returns nil for
// an empty heap.
func (h jobHeap) peek() *pendingJob {
	if len(h) == 0 {
		return nil
	}
	return h[0]
}

// removeByID locates and removes the entry for jobID. Returns true when
// found.
func (h *jobHeap) removeByID(jobID string) bool {
	for i, pj := range *h {
		if pj.job.ID == jobID {
			heap.Remove(h, i)
			return true
		}
	}
	return false
}

// upsertByID either updates the next-run time of an existing entry (and
// reheaps) or pushes a brand new entry.
func (h *jobHeap) upsertByID(j store.ScheduledJob, nextRun time.Time) {
	for i, pj := range *h {
		if pj.job.ID == j.ID {
			pj.job = j
			pj.nextRun = nextRun
			heap.Fix(h, i)
			return
		}
	}
	heap.Push(h, &pendingJob{job: j, nextRun: nextRun})
}
