package readiness

import (
	"testing"
)

func TestTrackerStateTransitions(t *testing.T) {
	tests := []struct {
		name      string
		actions   func(*Tracker)
		wantState State
		wantReady bool
		wantDrain bool
	}{
		{
			name:      "initial state is starting",
			actions:   func(tr *Tracker) {},
			wantState: Starting,
			wantReady: false,
			wantDrain: false,
		},
		{
			name:      "SetReady transitions to ready",
			actions:   func(tr *Tracker) { tr.SetReady() },
			wantState: Ready,
			wantReady: true,
			wantDrain: false,
		},
		{
			name: "SetDraining from ready",
			actions: func(tr *Tracker) {
				tr.SetReady()
				tr.SetDraining()
			},
			wantState: Draining,
			wantReady: false,
			wantDrain: true,
		},
		{
			name: "SetDraining from starting skips ready",
			actions: func(tr *Tracker) {
				tr.SetDraining()
			},
			wantState: Draining,
			wantReady: false,
			wantDrain: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewTracker()
			tc.actions(tr)
			if got := tr.State(); got != tc.wantState {
				t.Errorf("State() = %q, want %q", got, tc.wantState)
			}
			if got := tr.IsReady(); got != tc.wantReady {
				t.Errorf("IsReady() = %v, want %v", got, tc.wantReady)
			}
			if got := tr.IsDraining(); got != tc.wantDrain {
				t.Errorf("IsDraining() = %v, want %v", got, tc.wantDrain)
			}
		})
	}
}

func TestTrackerConcurrentReads(t *testing.T) {
	tr := NewTracker()
	tr.SetReady()

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				_ = tr.State()
				_ = tr.IsReady()
				_ = tr.IsDraining()
			}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}
