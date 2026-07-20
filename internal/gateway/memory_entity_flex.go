// memory_entity_flex.go — flexible JSON unmarshalling for the memory tools'
// `entities` / `entities_any` arguments.
//
// The documented shape is an array of {kind, id, role?} objects. Audit-log
// analysis on 2026-07-18 found 6 memory__save calls dying inside
// encoding/json before the handler ever ran ("cannot unmarshal string into
// Go struct field .entities of type gateway.entityArg"), because agents send
// the shapes that read naturally to them:
//
//	entities: ["task:01ABC", "person:a@example.com"]   // bare string array
//	entities: {"kind":"task","id":"01ABC"}             // single object
//	entities: "task:01ABC"                             // single string
//
// All three are unambiguous and are now accepted. A `kind:id` string splits
// on the FIRST colon so ids that themselves contain colons survive
// ("artifact:gh:owner/repo#1" → kind=artifact, id="gh:owner/repo#1").
//
// The one shape deliberately NOT guessed at is a bare string with no
// recognised kind prefix ("acme"): the kind decides whether recall can
// escape the workspace filter (store.EntityRecallCanEscapeScope), so
// inventing one would silently change scope semantics. That case returns a
// field error naming the reserved kinds instead.
package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// reservedEntityKinds is the closed set the memory tools document. Mirrors
// the `entities` schema description in builtin_tools_memory.go; a bare
// string only parses as `kind:id` when its prefix is one of these.
var reservedEntityKinds = map[string]bool{
	"task": true, "person": true, "place": true, "peer": true,
	"agent": true, "org": true, "skill": true, "artifact": true,
	"event": true, "workspace": true,
}

// reservedEntityKindList renders the reserved set for error hints in a
// stable, documentation order (not sorted — this is the order the tool
// description uses).
const reservedEntityKindList = "task, person, place, peer, agent, org, " +
	"skill, artifact, event, workspace"

// flexEntities accepts an array of entity objects, an array of `kind:id`
// strings, a single entity object, or a single `kind:id` string. Its
// underlying type is []entityArg so values stay assignable wherever the
// handlers already pass []entityArg.
type flexEntities []entityArg

// UnmarshalJSON never errors on an unresolvable string element — it stores
// the raw text as ID with an empty Kind and lets validateEntities report it.
// Returning an error here would be flattened into an opaque -32602 by the
// handler's json.Unmarshal, which is the papercut this file exists to fix.
func (f *flexEntities) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = nil
		return nil
	}
	switch b[0] {
	case '[':
		return f.unmarshalArray(b)
	case '{':
		var one entityArg
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		*f = flexEntities{one}
		return nil
	default:
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexEntities{parseEntityString(s)}
		return nil
	}
}

// unmarshalArray decodes a heterogeneous array: each element may be an
// object or a `kind:id` string. Decoding element-by-element (rather than
// into []entityArg) is what keeps a single string element from failing the
// whole call.
func (f *flexEntities) unmarshalArray(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	out := make(flexEntities, 0, len(raw))
	for _, el := range raw {
		el = bytes.TrimSpace(el)
		if len(el) == 0 || string(el) == "null" {
			continue
		}
		if el[0] == '{' {
			var one entityArg
			if err := json.Unmarshal(el, &one); err != nil {
				return err
			}
			out = append(out, one)
			continue
		}
		var s string
		if err := json.Unmarshal(el, &s); err != nil {
			return err
		}
		out = append(out, parseEntityString(s))
	}
	*f = out
	return nil
}

// parseEntityString splits "kind:id" on the first colon when the prefix is a
// reserved kind. Anything else is returned with an empty Kind and the raw
// text as ID, which validateEntities turns into an actionable error.
func parseEntityString(s string) entityArg {
	s = strings.TrimSpace(s)
	if kind, id, found := strings.Cut(s, ":"); found {
		k := strings.ToLower(strings.TrimSpace(kind))
		if reservedEntityKinds[k] && strings.TrimSpace(id) != "" {
			return entityArg{Kind: k, ID: strings.TrimSpace(id)}
		}
	}
	return entityArg{ID: s}
}

// validateEntities reports the entries that carry an id but no kind — the
// ambiguous bare-string case. Returns nil when every entry is usable.
//
// Entries missing BOTH fields are left to toEntityRefs, which drops them
// silently; an entry the caller clearly meant as a link deserves a loud
// error rather than a silent drop.
func validateEntities(field string, ents []entityArg) *fieldArgError {
	bad := make([]string, 0, len(ents))
	for _, e := range ents {
		if strings.TrimSpace(e.Kind) == "" && strings.TrimSpace(e.ID) != "" {
			bad = append(bad, e.ID)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return newFieldArgError(
		"invalid_argument_shape", field, strings.Join(bad, ", "),
		fmt.Sprintf("entity reference(s) %q have no kind",
			strings.Join(bad, ", ")),
		fmt.Sprintf("pass %s as objects {\"kind\":\"...\",\"id\":\"...\"} or as "+
			"\"kind:id\" strings — kind must be one of: %s",
			field, reservedEntityKindList),
	)
}
