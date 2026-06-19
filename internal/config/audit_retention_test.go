package config

import "testing"

// TestResolveRetentionAbsentUsesDefaults ensures the most common
// in-the-wild config (no audit_retention block at all) flows through
// to the documented defaults.
func TestResolveRetentionAbsentUsesDefaults(t *testing.T) {
	c := &FileConfig{}
	got := c.ResolveRetention()
	def := DefaultAuditRetention()
	if got != def {
		t.Errorf("ResolveRetention = %+v, want %+v", got, def)
	}
}

// TestResolveRetentionPartialBlockFillsDefaults asserts the merge
// behaviour: an explicit value overrides; a zero/omitted field
// inherits the default.
func TestResolveRetentionPartialBlockFillsDefaults(t *testing.T) {
	c := &FileConfig{
		AuditRetention: &AuditRetentionConfig{
			AuditDays: 30, // override
			// WorkerRunKeepPerWorker omitted -> default
			// WorkerRunCapDays omitted -> default
		},
	}
	got := c.ResolveRetention()
	def := DefaultAuditRetention()
	if got.AuditDays != 30 {
		t.Errorf("AuditDays = %d, want 30", got.AuditDays)
	}
	if got.WorkerRunKeepPerWorker != def.WorkerRunKeepPerWorker {
		t.Errorf("WorkerRunKeepPerWorker = %d, want %d (default)",
			got.WorkerRunKeepPerWorker, def.WorkerRunKeepPerWorker)
	}
	if got.WorkerRunCapDays != def.WorkerRunCapDays {
		t.Errorf("WorkerRunCapDays = %d, want %d (default)",
			got.WorkerRunCapDays, def.WorkerRunCapDays)
	}
}

// TestParseAuditRetentionYAML covers the YAML round-trip — the
// daemon reads this block via Parse() at boot.
func TestParseAuditRetentionYAML(t *testing.T) {
	yaml := []byte(`
audit_retention:
  audit_days: 60
  worker_run_keep_per_worker: 500
  worker_run_cap_days: 365
`)
	cfg, err := Parse(yaml)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.AuditRetention == nil {
		t.Fatal("audit_retention block not parsed")
	}
	if cfg.AuditRetention.AuditDays != 60 {
		t.Errorf("AuditDays = %d, want 60", cfg.AuditRetention.AuditDays)
	}
	if cfg.AuditRetention.WorkerRunKeepPerWorker != 500 {
		t.Errorf("WorkerRunKeepPerWorker = %d, want 500",
			cfg.AuditRetention.WorkerRunKeepPerWorker)
	}
	if cfg.AuditRetention.WorkerRunCapDays != 365 {
		t.Errorf("WorkerRunCapDays = %d, want 365",
			cfg.AuditRetention.WorkerRunCapDays)
	}
}

// TestParseAuditRetentionValidatesNegatives confirms the validator
// rejects nonsense retention knobs before the daemon ever reaches
// the prune job.
func TestParseAuditRetentionValidatesNegatives(t *testing.T) {
	cases := []string{
		"audit_retention:\n  audit_days: -1\n",
		"audit_retention:\n  worker_run_keep_per_worker: -1\n",
		"audit_retention:\n  worker_run_cap_days: -1\n",
	}
	for _, y := range cases {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("expected validation error for %q", y)
		}
	}
}
