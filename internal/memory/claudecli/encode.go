// encode.go — small JSON marshal helpers used by the importer. Split
// from claudecli.go to stay under the 300-line cap.
package claudecli

import "encoding/json"

// mustJSONArray marshals tags into a JSON array. Failure is impossible
// for a []string in practice, so we fall back to "[]" rather than
// returning the error up the call stack.
func mustJSONArray(tags []string) json.RawMessage {
	if len(tags) == 0 {
		return json.RawMessage("[]")
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return json.RawMessage("[]")
	}
	return b
}

// mustJSONObject marshals a map. Same reasoning as mustJSONArray —
// imported metadata is plain string keys with primitive values.
func mustJSONObject(m map[string]any) json.RawMessage {
	if len(m) == 0 {
		return json.RawMessage("{}")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
