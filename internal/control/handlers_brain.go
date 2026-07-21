package control

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/don-works/mcplexer/internal/brain"
)

// callBrain dispatches the brain git backplane admin tools. Lives on
// InternalBackend because they need the *brain.Git that the gateway-style
// (store, args) handlers don't have. Manual-push posture (Appendix B
// decision #6): brain_push pulls --rebase --autostash then pushes; the
// daemon never pushes on a timer.
func (b *InternalBackend) callBrain(ctx context.Context, name string) json.RawMessage {
	if b.brainGit == nil || !b.brainGit.Available() {
		return errorResult("brain git backplane not available — brain disabled or git binary not on PATH")
	}
	switch name {
	case "brain_push":
		return b.handleBrainPush(ctx)
	case "brain_status":
		return b.handleBrainStatus(ctx)
	}
	return errorResult("unknown brain tool: " + name)
}

// handleBrainPush performs the manual sync: pull --rebase --autostash, then
// push. A rebase conflict is surfaced (not auto-resolved) as a structured
// error carrying git's output so the operator can resolve it in VSCode.
func (b *InternalBackend) handleBrainPush(ctx context.Context) json.RawMessage {
	if err := b.brainGit.PullRebase(ctx); err != nil {
		var ce *brain.ConflictError
		if errors.As(err, &ce) {
			res, _ := jsonResult(map[string]any{
				"pushed":   false,
				"conflict": true,
				"detail":   ce.Output,
				"note":     "Rebase hit a conflict and was aborted. Resolve the conflicting brain files in VSCode, commit, then push again.",
			})
			return res
		}
		return errorResult("brain pull --rebase: " + err.Error())
	}
	if err := b.brainGit.Push(ctx); err != nil {
		return errorResult("brain push: " + err.Error())
	}
	st, err := b.brainGit.Status(ctx)
	if err != nil {
		return errorResult("brain status after push: " + err.Error())
	}
	res, mErr := jsonResult(map[string]any{
		"pushed":   true,
		"conflict": false,
		"status":   st,
	})
	if mErr != nil {
		return errorResult(mErr.Error())
	}
	return res
}

// handleBrainStatus reports ahead/behind/dirty for the dashboard.
func (b *InternalBackend) handleBrainStatus(ctx context.Context) json.RawMessage {
	st, err := b.brainGit.Status(ctx)
	if err != nil {
		return errorResult("brain status: " + err.Error())
	}
	res, mErr := jsonResult(st)
	if mErr != nil {
		return errorResult(mErr.Error())
	}
	return res
}

// handleBrainMigrateSecrets runs the one-time, idempotent secret migration
// (M3): decrypt every auth_scope value + OAuth client secret with the
// current age key, re-encrypt into the Brain's SOPS+age scopes.enc.yaml,
// round-trip verify, and LEAVE the DB blobs in place for dual-read rollout.
func (b *InternalBackend) handleBrainMigrateSecrets(ctx context.Context) json.RawMessage {
	if b.brainSecrets == nil {
		return errorResult("brain_migrate_secrets not available — brain disabled or no age recipients configured")
	}
	if b.enc == nil {
		return errorResult("brain_migrate_secrets needs the age encryptor (no age key configured)")
	}
	report, err := brain.MigrateSecrets(
		ctx,
		b.brainSecrets.dir,
		b.brainSecrets.recipients,
		b.store,
		b.enc,
		b.brainSecrets.ageKeyFile,
	)
	if err != nil {
		return errorResult("brain migrate secrets: " + err.Error())
	}
	if !report.RoundTripOK {
		return errorResult("brain migrate secrets: round-trip verification FAILED — the SOPS file was written but does not decrypt back to the source values; DB blobs left intact, do NOT trust the brain secret source until resolved")
	}
	res, mErr := jsonResult(report)
	if mErr != nil {
		return errorResult(mErr.Error())
	}
	return res
}
