package sqlite

import (
	"encoding/json"
	"slices"

	"github.com/don-works/mcplexer/internal/store"
)

func countDataItems(items []store.DataItem, defaultKind string) (rows, docs int) {
	for _, it := range items {
		kind := it.Kind
		if kind == "" {
			kind = defaultKind
		}
		if kind == store.DataWorkbenchKindDocs {
			docs++
		} else {
			rows++
		}
	}
	return rows, docs
}

func tagsMatch(raw json.RawMessage, want []string) bool {
	if len(want) == 0 {
		return true
	}
	var got []string
	if err := json.Unmarshal(raw, &got); err != nil {
		return false
	}
	for _, tag := range want {
		if !slices.Contains(got, tag) {
			return false
		}
	}
	return true
}
