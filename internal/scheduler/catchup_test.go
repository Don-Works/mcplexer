package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestSchedulerCatchUp(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		jobs       []store.ScheduledJob
		clockNow   time.Time
		wantFires  int32
		wantInHeap int
	}{
		{
			name:       "no overdue jobs does nothing",
			clockNow:   baseTime,
			wantFires:  0,
			wantInHeap: 0,
		},
		{
			name: "overdue interval job fires once",
			jobs: []store.ScheduledJob{
				{
					ID: "j1", Kind: KindInterval, Spec: "5m",
					Enabled:   true,
					NextRunAt: ptrTime(baseTime.Add(-10 * time.Minute)),
				},
			},
			clockNow:   baseTime,
			wantFires:  1,
			wantInHeap: 1,
		},
		{
			name: "multiple overdue jobs fires at most maxCatchUpPerJob",
			jobs: []store.ScheduledJob{
				{
					ID: "j1", Kind: KindInterval, Spec: "1m",
					Enabled:   true,
					NextRunAt: ptrTime(baseTime.Add(-5 * time.Minute)),
				},
				{
					ID: "j2", Kind: KindInterval, Spec: "2m",
					Enabled:   true,
					NextRunAt: ptrTime(baseTime.Add(-3 * time.Minute)),
				},
			},
			clockNow:   baseTime,
			wantFires:  1,
			wantInHeap: 2,
		},
		{
			name: "future job not fired",
			jobs: []store.ScheduledJob{
				{
					ID: "j1", Kind: KindInterval, Spec: "5m",
					Enabled:   true,
					NextRunAt: ptrTime(baseTime.Add(10 * time.Minute)),
				},
			},
			clockNow:   baseTime,
			wantFires:  0,
			wantInHeap: 1,
		},
		{
			name: "disabled job not fired even if overdue",
			jobs: []store.ScheduledJob{
				{
					ID: "j1", Kind: KindInterval, Spec: "5m",
					Enabled:   false,
					NextRunAt: ptrTime(baseTime.Add(-10 * time.Minute)),
				},
			},
			clockNow:   baseTime,
			wantFires:  0,
			wantInHeap: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			ms := newMemStore()
			for _, j := range tc.jobs {
				if err := ms.CreateScheduledJob(ctx, &j); err != nil {
					t.Fatal(err)
				}
			}

			clk := newFakeClock(tc.clockNow)
			fe := &fakeExecutor{}

			s := New(ms, &fakeApprover{approve: true}, nil, clk)
			s.exec = fe
			if err := s.Start(ctx); err != nil {
				t.Fatal(err)
			}

			if got := int32(len(fe.calls)); got != tc.wantFires {
				t.Errorf("fires = %d, want %d", got, tc.wantFires)
			}

			s.mu.Lock()
			heapLen := s.jobs.Len()
			s.mu.Unlock()
			if heapLen != tc.wantInHeap {
				t.Errorf("heap size = %d, want %d", heapLen, tc.wantInHeap)
			}

			_ = s.Stop(time.Second)
		})
	}
}

func TestSchedulerCatchUp_RespectsContext(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ms := newMemStore()
	job := store.ScheduledJob{
		ID: "j1", Kind: KindInterval, Spec: "1m",
		Enabled:   true,
		NextRunAt: ptrTime(baseTime.Add(-5 * time.Minute)),
	}
	if err := ms.CreateScheduledJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}

	clk := newFakeClock(baseTime)
	fe := &fakeExecutor{}

	s := New(ms, &fakeApprover{approve: true}, nil, clk)
	s.exec = fe
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	if len(fe.calls) != 0 {
		t.Errorf("fires = %d, want 0 (context cancelled before catch-up)", len(fe.calls))
	}
	_ = s.Stop(time.Second)
}
