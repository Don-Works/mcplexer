package downstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// secretRefPattern matches a string value that is *exactly* an opaque
// secret reference, e.g. "secret://stripe-prod". Mid-string interpolation
// is deliberately not supported — the whole argument value must be the
// reference. This keeps the substitution model predictable and avoids
// leaking plaintext into longer strings that downstream tools might
// echo back.
var secretRefPattern = regexp.MustCompile(`^secret://([A-Za-z0-9_.-]+)$`)

// secretRefMarker is the cheap byte-level prefilter. If the args blob
// doesn't contain this substring anywhere, we can skip the full
// unmarshal/walk/remarshal cycle entirely.
var secretRefMarker = []byte(`secret://`)

// ErrSecretRefUnknown is returned when args contain a reference whose
// key cannot be resolved against the call's auth scope.
var ErrSecretRefUnknown = errors.New("unknown secret reference")

// ErrSecretRefNoScope is returned when args contain a reference but the
// call has no auth scope to resolve against.
var ErrSecretRefNoScope = errors.New("secret reference used without auth scope")

// secretLookup resolves a reference key (the part after `secret://`) to
// its plaintext value. Implementations should emit a secret.read audit
// row as a side effect.
type secretLookup func(ctx context.Context, key string) ([]byte, error)

// substituteSecretRefs walks args, replacing any string value that
// exactly matches `secret://<key>` with the looked-up plaintext value.
// Non-matching strings, numbers, booleans, and nulls pass through
// unchanged.
//
// Number fidelity is preserved by decoding with UseNumber(); large
// integers won't be coerced to float64 and silently lose precision.
//
// Returns a new json.RawMessage; the input is never mutated. When no
// reference is present, the original slice is returned without copy.
func substituteSecretRefs(
	ctx context.Context, args json.RawMessage, lookup secretLookup,
) (json.RawMessage, error) {
	if len(args) == 0 || !bytes.Contains(args, secretRefMarker) {
		return args, nil
	}

	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("decode args: %w", err)
	}

	walked, err := walkAndSubstitute(ctx, root, lookup)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(walked)
	if err != nil {
		return nil, fmt.Errorf("re-marshal args after substitution: %w", err)
	}
	return out, nil
}

// walkAndSubstitute recursively descends the decoded JSON tree.
// Strings that exactly match the secret-ref pattern are resolved; all
// other leaves pass through. Objects and arrays are walked depth-first.
func walkAndSubstitute(
	ctx context.Context, node any, lookup secretLookup,
) (any, error) {
	switch v := node.(type) {
	case string:
		m := secretRefPattern.FindStringSubmatch(v)
		if m == nil {
			return v, nil
		}
		key := m[1]
		plaintext, err := lookup(ctx, key)
		if err != nil {
			// Join with ErrSecretRefUnknown so callers can match the
			// generic "ref couldn't resolve" sentinel, while preserving
			// the inner sentinel (e.g. ErrSecretRefNoScope, store.ErrNotFound)
			// for callers that want a more precise reason.
			return nil, errors.Join(
				ErrSecretRefUnknown,
				fmt.Errorf("secret ref %q: %w", key, err),
			)
		}
		return string(plaintext), nil

	case map[string]any:
		for k, child := range v {
			replaced, err := walkAndSubstitute(ctx, child, lookup)
			if err != nil {
				return nil, err
			}
			v[k] = replaced
		}
		return v, nil

	case []any:
		for i, child := range v {
			replaced, err := walkAndSubstitute(ctx, child, lookup)
			if err != nil {
				return nil, err
			}
			v[i] = replaced
		}
		return v, nil

	default:
		return v, nil
	}
}
