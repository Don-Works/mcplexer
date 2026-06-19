// Package admin_test — crud_audit_test.go locks the worker_admin.*
// audit contract: every CRUD mutation emits a record with the right
// event name, status, payload shape, and worker_id; long fields are
// fingerprinted not bodied; failure paths still emit (status=error)
// while still bubbling the underlying error back to the caller.
package admin_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/workers/admin"
)

// TestAudit_Create_EmitsFingerprintedFields locks in the create audit
// shape: event name, ok status, fingerprinted long fields, literal
// short fields. No prompt body must appear anywhere in the payload.
func TestAudit_Create_EmitsFingerprintedFields(t *testing.T) {
	svc, fa, wsID, scopeID := newAuditedService(t)
	in := baseCreate(wsID, scopeID)
	in.PromptTemplate = "this is a secret prompt body that must never appear in the audit row"

	w, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rec := findRecord(t, fa.snapshot(), "worker_admin.create")
	if rec.ClientType != "worker_admin" {
		t.Errorf("ClientType = %q, want worker_admin", rec.ClientType)
	}
	if rec.Status != "ok" {
		t.Errorf("Status = %q, want ok", rec.Status)
	}
	if rec.SessionID != "worker:"+w.ID {
		t.Errorf("SessionID = %q, want worker:%s", rec.SessionID, w.ID)
	}
	if rec.ActorKind != "worker_admin" {
		t.Errorf("ActorKind = %q, want worker_admin", rec.ActorKind)
	}
	if rec.ActorID != w.ID {
		t.Errorf("ActorID = %q, want %s", rec.ActorID, w.ID)
	}
	payload := decodePayload(t, rec)
	if payload["worker_id"] != w.ID {
		t.Errorf("payload.worker_id = %v, want %s", payload["worker_id"], w.ID)
	}
	// Body must NEVER appear — only fingerprint shape.
	if strings.Contains(string(rec.ParamsRedacted), "secret prompt body") {
		t.Fatal("audit payload contains raw prompt body — must be fingerprinted")
	}
	pt, ok := payload["prompt_template"].(map[string]any)
	if !ok {
		t.Fatalf("prompt_template payload missing or wrong type: %v", payload["prompt_template"])
	}
	if pt["sha256"] == nil || pt["len"] == nil {
		t.Errorf("prompt_template fingerprint missing sha256/len: %v", pt)
	}
}

// TestAudit_Create_FingerprintsEndpointURL is the regression guard for
// the C3 fix: model_endpoint_url can carry secrets in the path/query
// for openai_compat workers ("https://api.example.com/v1?key=…"). The
// audit row MUST fingerprint it rather than write the URL verbatim,
// otherwise tokens land in audit_records.params_redacted in cleartext.
func TestAudit_Create_FingerprintsEndpointURL(t *testing.T) {
	svc, fa, wsID, scopeID := newAuditedService(t)
	in := baseCreate(wsID, scopeID)
	in.ModelProvider = "openai_compat"
	in.ModelEndpointURL = "https://api.example.com/v1?key=secret-token-123"

	w, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rec := findRecord(t, fa.snapshot(), "worker_admin.create")
	// The full token string must not survive ANYWHERE in the marshalled
	// record — params, error message, session id, anywhere a grep would
	// find it.
	if strings.Contains(string(rec.ParamsRedacted), "secret-token-123") {
		t.Fatalf("audit payload leaks endpoint URL token: %s", rec.ParamsRedacted)
	}
	payload := decodePayload(t, rec)
	endpoint, ok := payload["model_endpoint_url"].(map[string]any)
	if !ok {
		t.Fatalf("model_endpoint_url payload missing or wrong type: %v", payload["model_endpoint_url"])
	}
	if endpoint["sha256"] == nil || endpoint["len"] == nil {
		t.Errorf("model_endpoint_url fingerprint missing sha256/len: %v", endpoint)
	}
	_ = w
}

// TestAudit_Update_FingerprintsEndpointURL covers the diff path: when
// an operator rotates model_endpoint_url (e.g. swapping the embedded
// token), neither the old nor new URL should appear verbatim in the
// diff row.
func TestAudit_Update_FingerprintsEndpointURL(t *testing.T) {
	svc, fa, wsID, scopeID := newAuditedService(t)
	in := baseCreate(wsID, scopeID)
	in.ModelProvider = "openai_compat"
	in.ModelEndpointURL = "https://api.example.com/v1?key=old-token-abc"
	w, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	fa.records = nil // drop the create record; focus on update

	newURL := "https://api.example.com/v1?key=new-token-xyz"
	if _, err := svc.Update(context.Background(), admin.UpdateInput{
		ID:               w.ID,
		ModelEndpointURL: &newURL,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	rec := findRecord(t, fa.snapshot(), "worker_admin.update")
	body := string(rec.ParamsRedacted)
	if strings.Contains(body, "old-token-abc") {
		t.Fatalf("update audit row leaks OLD endpoint URL token: %s", body)
	}
	if strings.Contains(body, "new-token-xyz") {
		t.Fatalf("update audit row leaks NEW endpoint URL token: %s", body)
	}
	payload := decodePayload(t, rec)
	changes, ok := payload["changes"].(map[string]any)
	if !ok {
		t.Fatalf("payload.changes missing: %v", payload)
	}
	diff, ok := changes["model_endpoint_url"].(map[string]any)
	if !ok {
		t.Fatalf("model_endpoint_url diff missing or wrong type: %v", changes["model_endpoint_url"])
	}
	for _, side := range []string{"old", "new"} {
		fp, ok := diff[side].(map[string]any)
		if !ok {
			t.Errorf("diff[%s] missing or wrong type: %v", side, diff[side])
			continue
		}
		if fp["sha256"] == nil || fp["len"] == nil {
			t.Errorf("diff[%s] not a fingerprint: %v", side, fp)
		}
	}
}

// TestAudit_Update_EmitsOnlyChangedFields verifies the per-field diff:
// only the fields the UpdateInput actually mutated appear in the audit
// payload, and each carries {old, new}. Long fields render as
// fingerprint pairs; the worker_id is always set.
func TestAudit_Update_EmitsOnlyChangedFields(t *testing.T) {
	svc, fa, wsID, scopeID := newAuditedService(t)
	w, err := svc.Create(context.Background(), baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	fa.records = nil // drop the create record; focus on update

	newPrompt := "totally rewritten prompt that an attacker might inject here"
	newDesc := "now with extra context"
	if _, err := svc.Update(context.Background(), admin.UpdateInput{
		ID:             w.ID,
		Description:    &newDesc,
		PromptTemplate: &newPrompt,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	rec := findRecord(t, fa.snapshot(), "worker_admin.update")
	if rec.Status != "ok" {
		t.Errorf("Status = %q, want ok", rec.Status)
	}
	payload := decodePayload(t, rec)
	if payload["worker_id"] != w.ID {
		t.Errorf("worker_id = %v, want %s", payload["worker_id"], w.ID)
	}
	changes, ok := payload["changes"].(map[string]any)
	if !ok {
		t.Fatalf("payload.changes missing or wrong type: %v", payload["changes"])
	}
	if _, has := changes["description"]; !has {
		t.Errorf("description not in diff: %v", changes)
	}
	if _, has := changes["prompt_template"]; !has {
		t.Errorf("prompt_template not in diff: %v", changes)
	}
	// model_id was NOT touched — it must not appear in the diff.
	if _, has := changes["model_id"]; has {
		t.Errorf("unchanged model_id leaked into diff: %v", changes)
	}
	if strings.Contains(string(rec.ParamsRedacted), "attacker might inject") {
		t.Fatal("update payload leaked prompt body — must be fingerprinted")
	}
}

// TestAudit_Delete captures the worker name and emits ok.
func TestAudit_Delete(t *testing.T) {
	svc, fa, wsID, scopeID := newAuditedService(t)
	w, err := svc.Create(context.Background(), baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	fa.records = nil

	if err := svc.Delete(context.Background(), w.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rec := findRecord(t, fa.snapshot(), "worker_admin.delete")
	if rec.Status != "ok" {
		t.Errorf("Status = %q, want ok", rec.Status)
	}
	payload := decodePayload(t, rec)
	if payload["worker_id"] != w.ID {
		t.Errorf("worker_id = %v, want %s", payload["worker_id"], w.ID)
	}
	if payload["name"] != w.Name {
		t.Errorf("name = %v, want %s", payload["name"], w.Name)
	}
}

// TestAudit_PauseResume verifies each verb emits its OWN event name,
// not the generic set_enabled, and that previous_enabled tracks the
// pre-flip state.
func TestAudit_PauseResume(t *testing.T) {
	svc, fa, wsID, scopeID := newAuditedService(t)
	w, err := svc.Create(context.Background(), baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	fa.records = nil

	if _, err := svc.Pause(context.Background(), w.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	pauseRec := findRecord(t, fa.snapshot(), "worker_admin.pause")
	p := decodePayload(t, pauseRec)
	if p["enabled"] != false || p["previous_enabled"] != true {
		t.Errorf("pause payload = %v, want enabled=false previous=true", p)
	}

	if _, err := svc.Resume(context.Background(), w.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	resumeRec := findRecord(t, fa.snapshot(), "worker_admin.resume")
	r := decodePayload(t, resumeRec)
	if r["enabled"] != true || r["previous_enabled"] != false {
		t.Errorf("resume payload = %v, want enabled=true previous=false", r)
	}

	// SetEnabled (the generic verb) must produce worker_admin.set_enabled,
	// not pause / resume. Calling it with the same value is the
	// idempotent case — still audits.
	if _, err := svc.SetEnabled(context.Background(), w.ID, true); err != nil {
		t.Fatalf("set_enabled idempotent: %v", err)
	}
	findRecord(t, fa.snapshot(), "worker_admin.set_enabled")
}

// TestAudit_RunNow emits with the run_id from the runner (or "" when
// the runner errors). The Status reflects the outcome.
func TestAudit_RunNow(t *testing.T) {
	svc, fa, wsID, scopeID := newAuditedService(t)
	w, err := svc.Create(context.Background(), baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	fa.records = nil
	svc.SetRunnerForTest(&fakeRunner{runID: "run-from-fake"})

	out, err := svc.RunNow(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run_now: %v", err)
	}
	if out.RunID != "run-from-fake" {
		t.Errorf("run_id = %q, want run-from-fake", out.RunID)
	}
	rec := findRecord(t, fa.snapshot(), "worker_admin.run_now")
	if rec.Status != "ok" {
		t.Errorf("status = %q, want ok", rec.Status)
	}
	p := decodePayload(t, rec)
	if p["run_id"] != "run-from-fake" {
		t.Errorf("run_id in payload = %v", p["run_id"])
	}
	if p["worker_id"] != w.ID {
		t.Errorf("worker_id = %v, want %s", p["worker_id"], w.ID)
	}
}

// TestAudit_RunNow_RunnerError captures the failure path: when the
// runner returns an error, the audit row still lands with status=error
// AND the caller gets the underlying error.
func TestAudit_RunNow_RunnerError(t *testing.T) {
	svc, fa, wsID, scopeID := newAuditedService(t)
	w, err := svc.Create(context.Background(), baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	fa.records = nil
	svc.SetRunnerForTest(&fakeRunner{err: errors.New("downstream broke")})

	if _, err := svc.RunNow(context.Background(), w.ID); err == nil {
		t.Fatal("expected run_now error, got nil")
	}
	rec := findRecord(t, fa.snapshot(), "worker_admin.run_now")
	if rec.Status != "error" {
		t.Errorf("status = %q, want error", rec.Status)
	}
	if !strings.Contains(rec.ErrorMessage, "downstream broke") {
		t.Errorf("error_message = %q, want substring 'downstream broke'", rec.ErrorMessage)
	}
}

// TestAudit_NilAuditor_NoPanic verifies the nil-safe contract: a
// Service without an Auditor wired must not panic on CRUD calls.
func TestAudit_NilAuditor_NoPanic(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	// No SetAuditor call — auditor stays nil.
	w, err := svc.Create(context.Background(), baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create with nil auditor: %v", err)
	}
	if _, err := svc.Pause(context.Background(), w.ID); err != nil {
		t.Fatalf("pause with nil auditor: %v", err)
	}
	if err := svc.Delete(context.Background(), w.ID); err != nil {
		t.Fatalf("delete with nil auditor: %v", err)
	}
}

// TestAudit_RecordErrorDoesNotFailCRUD verifies that an Auditor.Record
// error never propagates back into the CRUD return value. The CRUD
// write already landed; audit is best-effort.
func TestAudit_RecordErrorDoesNotFailCRUD(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	fa := &fakeAuditor{err: errors.New("audit pipeline jammed")}
	svc.SetAuditor(fa)

	w, err := svc.Create(context.Background(), baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create must succeed even when auditor errors, got: %v", err)
	}
	if w.ID == "" {
		t.Fatal("created worker missing ID")
	}
	// And the broken auditor was still invoked.
	if len(fa.snapshot()) == 0 {
		t.Error("auditor was never called")
	}
}
