package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newRetentionTestDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "ret.db"))
	if err != nil {
		t.Fatalf("new db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestEnsureAuditPruneJobRegistration is the cron-registration check
// the task brief asks for: after seeding, the audit_prune row
// shows up in ListScheduledJobs with the expected kind + spec.
func TestEnsureAuditPruneJobRegistration(t *testing.T) {
	db := newRetentionTestDB(t)
	ctx := context.Background()
	if err := ensureAuditPruneJob(ctx, db); err != nil {
		t.Fatalf("seed: %v", err)
	}

	jobs, err := db.ListScheduledJobs(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found *store.ScheduledJob
	for i := range jobs {
		if jobs[i].ID == auditPruneJobID {
			found = &jobs[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("audit_prune job not registered; got %d rows", len(jobs))
	}
	if found.Kind != scheduler.KindAuditPrune {
		t.Errorf("kind = %q, want %q", found.Kind, scheduler.KindAuditPrune)
	}
	if found.Spec != auditPruneCron {
		t.Errorf("spec = %q, want %q", found.Spec, auditPruneCron)
	}
	if !found.Enabled {
		t.Errorf("audit_prune should be enabled by default")
	}
	if found.NextRunAt == nil {
		t.Errorf("next_run_at not stamped")
	}
}

// TestEnsureAuditPruneJobIdempotent calls the seeder twice and
// verifies the second call is a no-op — the daemon restart path
// must not create duplicate rows.
func TestEnsureAuditPruneJobIdempotent(t *testing.T) {
	db := newRetentionTestDB(t)
	ctx := context.Background()
	if err := ensureAuditPruneJob(ctx, db); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if err := ensureAuditPruneJob(ctx, db); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	jobs, _ := db.ListScheduledJobs(ctx)
	hits := 0
	for _, j := range jobs {
		if j.ID == auditPruneJobID {
			hits++
		}
	}
	if hits != 1 {
		t.Errorf("audit_prune row count = %d, want 1", hits)
	}
}

// TestStorePruneExecutorWiresBothTables drives the adapter end-to-end
// against a real sqlite db: insert audit + worker_run rows, run
// Prune, then assert both tables were touched. Catches the easy
// regression where someone wires the executor but only calls one
// of the two store methods.
func TestStorePruneExecutorWiresBothTables(t *testing.T) {
	db := newRetentionTestDB(t)
	ctx := context.Background()

	// Seed a workspace + worker so the worker_run insert satisfies its
	// FK chain.
	ws := &store.Workspace{Name: "ret-ws", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("ws: %v", err)
	}
	as := &store.AuthScope{Name: "ret-scope", Type: "env"}
	if err := db.CreateAuthScope(ctx, as); err != nil {
		t.Fatalf("scope: %v", err)
	}
	w := &store.Worker{
		Name: "ret-worker", ModelProvider: "anthropic",
		ModelID: "claude-opus-4-7", SecretScopeID: as.ID,
		ScheduleSpec: "0 9 * * *", WorkspaceID: ws.ID, Enabled: true,
	}
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatalf("worker: %v", err)
	}

	old := time.Now().UTC().Add(-365 * 24 * time.Hour)
	for i := 0; i < 5; i++ {
		_ = db.InsertAuditRecord(ctx, &store.AuditRecord{
			Timestamp: old, CreatedAt: old,
			ToolName: "test__tool", Status: "success",
		})
	}
	for i := 0; i < 5; i++ {
		_ = db.CreateWorkerRun(ctx, &store.WorkerRun{
			WorkerID: w.ID, StartedAt: old, Status: "success",
		})
	}

	exec := newStorePruneExecutor(db)
	policy := scheduler.PrunePolicy{
		AuditRetentionDays:     90,
		WorkerRunKeepPerWorker: 2,
		WorkerRunCapDays:       180,
	}
	auditDel, runsDel, err := exec.Prune(ctx, policy, time.Now().UTC())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if auditDel != 5 {
		t.Errorf("audit deleted = %d, want 5", auditDel)
	}
	if runsDel != 3 {
		t.Errorf("worker_runs deleted = %d, want 3 (5 - 2 keep)", runsDel)
	}
}

// TestRetentionPolicyFromConfig confirms the YAML defaults flow
// into the scheduler policy without surprises.
func TestRetentionPolicyFromConfig(t *testing.T) {
	c := config.DefaultAuditRetention()
	p := retentionPolicyFromConfig(c)
	if p.AuditRetentionDays != c.AuditDays {
		t.Errorf("AuditRetentionDays = %d, want %d", p.AuditRetentionDays, c.AuditDays)
	}
	if p.WorkerRunKeepPerWorker != c.WorkerRunKeepPerWorker {
		t.Errorf("WorkerRunKeepPerWorker = %d, want %d", p.WorkerRunKeepPerWorker, c.WorkerRunKeepPerWorker)
	}
	if p.WorkerRunCapDays != c.WorkerRunCapDays {
		t.Errorf("WorkerRunCapDays = %d, want %d", p.WorkerRunCapDays, c.WorkerRunCapDays)
	}
}
