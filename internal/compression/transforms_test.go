package compression

import (
	"encoding/json"
	"reflect"
	"testing"
)

// textOf extracts content[0].text from a tool-result envelope for assertions.
func textOf(t *testing.T, env json.RawMessage) string {
	t.Helper()
	var e struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(env, &e); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if len(e.Content) == 0 {
		t.Fatalf("no content in envelope: %s", env)
	}
	return e.Content[0].Text
}

func TestJSONMinifyShrinksAndPreservesValue(t *testing.T) {
	pretty := "{\n  \"id\": 1,\n  \"name\": \"acme\",\n  \"nested\": [\n    1,\n    2,\n    3\n  ]\n}"
	prettyJSON, _ := json.Marshal(pretty)
	env := json.RawMessage(`{"content":[{"type":"text","text":` + string(prettyJSON) + `}]}`)

	out, changed := jsonMinify{}.Apply(env)
	if !changed {
		t.Fatal("expected minify to change a pretty-printed JSON payload")
	}
	if len(out) >= len(env) {
		t.Fatalf("expected smaller output: %d >= %d", len(out), len(env))
	}
	// Value equivalence: the minified text must parse to the same JSON value.
	var before, after any
	if err := json.Unmarshal([]byte(pretty), &before); err != nil {
		t.Fatalf("pretty not valid JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(textOf(t, out)), &after); err != nil {
		t.Fatalf("minified not valid JSON: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("minify altered the JSON value:\n before=%#v\n after =%#v", before, after)
	}
}

func TestJSONMinifyNoopOnCompactJSON(t *testing.T) {
	env := json.RawMessage(`{"content":[{"type":"text","text":"{\"a\":1,\"b\":2}"}]}`)
	out, changed := jsonMinify{}.Apply(env)
	if changed {
		t.Fatalf("compact JSON should be a no-op, got: %s", out)
	}
}

func TestJSONMinifyNoopOnPlainText(t *testing.T) {
	env := json.RawMessage(`{"content":[{"type":"text","text":"just some log output, not JSON"}]}`)
	out, changed := jsonMinify{}.Apply(env)
	if changed {
		t.Fatalf("plain text should be a no-op, got: %s", out)
	}
}

func TestJSONMinifyNoopOnMalformedEnvelope(t *testing.T) {
	for _, in := range []string{
		`not json at all`,
		`{"isError":true}`,
		`{"content":"not an array"}`,
	} {
		out, changed := jsonMinify{}.Apply(json.RawMessage(in))
		if changed || string(out) != in {
			t.Fatalf("malformed envelope %q must be returned unchanged, got changed=%v out=%s", in, changed, out)
		}
	}
}
