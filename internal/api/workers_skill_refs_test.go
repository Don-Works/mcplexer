// workers_skill_refs_test.go (M0.7) — HTTP round-trip for the multi-
// skill array. Confirms a POST /workers with `skill_refs` lands back
// unchanged on GET and that legacy single-skill payloads still work.
package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestWorkersHandler_SkillRefsRoundTrip(t *testing.T) {
	srv, _, wsID, scopeID := newWorkersTestServer(t)

	create := map[string]any{
		"name":            "multi-skill",
		"model_provider":  "anthropic",
		"model_id":        "claude-opus-4-7",
		"secret_scope_id": scopeID,
		"prompt_template": "go",
		"schedule_spec":   "0 * * * *",
		"workspace_id":    wsID,
		"skill_refs": []map[string]string{
			{"name": "a", "version": ""},
			{"name": "b", "version": "1"},
		},
	}
	created := postJSON(t, srv.URL+"/api/v1/workers", create, http.StatusCreated)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("missing id: %+v", created)
	}

	gotRefs := refsFromMap(t, created)
	if len(gotRefs) != 2 || gotRefs[0]["name"] != "a" || gotRefs[1]["name"] != "b" {
		t.Fatalf("create skill_refs mismatch: %+v", gotRefs)
	}

	// GET round-trip should preserve the slice and the legacy mirror.
	resp, err := http.Get(srv.URL + "/api/v1/workers/" + id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d", resp.StatusCode)
	}
	var body struct {
		Worker map[string]any `json:"worker"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	refs := refsFromMap(t, body.Worker)
	if len(refs) != 2 || refs[0]["name"] != "a" || refs[1]["name"] != "b" {
		t.Fatalf("get skill_refs mismatch: %+v", refs)
	}
	if body.Worker["skill_name"] != "a" {
		t.Fatalf("legacy skill_name mirror = %v, want a", body.Worker["skill_name"])
	}
}

func TestWorkersHandler_LegacySkillNameStillWorks(t *testing.T) {
	srv, _, wsID, scopeID := newWorkersTestServer(t)

	create := map[string]any{
		"name":            "legacy-skill",
		"model_provider":  "anthropic",
		"model_id":        "claude-opus-4-7",
		"secret_scope_id": scopeID,
		"prompt_template": "go",
		"schedule_spec":   "0 * * * *",
		"workspace_id":    wsID,
		"skill_name":      "lead-responder",
		"skill_version":   "2",
	}
	created := postJSON(t, srv.URL+"/api/v1/workers", create, http.StatusCreated)
	refs := refsFromMap(t, created)
	if len(refs) != 1 || refs[0]["name"] != "lead-responder" {
		t.Fatalf("expected synthesized single ref, got %+v", refs)
	}
}

// refsFromMap pulls the skill_refs array out of a JSON-decoded worker
// payload. Returns []map[string]any so tests can poke at name/version.
func refsFromMap(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	raw, ok := m["skill_refs"].([]any)
	if !ok || raw == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		entry, ok := r.(map[string]any)
		if !ok {
			t.Fatalf("skill_refs entry not an object: %+v", r)
		}
		out = append(out, entry)
	}
	return out
}
