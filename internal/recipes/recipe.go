package recipes

import (
	"context"
	"encoding/json"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// Rank computes a composite relevance score for a harvested recipe.
//
// Factors (all normalised to [0..1] then weighted):
//   - success_rate_weight (0.35): higher success rate = more reliable
//   - frequency_weight    (0.25): more total calls = more practiced
//   - recency_weight      (0.25): recently used = still relevant
//   - diversity_weight    (0.15): more unique sessions = broader utility
//
// The returned score is in [0..1].
func Rank(r *store.Recipe, now time.Time) float64 {
	if r.TotalCount == 0 {
		return 0
	}

	successRate := 1.0 - r.ErrorRate
	if successRate < 0.5 {
		successRate = 0.5
	}

	freqNorm := float64(r.TotalCount) / 1000.0
	if freqNorm > 1.0 {
		freqNorm = 1.0
	}

	var recencyNorm float64
	if r.LastUsedAt != nil && !r.LastUsedAt.IsZero() {
		hoursSince := now.Sub(*r.LastUsedAt).Hours()
		recencyNorm = exp2(-hoursSince / (30 * 24))
	}

	diversityNorm := float64(r.SessionCount) / 20.0
	if diversityNorm > 1.0 {
		diversityNorm = 1.0
	}

	const (
		successRateWeight = 0.35
		freqWeight        = 0.25
		recencyWeight     = 0.25
		diversityWeight   = 0.15
	)

	return successRateWeight*successRate +
		freqWeight*freqNorm +
		recencyWeight*recencyNorm +
		diversityWeight*diversityNorm
}

func exp2(x float64) float64 {
	result := 1.0
	neg := x < 0
	if neg {
		x = -x
	}
	intPart := int(x)
	for i := 0; i < intPart; i++ {
		result *= 2
	}
	frac := x - float64(intPart)
	result *= 1 + frac
	if neg {
		return 1.0 / result
	}
	return result
}

// ParamKeys describes the parameter structure of a tool call.
type ParamKeys struct {
	Keys     []string `json:"keys"`
	Optional []string `json:"optional,omitempty"`
}

// AuditCall is the subset of audit record fields the harvester needs.
type AuditCall struct {
	ID        string
	ToolName  string
	Status    string
	LatencyMs int
	SessionID string
	Timestamp time.Time
	Params    json.RawMessage
}

// AuditQuerier abstracts the query surface the harvester needs from the
// audit store.
type AuditQuerier interface {
	QueryRecentToolCalls(ctx context.Context, since time.Time, limit int) ([]AuditCall, error)
	QueryToolCallsByName(ctx context.Context, toolName string, since time.Time, limit int) ([]AuditCall, error)
}

// QueryAdapter wraps a store.AuditStore into the AuditQuerier interface.
type QueryAdapter struct {
	Store interface {
		QueryAuditRecords(ctx context.Context, f store.AuditFilter) ([]store.AuditRecord, int, error)
	}
}

func (a *QueryAdapter) QueryRecentToolCalls(ctx context.Context, since time.Time, limit int) ([]AuditCall, error) {
	s := since
	f := store.AuditFilter{After: &s, Limit: limit}
	records, _, err := a.Store.QueryAuditRecords(ctx, f)
	if err != nil {
		return nil, err
	}
	return toAuditCalls(records), nil
}

func (a *QueryAdapter) QueryToolCallsByName(ctx context.Context, toolName string, since time.Time, limit int) ([]AuditCall, error) {
	s := since
	f := store.AuditFilter{
		ToolName: &toolName,
		After:    &s,
		Limit:    limit,
	}
	records, _, err := a.Store.QueryAuditRecords(ctx, f)
	if err != nil {
		return nil, err
	}
	return toAuditCalls(records), nil
}

func toAuditCalls(records []store.AuditRecord) []AuditCall {
	out := make([]AuditCall, len(records))
	for i, r := range records {
		out[i] = AuditCall{
			ID:        r.ID,
			ToolName:  r.ToolName,
			Status:    r.Status,
			LatencyMs: r.LatencyMs,
			SessionID: r.SessionID,
			Timestamp: r.Timestamp,
			Params:    r.ParamsRedacted,
		}
	}
	return out
}
