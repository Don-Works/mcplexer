// memory_arg_flex.go — flexible JSON unmarshalling for the memory tools'
// `tags` argument. Agents (and the loosely-typed tool schema) pass tags as
// EITHER a JSON array (["a","b"]) OR a single string ("a", or a
// comma-separated "a,b"). Before this, the args used []string and a string
// value errored with "cannot unmarshal string into []string" — a sharp
// papercut on a forgiving-looking field.
//
// flexStrings has underlying type []string, so a flexStrings value remains
// assignable to the []string fields on store.MemoryFilter /
// memory.WriteOptions with no conversion at the call sites.
package gateway

import (
	"bytes"
	"encoding/json"
	"strings"
)

// flexStrings accepts a JSON array of strings, a single JSON string, or a
// comma-separated JSON string, normalising all three to a []string.
type flexStrings []string

func (f *flexStrings) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = nil
		return nil
	}
	if b[0] == '[' {
		var arr []string
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*f = trimEach(arr)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	// Split a bare/comma-separated string so tags:"a,b" → ["a","b"].
	*f = trimEach(strings.Split(s, ","))
	return nil
}

// trimEach trims whitespace and drops empties so " a , b ,," → ["a","b"].
func trimEach(in []string) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
