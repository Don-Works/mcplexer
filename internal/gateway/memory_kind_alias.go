// memory_kind_alias.go — server-side alias mapping for the memory__save
// `kind` argument.
//
// The store accepts exactly two kinds (store.MemoryKindFact /
// store.MemoryKindNote) and rejects anything else with a bare
// "WriteMemory: invalid kind %q". Audit-log analysis on 2026-07-18 found
// 17 of 200 memory__save calls dying on that check: agents reach for the
// vocabulary they think in — `decision` (x15), `preference`,
// `anti-pattern`, `project`, `project_fact` — not the storage primitive.
// Every one of those saves was lost.
//
// The mapping is driven by the ONE load-bearing difference between the two
// kinds, not by wording:
//
//   - fact = atomic key/value. Exactly one active row per
//     (workspace, worker, name); a re-save under the same name atomically
//     supersedes the prior row and stamps its t_valid_end, preserving the
//     bi-temporal trail. Use for anything with a single CURRENT value that
//     gets revised — a decision (ADR supersession is precisely this), a
//     preference ("preferred-editor" is the tool description's own example),
//     a project attribute.
//   - note = append-only markdown blob, no uniqueness. Many rows coexist
//     under one name. Use for narrative guidance where the whole set is the
//     value — lessons, anti-patterns, observations. Matches
//     internal/concierge/lessons.go, which writes lessons as notes, and
//     digest.partitionByKind, whose default arm is notes.
//
// Note is also the SAFE direction: mis-mapping to note leaves duplicates the
// contradiction scan surfaces advisorily, whereas mis-mapping to fact drops
// the earlier row out of default recall. Aliases only resolve to fact when
// they unambiguously denote a single-valued slot.
package gateway

import (
	"fmt"
	"sort"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// memoryKindAliases maps a normalised alias to the canonical store kind.
// Keys are normalised by normalizeMemoryKind (lowercase, separators
// stripped), so "anti-pattern", "anti_pattern" and "AntiPattern" all land
// on the "antipattern" key.
var memoryKindAliases = map[string]string{
	// → fact: single current value, revisions supersede.
	"fact":           store.MemoryKindFact,
	"facts":          store.MemoryKindFact,
	"decision":       store.MemoryKindFact,
	"decisions":      store.MemoryKindFact,
	"decisionrecord": store.MemoryKindFact,
	"adr":            store.MemoryKindFact,
	"project":        store.MemoryKindFact,
	"projects":       store.MemoryKindFact,
	"projectfact":    store.MemoryKindFact,
	"projectfacts":   store.MemoryKindFact,
	"preference":     store.MemoryKindFact,
	"preferences":    store.MemoryKindFact,
	"userpreference": store.MemoryKindFact,
	"setting":        store.MemoryKindFact,
	"settings":       store.MemoryKindFact,
	"config":         store.MemoryKindFact,
	"configuration":  store.MemoryKindFact,
	"keyvalue":       store.MemoryKindFact,
	"kv":             store.MemoryKindFact,
	"identity":       store.MemoryKindFact,
	"profile":        store.MemoryKindFact,

	// → note: append-only prose, many coexist.
	"note":         store.MemoryKindNote,
	"notes":        store.MemoryKindNote,
	"antipattern":  store.MemoryKindNote,
	"antipatterns": store.MemoryKindNote,
	"pattern":      store.MemoryKindNote,
	"lesson":       store.MemoryKindNote,
	"lessons":      store.MemoryKindNote,
	"insight":      store.MemoryKindNote,
	"observation":  store.MemoryKindNote,
	"gotcha":       store.MemoryKindNote,
	"tip":          store.MemoryKindNote,
	"guide":        store.MemoryKindNote,
	"howto":        store.MemoryKindNote,
	"memo":         store.MemoryKindNote,
	"summary":      store.MemoryKindNote,
	"context":      store.MemoryKindNote,
	"reference":    store.MemoryKindNote,
	"snippet":      store.MemoryKindNote,
	"markdown":     store.MemoryKindNote,
	"text":         store.MemoryKindNote,
	"blob":         store.MemoryKindNote,
}

// normalizeMemoryKind lowercases and strips `_`/`-`/whitespace so alias
// lookup is insensitive to the separator style the caller happened to pick.
func normalizeMemoryKind(kind string) string {
	return normalizeName(strings.Join(strings.Fields(kind), ""))
}

// resolveMemoryKind maps a caller-supplied kind onto a canonical store kind.
//
// An empty kind stays empty — the store defaults it to note, and forcing a
// value here would mask that default. A recognised alias resolves silently.
// Anything else returns a did-you-mean field error naming the valid set plus
// the closest alias, so the caller can self-correct in one retry instead of
// re-sending the same rejected verb.
func resolveMemoryKind(kind string) (string, *fieldArgError) {
	if strings.TrimSpace(kind) == "" {
		return "", nil
	}
	norm := normalizeMemoryKind(kind)
	if canonical, ok := memoryKindAliases[norm]; ok {
		return canonical, nil
	}
	return "", newFieldArgError(
		"invalid_enum_value", "kind", kind,
		fmt.Sprintf("%q is not a memory kind — valid kinds are %s, %s",
			kind, store.MemoryKindFact, store.MemoryKindNote),
		memoryKindHint(norm),
	)
}

// memoryKindHint builds the corrective hint: the nearest known alias when
// one is close enough to be a plausible typo, otherwise the semantic rule
// that lets the caller choose for themselves.
func memoryKindHint(norm string) string {
	const rule = "use kind=\"fact\" for a single current value that later saves " +
		"should supersede (decisions, preferences, settings), or kind=\"note\" " +
		"for append-only prose where every entry is kept (lessons, anti-patterns, " +
		"observations). Omit kind entirely to default to note."
	if alias, canonical, ok := nearestMemoryKindAlias(norm); ok {
		return fmt.Sprintf("did you mean %q (→ %s)? Otherwise %s",
			alias, canonical, rule)
	}
	return rule
}

// nearestMemoryKindAlias finds the closest alias within a small edit
// distance. Aliases are scanned in sorted order so the suggestion is
// deterministic when two are equidistant.
func nearestMemoryKindAlias(norm string) (alias, canonical string, ok bool) {
	threshold := 2
	if len(norm) > 8 {
		threshold = 3
	}
	keys := make([]string, 0, len(memoryKindAliases))
	for k := range memoryKindAliases {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	best := threshold + 1
	for _, k := range keys {
		if d := levenshtein(norm, k); d < best {
			best, alias, canonical, ok = d, k, memoryKindAliases[k], true
		}
	}
	return alias, canonical, ok
}
