package brain

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCommitter records commit calls so tests can assert coalescing without
// a real git repo.
type fakeCommitter struct {
	mu    sync.Mutex
	calls [][]string
	done  chan struct{}
}

func newFakeCommitter() *fakeCommitter {
	return &fakeCommitter{done: make(chan struct{}, 16)}
}

func (f *fakeCommitter) commit(_ context.Context, paths []string, _ string) error {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), paths...))
	f.mu.Unlock()
	f.done <- struct{}{}
	return nil
}

func (f *fakeCommitter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// newTestAC builds an AutoCommitter wired to a fake committer with tight
// idle/ceiling windows for fast tests.
func newTestAC(idle, ceiling time.Duration) (*AutoCommitter, *fakeCommitter) {
	fc := newFakeCommitter()
	ac := NewAutoCommitter(nil, idle, ceiling, nil)
	ac.commitForCB = fc.commit
	return ac, fc
}

func TestAutoCommit_DebounceCoalescesCommits(t *testing.T) {
	ac, fc := newTestAC(60*time.Millisecond, 5*time.Second)

	// Burst of writes within the idle window → exactly one commit.
	for i := 0; i < 5; i++ {
		ac.Notify([]string{"workspaces/ws/tasks/01J-a.md"})
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case <-fc.done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected a commit after the idle window")
	}
	// Give a beat to ensure no second commit fires.
	time.Sleep(100 * time.Millisecond)
	if n := fc.count(); n != 1 {
		t.Fatalf("expected exactly 1 coalesced commit, got %d", n)
	}
}

func TestAutoCommit_CeilingFlush(t *testing.T) {
	// Idle never settles (kept re-armed), but the ceiling must force a flush.
	ac, fc := newTestAC(200*time.Millisecond, 150*time.Millisecond)

	stop := make(chan struct{})
	go func() {
		tk := time.NewTicker(20 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				ac.Notify([]string{"workspaces/ws/tasks/01J-stream.md"})
			}
		}
	}()

	select {
	case <-fc.done:
	case <-time.After(2 * time.Second):
		close(stop)
		t.Fatal("expected a ceiling-forced commit despite continuous writes")
	}
	close(stop)
}

func TestAutoCommit_CloseFlushesPending(t *testing.T) {
	ac, fc := newTestAC(10*time.Second, 30*time.Second) // long windows: won't fire on their own
	ac.Notify([]string{"workspaces/ws/tasks/01J-x.md"})
	ac.Close()
	if fc.count() != 1 {
		t.Fatalf("Close should flush pending work, got %d commits", fc.count())
	}
	// Notify after Close is inert.
	ac.Notify([]string{"workspaces/ws/tasks/01J-y.md"})
	time.Sleep(50 * time.Millisecond)
	if fc.count() != 1 {
		t.Fatalf("Notify after Close must be inert, got %d commits", fc.count())
	}
}

func TestAutoCommit_NoPendingIsNoop(t *testing.T) {
	ac, fc := newTestAC(20*time.Millisecond, time.Second)
	ac.Notify(nil)          // nothing to commit
	ac.Notify([]string{""}) // empty path ignored
	time.Sleep(80 * time.Millisecond)
	if fc.count() != 0 {
		t.Fatalf("expected no commit with empty/no paths, got %d", fc.count())
	}
}

func TestBuildCommitMessage(t *testing.T) {
	tests := []struct {
		name          string
		touched       []string
		session       string
		agent         string
		wantInSubj    []string
		wantInBody    []string
		wantNotInSubj []string
	}{
		{
			name:       "single workspace tasks",
			touched:    []string{"workspaces/mcplexer/tasks/01J-a.md", "workspaces/mcplexer/tasks/01J-b.md"},
			session:    "sess_123",
			agent:      "claude-opus",
			wantInSubj: []string{"chore(brain): autosave", "mcplexer", "2 tasks", "[machine]"},
			wantInBody: []string{"Touched:", "workspaces/mcplexer/tasks/01J-a.md", "Session: sess_123", "Agent: claude-opus"},
		},
		{
			name:          "mixed workspaces drops workspace from subject",
			touched:       []string{"workspaces/a/tasks/x.md", "workspaces/b/memory/y.md"},
			wantInSubj:    []string{"1 task", "1 memory"},
			wantNotInSubj: []string{"workspaces/a", "— a —", "— b —"},
		},
		{
			name:       "memory pluralisation",
			touched:    []string{"workspaces/w/memory/a.md", "workspaces/w/memory/b.md", "workspaces/w/memory/c.md"},
			wantInSubj: []string{"3 memories", "w —"},
		},
		{
			name:       "no provenance omits footer",
			touched:    []string{"workspaces/w/tasks/x.md"},
			wantInSubj: []string{"1 task"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := BuildCommitMessage(tc.touched, tc.session, tc.agent)
			subject := strings.SplitN(msg, "\n", 2)[0]
			for _, want := range tc.wantInSubj {
				if !strings.Contains(subject, want) {
					t.Errorf("subject %q missing %q", subject, want)
				}
			}
			for _, notWant := range tc.wantNotInSubj {
				if strings.Contains(subject, notWant) {
					t.Errorf("subject %q should not contain %q", subject, notWant)
				}
			}
			for _, want := range tc.wantInBody {
				if !strings.Contains(msg, want) {
					t.Errorf("message missing %q\n---\n%s", want, msg)
				}
			}
			if tc.session == "" && tc.agent == "" && strings.Contains(msg, "Session:") {
				t.Errorf("message should omit Session footer with no provenance:\n%s", msg)
			}
		})
	}
}
