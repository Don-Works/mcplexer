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
