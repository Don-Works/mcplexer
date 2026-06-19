package downstream

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeLookup returns plaintext from a static map. Unknown keys produce
// a generic error that substituteSecretRefs is expected to wrap with
// ErrSecretRefUnknown.
func fakeLookup(values map[string]string) secretLookup {
	return func(_ context.Context, key string) ([]byte, error) {
		v, ok := values[key]
		if !ok {
			return nil, errors.New("not found")
		}
		return []byte(v), nil
	}
}

func TestSubstituteSecretRefs_NoRefs_FastPath(t *testing.T) {
	in := json.RawMessage(`{"q":"hello","limit":10}`)
	called := false
	out, err := substituteSecretRefs(context.Background(), in, func(context.Context, string) ([]byte, error) {
		called = true
		return nil, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Fast path returns the original slice when the marker isn't present.
	if &out[0] != &in[0] {
		t.Errorf("expected fast-path passthrough of input slice")
	}
	if called {
		t.Errorf("lookup should not be invoked when no refs are present")
	}
}

func TestSubstituteSecretRefs_SingleRef(t *testing.T) {
	in := json.RawMessage(`{"api_key":"secret://stripe-prod","limit":10}`)
	out, err := substituteSecretRefs(
		context.Background(), in,
		fakeLookup(map[string]string{"stripe-prod": "stripe_secret_fixture"}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if got["api_key"] != "stripe_secret_fixture" {
		t.Errorf("got api_key=%v, want substituted plaintext", got["api_key"])
	}
	if _, ok := got["limit"]; !ok {
		t.Errorf("non-ref fields should pass through unchanged")
	}
}

func TestSubstituteSecretRefs_NestedAndArray(t *testing.T) {
	in := json.RawMessage(`{
		"headers": {"Authorization": "secret://bearer-token"},
		"body": {"tokens": ["secret://stripe-prod", "literal", "secret://github-pat"]},
		"depth": {"a": {"b": {"c": "secret://deep"}}}
	}`)
	out, err := substituteSecretRefs(
		context.Background(), in,
		fakeLookup(map[string]string{
			"bearer-token": "T1",
			"stripe-prod":  "T2",
			"github-pat":   "T3",
			"deep":         "T4",
		}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := string(out)
	for _, want := range []string{`"T1"`, `"T2"`, `"T3"`, `"T4"`, `"literal"`} {
		if !strings.Contains(s, want) {
			t.Errorf("expected %s in output, got: %s", want, s)
		}
	}
	if strings.Contains(s, "secret://") {
		t.Errorf("expected all refs substituted, got: %s", s)
	}
}

func TestSubstituteSecretRefs_MidStringNotInterpolated(t *testing.T) {
	// Substitution must be exact-string-match only. A value that *contains*
	// "secret://foo" as a substring must pass through unchanged — otherwise
	// agents could smuggle plaintext into longer strings.
	in := json.RawMessage(`{"q":"please look up secret://stripe-prod for me"}`)
	out, err := substituteSecretRefs(
		context.Background(), in,
		fakeLookup(map[string]string{"stripe-prod": "stripe_secret_fixture"}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(out), "secret://stripe-prod") {
		t.Errorf("mid-string ref should NOT be substituted, got: %s", out)
	}
	if strings.Contains(string(out), "stripe_secret_fixture") {
		t.Errorf("plaintext leaked through mid-string substitution: %s", out)
	}
}

func TestSubstituteSecretRefs_UnknownRefErrors(t *testing.T) {
	in := json.RawMessage(`{"api_key":"secret://does-not-exist"}`)
	_, err := substituteSecretRefs(
		context.Background(), in,
		fakeLookup(map[string]string{"stripe-prod": "stripe_secret_fixture"}),
	)
	if err == nil {
		t.Fatal("expected error for unknown reference")
	}
	if !errors.Is(err, ErrSecretRefUnknown) {
		t.Errorf("expected ErrSecretRefUnknown, got: %v", err)
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error should mention the unknown key, got: %v", err)
	}
}

func TestSubstituteSecretRefs_EmptyArgs(t *testing.T) {
	for _, raw := range []string{"", "null", "{}", "[]"} {
		t.Run(raw, func(t *testing.T) {
			out, err := substituteSecretRefs(
				context.Background(), json.RawMessage(raw),
				fakeLookup(nil),
			)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", raw, err)
			}
			_ = out
		})
	}
}

func TestSubstituteSecretRefs_PreservesNumberPrecision(t *testing.T) {
	// json.Unmarshal into interface{} normally coerces numbers to float64,
	// which silently truncates large integers. UseNumber() prevents this —
	// verify by round-tripping a value larger than 2^53.
	in := json.RawMessage(`{"id":9007199254740993,"key":"secret://k"}`)
	out, err := substituteSecretRefs(
		context.Background(), in,
		fakeLookup(map[string]string{"k": "v"}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(out), `9007199254740993`) {
		t.Errorf("large integer lost precision: %s", out)
	}
}

func TestSubstituteSecretRefs_RefShapeButInvalidKey(t *testing.T) {
	// Strings starting with `secret://` but containing characters outside
	// the allowed key character class are NOT references — they pass through
	// unchanged. (The marker prefilter triggers but the regex rejects.)
	in := json.RawMessage(`{"v":"secret://has spaces!"}`)
	out, err := substituteSecretRefs(
		context.Background(), in,
		fakeLookup(nil),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(out), "secret://has spaces!") {
		t.Errorf("ref-shaped invalid value should pass through, got: %s", out)
	}
}

func TestSubstituteSecretRefs_KeyAtTopLevel(t *testing.T) {
	// Tools whose schema accepts a bare string (not an object) should also
	// have their argument substituted when it's a ref.
	in := json.RawMessage(`"secret://just-a-token"`)
	out, err := substituteSecretRefs(
		context.Background(), in,
		fakeLookup(map[string]string{"just-a-token": "abc"}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != `"abc"` {
		t.Errorf("top-level ref should be substituted, got: %s", out)
	}
}
