package models

import (
	"reflect"
	"testing"
)

// grokModelsFixture is verbatim `grok models` output captured on the host.
const grokModelsFixture = `You are logged in with grok.com.

Default model: grok-4.5

Available models:
  * grok-4.5 (default)
`

func TestParseGrokModelsList(t *testing.T) {
	ids, auth := parseGrokModelsList([]byte(grokModelsFixture))
	if want := []string{"grok-4.5"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	if auth != ModelAuthOK {
		t.Fatalf("auth = %q, want %q", auth, ModelAuthOK)
	}
}

func TestParseGrokModelsListMultipleAndUnauth(t *testing.T) {
	raw := `You are not logged in. Run 'grok login'.

Available models:
  * grok-4.5 (default)
  * grok-4.5-fast
  - grok-code
`
	ids, auth := parseGrokModelsList([]byte(raw))
	want := []string{"grok-4.5", "grok-4.5-fast", "grok-code"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	if auth != ModelAuthUnauthenticated {
		t.Fatalf("auth = %q, want unauthenticated", auth)
	}
}

// mimoModelsFixture is verbatim `mimo models` output captured on the host.
const mimoModelsFixture = `mimo/mimo-auto
xiaomi/mimo-v2.5
xiaomi/mimo-v2.5-pro
xiaomi/mimo-v2.5-pro-ultraspeed
`

func TestParseMimoModelsList(t *testing.T) {
	ids := parseMimoModelsList([]byte(mimoModelsFixture))
	want := []string{
		"mimo/mimo-auto",
		"xiaomi/mimo-v2.5",
		"xiaomi/mimo-v2.5-pro",
		"xiaomi/mimo-v2.5-pro-ultraspeed",
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

func TestParseMimoModelsListSkipsProse(t *testing.T) {
	// Header/prose lines (containing spaces) must be dropped; only bare ids kept.
	raw := "Available models for provider xiaomi:\nxiaomi/mimo-v2.5\n\nDone.\n"
	ids := parseMimoModelsList([]byte(raw))
	if want := []string{"xiaomi/mimo-v2.5"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

// piModelsFixture mirrors ~/.pi/agent/models.json: the `name` is a local
// alias (qwen-local) distinct from the `id` (qwen3.6-35b-a3b); parsing must
// collect BOTH so a proven alias is never false-rejected by preflight.
const piModelsFixture = `{
  "providers": {
    "local": {
      "baseUrl": "http://127.0.0.1:1234/v1",
      "models": [
        {"id": "qwen3-coder-next", "name": "qwen-local-coder"},
        {"id": "qwen3.6-35b-a3b", "name": "qwen-local"}
      ]
    },
    "zai": {
      "models": [
        {"id": "glm-5.2", "name": ""}
      ]
    }
  }
}`

func TestParsePiModelsFileCollectsIDAndName(t *testing.T) {
	ids, err := parsePiModelsFile([]byte(piModelsFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	for _, want := range []string{"qwen3-coder-next", "qwen-local-coder", "qwen3.6-35b-a3b", "qwen-local", "glm-5.2"} {
		if !got[want] {
			t.Errorf("missing model id %q in %v", want, ids)
		}
	}
	// The empty name must not have produced a blank id.
	for _, id := range ids {
		if id == "" {
			t.Fatalf("blank id in output: %v", ids)
		}
	}
}

func TestParsePiModelsFileRejectsGarbage(t *testing.T) {
	if _, err := parsePiModelsFile([]byte("not json")); err == nil {
		t.Fatal("expected error on invalid JSON")
	}
}
