package admin_test

import (
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

func TestAnnotateDeliverable(t *testing.T) {
	cases := []struct {
		name   string
		run    store.WorkerRun
		want   string
		commit string
		branch string
	}{
		{
			name: "success with commit",
			run: store.WorkerRun{
				Status:      "success",
				OutputText:  "STATUS: success\ncommit: abc1234\nCHANGED: internal/foo.go",
				InputTokens: 1000,
			},
			want:   "success_with_output",
			commit: "abc1234",
		},
		{
			name: "spend no commit",
			run: store.WorkerRun{
				Status:      "success",
				OutputText:  "STATUS: success\nCHANGED: investigated only",
				InputTokens: 5000,
				CostUSD:     0.12,
			},
			want: "spend_no_commit",
		},
		{
			name: "adapter failure no output",
			run: store.WorkerRun{
				Status: "failure",
				Error:  "adapter send: grok_cli: run: signal: killed",
			},
			want: "failed_no_output",
		},
		{
			name: "blocked worker report",
			run: store.WorkerRun{
				Status:     "success",
				OutputText: "STATUS: blocked\nRISKS: missing credentials",
			},
			want: "failed_no_output",
		},
		{
			// Regression: a local CLI model glued the STATUS token onto the
			// end of a preceding sentence ("…not inventing a value.STATUS:
			// blocked"). The bare ^ anchor missed it, so the blocked
			// self-report was dropped and the run mis-read as spend_no_commit.
			name: "blocked worker report glued to prose",
			run: store.WorkerRun{
				Status:     "success",
				OutputText: "Confirming: no agreed value.STATUS: blocked\nQUESTION: which timeout, and what seconds?\nRISKS: none",
			},
			want: "failed_no_output",
		},
		{
			name: "branch only",
			run: store.WorkerRun{
				Status:         "success",
				OutputText:     "STATUS: success\nbranch: fix/foo\nTESTED: go test ./...",
				ToolCallsCount: 3,
			},
			want:   "success_with_output",
			branch: "fix/foo",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.run
			admin.AnnotateDeliverableForTest(&r)
			if r.DeliverableStatus != tc.want {
				t.Fatalf("status = %q, want %q", r.DeliverableStatus, tc.want)
			}
			if tc.commit != "" && r.DeliverableCommit != tc.commit {
				t.Fatalf("commit = %q, want %q", r.DeliverableCommit, tc.commit)
			}
			if tc.branch != "" && r.DeliverableBranch != tc.branch {
				t.Fatalf("branch = %q, want %q", r.DeliverableBranch, tc.branch)
			}
		})
	}
}

func TestAnnotateDeliverableTrustsRunnerSnapshotOverWorkerText(t *testing.T) {
	tests := []struct {
		name       string
		run        store.WorkerRun
		wantStatus string
		wantCommit string
		wantBranch string
	}{
		{
			name: "clean trusted snapshot is not an artifact",
			run: store.WorkerRun{
				Status: "success", InputTokens: 100,
				ResultBranch: "mcplexer/delegation/clean", ResultCommit: "base", ResultChanged: false,
				OutputText: "STATUS: success\ncommit: ffffffff\nbranch: worker/fake",
			},
			wantStatus: "spend_no_commit",
		},
		{
			name: "changed trusted success",
			run: store.WorkerRun{
				Status: "success", ResultBranch: "mcplexer/delegation/changed", ResultCommit: "deadbeef", ResultChanged: true,
			},
			wantStatus: "success_with_output", wantCommit: "deadbeef", wantBranch: "mcplexer/delegation/changed",
		},
		{
			name: "changed trusted failure is partial",
			run: store.WorkerRun{
				Status: "failure", ResultBranch: "mcplexer/delegation/partial", ResultCommit: "cafebabe", ResultChanged: true,
			},
			wantStatus: "partial", wantCommit: "cafebabe", wantBranch: "mcplexer/delegation/partial",
		},
		{
			name: "snapshot failure cannot parse worker fake artifact",
			run: store.WorkerRun{
				Status: "failure", ResultBranch: "mcplexer/delegation/recovery", ResultChanged: false,
				OutputText: "STATUS: success\ncommit: ffffffff\nbranch: worker/fake",
			},
			wantStatus: "failed_no_output",
		},
		{
			name:       "legacy artifactless partial remains partial",
			run:        store.WorkerRun{Status: "partial", OutputText: "partial analysis only"},
			wantStatus: "partial",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run := tc.run
			admin.AnnotateDeliverableForTest(&run)
			if run.DeliverableStatus != tc.wantStatus || run.DeliverableCommit != tc.wantCommit || run.DeliverableBranch != tc.wantBranch {
				t.Fatalf("deliverable = status %q commit %q branch %q, want %q %q %q",
					run.DeliverableStatus, run.DeliverableCommit, run.DeliverableBranch,
					tc.wantStatus, tc.wantCommit, tc.wantBranch)
			}
		})
	}
}
