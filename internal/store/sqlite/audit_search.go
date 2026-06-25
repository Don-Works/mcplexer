// audit_search.go — ranked relevance search over the audit log.
//
// Strategy (local-first, no network):
//  1. Pull an FTS5 candidate pool scoped by the filter, capped to the most
//     recent auditSearchPoolCap rows so a huge table stays cheap.
//  2. Build a TF-IDF index (internal/embedding) over the candidate text
//     (tool_name + error_message + subpath + workspace_name + params) and
//     rank by cosine similarity to the query. mode "tfidf".
//  3. When the query has no usable FTS terms (sanitiseFTS5Query == ""), or
//     the TF-IDF arm finds nothing, fall back to recency order over the
//     scoped pool. mode "fts".
//
// The optional query-time embedding rerank ("vector" mode) is layered in
// the API handler — this store method never reaches the network.
package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/embedding"
	"github.com/don-works/mcplexer/internal/store"
)

// auditSearchPoolCap bounds the candidate pool the TF-IDF index is built
// over. 500 keeps the in-memory index small while covering enough recent
// history for relevance ranking to matter.
const auditSearchPoolCap = 500

func (d *DB) SearchAuditRecords(
	ctx context.Context, f store.AuditFilter, k int,
) ([]store.AuditRecord, string, error) {
	if k <= 0 {
		k = 50
	}

	// The candidate pool reuses QueryAuditRecords with the caller's filter
	// (scope: workspace/tool/status/q/etc.), forced to recency order and
	// the pool cap. q is folded into the WHERE by buildAuditWhere, so the
	// pool is already lexically pre-filtered when a usable query exists.
	pool := f
	pool.Sort = "time_desc"
	pool.Offset = 0
	pool.CursorTs = nil
	pool.CursorID = ""
	if pool.Limit <= 0 || pool.Limit > auditSearchPoolCap {
		pool.Limit = auditSearchPoolCap
	}

	candidates, _, err := d.QueryAuditRecords(ctx, pool)
	if err != nil {
		return nil, "", fmt.Errorf("audit search pool: %w", err)
	}

	expr := sanitizeFTS5Query(f.Q)
	if expr == "" {
		// No usable terms — recency fallback. Cap to k.
		if len(candidates) > k {
			candidates = candidates[:k]
		}
		return candidates, "fts", nil
	}

	// TF-IDF rerank over the candidate pool.
	docs := make([]embedding.Document, 0, len(candidates))
	byID := make(map[string]store.AuditRecord, len(candidates))
	for _, r := range candidates {
		byID[r.ID] = r
		docs = append(docs, embedding.Document{ID: r.ID, Text: auditSearchText(r)})
	}
	idx := embedding.NewIndex(docs)
	hits := idx.Search(f.Q, k)
	if len(hits) == 0 {
		// Query had terms but matched nothing in the TF-IDF space — fall
		// back to the (already FTS-filtered) recency pool so the caller
		// still gets the lexical matches.
		if len(candidates) > k {
			candidates = candidates[:k]
		}
		return candidates, "fts", nil
	}

	ranked := make([]store.AuditRecord, 0, len(hits))
	for _, h := range hits {
		if r, ok := byID[h.ID]; ok {
			ranked = append(ranked, r)
		}
	}
	return ranked, "tfidf", nil
}

// auditSearchText composes the searchable text for one audit record. Kept
// in sync with the FTS index columns (migration 116) so the TF-IDF arm
// ranks on the same surface the lexical pre-filter narrowed.
func auditSearchText(r store.AuditRecord) string {
	var b strings.Builder
	b.WriteString(r.ToolName)
	b.WriteByte(' ')
	b.WriteString(r.ErrorMessage)
	b.WriteByte(' ')
	b.WriteString(r.Subpath)
	b.WriteByte(' ')
	b.WriteString(r.WorkspaceName)
	b.WriteByte(' ')
	b.WriteString(string(r.ParamsRedacted))
	if r.DenialReason != "" {
		b.WriteByte(' ')
		b.WriteString(r.DenialReason)
	}
	return b.String()
}
