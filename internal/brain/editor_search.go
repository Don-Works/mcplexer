package brain

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// scaleCliff is the index size past which the search drops the fuzzy tier so
// the picker stays sub-frame (DESIGN §4.1 "~10k scale-cliff fallback"). Below
// it all three tiers run; at or above it only exact-prefix + token run.
const scaleCliff = 10000

// searchTierExact / Token / Fuzzy rank the three match qualities. Lower is
// better (exact-prefix beats token beats fuzzy) and the frecency boost is
// applied within a tier, never across tiers.
const (
	searchTierExact = 0
	searchTierToken = 1
	searchTierFuzzy = 2
)

// SearchHit is one intellisense result. It is the shared shape behind cmd+K
// and every in-field [[ref]]/#tag/@workspace typeahead — one engine, one
// ranking, one dropdown (DESIGN §4.0).
type SearchHit struct {
	Kind      string   `json:"kind"` // task|memory
	ID        string   `json:"id"`
	Title     string   `json:"title"` // task title / memory name
	Status    string   `json:"status,omitempty"`
	Workspace string   `json:"workspace,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Score     float64  `json:"score"` // composite frecency rank; higher = better
	Tier      int      `json:"tier"`  // 0 exact-prefix, 1 token, 2 fuzzy
}

// SearchResult wraps the ranked hits with the engine decisions the GUI shows
// (whether the fuzzy tier was dropped at scale, and the create-on-miss seed).
type SearchResult struct {
	Hits        []SearchHit `json:"hits"`
	FuzzyOff    bool        `json:"fuzzy_off"`    // scale-cliff dropped the fuzzy tier
	CreateLabel string      `json:"create_label"` // typed text, echoed for the "+ Create" row
}

// Search runs the three-tier frecency intellisense over the FTS5 index for a
// workspace's scope. q is the raw typed text (NOT an FTS5 expression — this
// builds the MATCH expression). kind narrows to task|memory|"" (both). limit
// caps the merged result set.
//
// Tiering: tier 0 (exact-prefix) ranks rows whose title/name starts with the
// query; tier 1 (token) ranks the FTS5 prefix-match remainder; tier 2 (fuzzy)
// is a substring scan over the broader candidate set, dropped past the
// scale-cliff. Within a tier, frecency (recency + a light frequency proxy)
// orders the rows so a recently-touched record floats up.
func (e *Editor) Search(ctx context.Context, q, kind, workspace string, limit int) (*SearchResult, error) {
	q = strings.TrimSpace(q)
	if limit <= 0 {
		limit = 20
	}
	res := &SearchResult{Hits: []SearchHit{}, CreateLabel: q}
	if q == "" {
		return res, nil
	}

	total, err := e.indexSize(ctx)
	if err != nil {
		return nil, err
	}
	res.FuzzyOff = total >= scaleCliff

	now := time.Now()
	seen := make(map[string]struct{})
	var hits []SearchHit

	if kind == "" || kind == EntityKindTask {
		ts, terr := e.searchTasks(ctx, q, workspace, res.FuzzyOff)
		if terr != nil {
			return nil, terr
		}
		for i := range ts {
			t := &ts[i]
			h := tier(strings.ToLower(t.Title), q, res.FuzzyOff)
			if h < 0 {
				continue
			}
			key := EntityKindTask + "\x00" + t.ID
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			tags, _ := decodeStringSlice(t.TagsJSON)
			hits = append(hits, SearchHit{
				Kind: EntityKindTask, ID: t.ID, Title: t.Title, Status: t.Status,
				Workspace: t.WorkspaceID, Tags: tags, Tier: h,
				Score: frecency(h, t.UpdatedAt, now),
			})
		}
	}
	if kind == "" || kind == EntityKindMemory {
		ms, merr := e.searchMemories(ctx, q, workspace, res.FuzzyOff)
		if merr != nil {
			return nil, merr
		}
		for i := range ms {
			m := &ms[i]
			h := tier(strings.ToLower(m.Name), q, res.FuzzyOff)
			if h < 0 {
				continue
			}
			key := EntityKindMemory + "\x00" + m.ID
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			tags, _ := decodeStringSlice(m.TagsJSON)
			ws := ""
			if m.WorkspaceID != nil {
				ws = *m.WorkspaceID
			}
			hits = append(hits, SearchHit{
				Kind: EntityKindMemory, ID: m.ID, Title: m.Name, Status: m.Kind,
				Workspace: ws, Tags: tags, Tier: h,
				Score: frecency(h, m.UpdatedAt, now),
			})
		}
	}

	// Stable rank: tier asc, then score desc (frecency), then id for ties.
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Tier != hits[j].Tier {
			return hits[i].Tier < hits[j].Tier
		}
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID < hits[j].ID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	res.Hits = hits
	return res, nil
}

// searchTasks gathers the task candidate set for the in-Go three-tier
// ranking. Below the scale cliff it lists the workspace's rows (capped) so the
// tiering sees a full candidate pool — partial-word prefix/token/fuzzy needs
// every candidate, not just FTS full-word hits. At scale it switches to the
// store's FTS5 MATCH (passing the raw words, which the store escapes) so the
// candidate set stays bounded by the index, not the full table.
func (e *Editor) searchTasks(ctx context.Context, q, workspace string, atScale bool) ([]store.Task, error) {
	f := store.TaskFilter{WorkspaceID: strings.TrimSpace(workspace), Limit: 200}
	if !atScale {
		return e.store.ListTasks(ctx, f)
	}
	rows, err := e.store.SearchTasks(ctx, f, ftsWords(q))
	if err != nil {
		// FTS5 syntax errors on exotic input degrade to a plain list scan so
		// the typeahead never hard-fails mid-keystroke.
		return e.store.ListTasks(ctx, f)
	}
	return rows, nil
}

// searchMemories gathers the memory candidate set (see searchTasks).
func (e *Editor) searchMemories(ctx context.Context, q, workspace string, atScale bool) ([]store.MemoryEntry, error) {
	f := store.MemoryFilter{Limit: 200}
	if ws := strings.TrimSpace(workspace); ws != "" && ws != "global" {
		f.Scope = store.SkillScope{WorkspaceIDs: []string{ws}}
	}
	if !atScale {
		return e.store.ListMemories(ctx, f)
	}
	hits, err := e.store.SearchMemories(ctx, f, ftsWords(q))
	if err != nil {
		rows, lerr := e.store.ListMemories(ctx, f)
		return rows, lerr
	}
	out := make([]store.MemoryEntry, 0, len(hits))
	for i := range hits {
		out = append(out, hits[i].Entry)
	}
	return out, nil
}

// indexSize returns a cheap proxy for the total indexed-record count (the
// scale-cliff gate). It counts index_files rows, which is one-per-record.
func (e *Editor) indexSize(ctx context.Context) (int, error) {
	files, err := e.store.ListIndexFiles(ctx, "")
	if err != nil {
		return 0, err
	}
	return len(files), nil
}

// ftsWords normalises raw typed text into the space-separated bare words the
// store's SearchTasks/SearchMemories expect (the store applies its own FTS5
// quoting/escaping — callers pass words, not a MATCH expression). The text is
// split on any non-alphanumeric run so "re-arm" becomes the two indexed terms
// "re" + "arm". An all-symbol query yields "" (the store treats it as a list).
func ftsWords(q string) string {
	return strings.Join(splitAlnum(q), " ")
}

// splitAlnum splits s on any non-alphanumeric run, lowercasing the result.
func splitAlnum(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

// tier classifies a candidate title against the lowercased query: exact
// prefix (tier 0), token boundary match (tier 1), substring/fuzzy (tier 2,
// suppressed when fuzzyOff). Returns -1 for no match.
func tier(title, query string, fuzzyOff bool) int {
	ql := strings.ToLower(strings.TrimSpace(query))
	if ql == "" {
		return -1
	}
	if strings.HasPrefix(title, ql) {
		return searchTierExact
	}
	// Token-boundary: any whitespace-delimited token in the title prefixes ql.
	for _, tok := range strings.Fields(title) {
		if strings.HasPrefix(tok, ql) {
			return searchTierToken
		}
	}
	if fuzzyOff {
		return -1
	}
	if strings.Contains(title, ql) {
		return searchTierFuzzy
	}
	return -1
}

// frecency computes the intra-tier rank boost: a base by tier plus a recency
// decay over updated_at. Recently-touched records float up within their tier
// (DESIGN §4.1 — frecency = recently + frequently touched rank up). Frequency
// is proxied by recency here (the index has no per-record recall counter to
// read cheaply); the recency curve is the dominant, honest signal.
func frecency(t int, updated, now time.Time) float64 {
	base := float64(searchTierFuzzy-t) * 100 // exact tier gets the largest base
	if updated.IsZero() {
		return base
	}
	ageHours := now.Sub(updated).Hours()
	if ageHours < 0 {
		ageHours = 0
	}
	// Half-life ~7 days: a record touched today scores ~50 recency points, one
	// from a week ago ~25, decaying smoothly. Bounded so it never outranks a
	// better tier.
	recency := 50.0 / (1.0 + ageHours/168.0)
	return base + recency
}
