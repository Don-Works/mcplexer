package control

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/don-works/mcplexer/internal/brain"
)

// callBrainMigration dispatches the M5 migration tooling. These tools share
// the brainMigrationConfig (serializer + indexer + settings) so import-time
// writes are byte-identical to live dual-writes. When the brain is disabled
// the config is nil and every tool returns a structured error rather than
// panicking.
func (b *InternalBackend) callBrainMigration(ctx context.Context, name string) json.RawMessage {
	if b.brainMig == nil {
		return errorResult("brain migration tooling not available — brain not wired (enable MCPLEXER_BRAIN_ENABLED / settings.brain_enabled and restart)")
	}
	switch name {
	case "brain_init":
		return b.handleBrainInit(ctx)
	case "brain_import":
		return b.handleBrainImport(ctx)
	case "brain_verify":
		return b.handleBrainVerify(ctx)
	case "brain_disable":
		return b.handleBrainDisable(ctx)
	}
	return errorResult("unknown brain migration tool: " + name)
}

// handleBrainInit takes a rollback backup snapshot FIRST, scaffolds the repo
// layout (idempotent — never clobbers existing files), then git init +
// initial commit (idempotent — skips an existing repo). The whole operation
// is rollback-able via the snapshot recorded before any file is written.
func (b *InternalBackend) handleBrainInit(ctx context.Context) json.RawMessage {
	dir := b.brainMig.cfg.Dir
	var backupID string
	if b.backupSvc != nil {
		m, err := b.backupSvc.Create(ctx, "pre-brain-init", true)
		if err != nil {
			return errorResult("brain init: pre-snapshot failed (aborting before any file write): " + err.Error())
		}
		backupID = m.ID
	}
	if err := brain.ScaffoldRepo(dir); err != nil {
		return errorResult("brain init: scaffold: " + err.Error())
	}
	gitInitialised := false
	if b.brainGit != nil && b.brainGit.Available() {
		if err := b.brainGit.Init(ctx); err != nil {
			return errorResult("brain init: git init: " + err.Error())
		}
		gitInitialised = true
	}
	res, mErr := jsonResult(map[string]any{
		"dir":             dir,
		"backup_id":       backupID,
		"git_initialised": gitInitialised,
		"note":            "Repo scaffolded. Run brain_import next to populate it (parity-verified), then enable the flag.",
	})
	if mErr != nil {
		return errorResult(mErr.Error())
	}
	return res
}

// handleBrainImport runs the parity-verified one-way DB→Brain import via
// the shared serializer + indexer, then asserts the re-derived index matches
// the live DB. It does NOT flip the enable flag and does NOT mutate the DB —
// the caller enables the brain ONLY after parity_ok is true (SPEC §10).
func (b *InternalBackend) handleBrainImport(ctx context.Context) json.RawMessage {
	if b.brainMig.ser == nil || b.brainMig.ix == nil {
		return errorResult("brain import: serializer/indexer not wired")
	}
	im := brain.NewImporter(b.brainMig.cfg, b.store, b.brainMig.ser, b.brainMig.ix)
	rep, err := im.Run(ctx, b.store)
	if err != nil {
		return errorResult("brain import: " + err.Error())
	}
	if !rep.ParityOK {
		// Surface the report (with drift) as a structured error so the
		// operator can see WHY parity failed without trusting the brain.
		payload, _ := json.Marshal(rep)
		return errorResult("brain import: PARITY CHECK FAILED — the re-derived index does not match the live DB; the brain was NOT enabled and the DB remains authoritative. Report: " + string(payload))
	}
	res, mErr := jsonResult(rep)
	if mErr != nil {
		return errorResult(mErr.Error())
	}
	return res
}

// handleBrainVerify re-derives every indexed file's row and diffs it against
// the live DB, reporting drift without mutating anything.
func (b *InternalBackend) handleBrainVerify(ctx context.Context) json.RawMessage {
	rep, err := brain.Verify(ctx, b.brainMig.cfg, b.store)
	if err != nil {
		return errorResult("brain verify: " + err.Error())
	}
	res, mErr := jsonResult(map[string]any{
		"ok":            rep.OK(),
		"files_checked": rep.FilesChecked,
		"drifts":        rep.Drifts,
	})
	if mErr != nil {
		return errorResult(mErr.Error())
	}
	return res
}

// handleBrainDisable flips settings.brain_enabled=false (preserving every
// other settings key) so the gateway resumes reading the authoritative DB
// on the next restart. The brain repo is LEFT on disk — nothing destroyed,
// fully reversible (SPEC §10 reversibility).
func (b *InternalBackend) handleBrainDisable(ctx context.Context) json.RawMessage {
	if b.brainMig.settings == nil {
		return errorResult("brain disable: settings store not wired")
	}
	raw, err := b.brainMig.settings.GetSettings(ctx)
	if err != nil {
		return errorResult("brain disable: load settings: " + err.Error())
	}
	merged, err := setSettingsKeyFalse(raw, brain.SettingsKey)
	if err != nil {
		return errorResult("brain disable: " + err.Error())
	}
	if err := b.brainMig.settings.UpdateSettings(ctx, merged); err != nil {
		return errorResult("brain disable: persist settings: " + err.Error())
	}
	res, mErr := jsonResult(map[string]any{
		"brain_enabled": false,
		"note":          "settings.brain_enabled set to false. The brain repo is left on disk as an archive. Restart the daemon (or it takes effect on next restart) so the gateway resumes authoritative-DB reads. Re-enable + reindex to resume.",
	})
	if mErr != nil {
		return errorResult(mErr.Error())
	}
	return res
}

// setSettingsKeyFalse merges {key:false} into the settings JSON blob,
// preserving every other key. An empty/whitespace blob starts a fresh
// object. A non-object blob is an error (we never overwrite an unexpected
// shape).
func setSettingsKeyFalse(raw json.RawMessage, key string) (json.RawMessage, error) {
	m := map[string]any{}
	if s := strings.TrimSpace(string(raw)); s != "" {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
	}
	m[key] = false
	return json.Marshal(m)
}
