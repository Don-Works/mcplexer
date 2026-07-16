package distill

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const templateEvidenceLineLimit = 5000

type templateEvidenceStore interface {
	GetLogTemplateHistory(ctx context.Context, templateID string) (*store.LogTemplateHistory, error)
	ListLogLinesForTemplateEvidence(ctx context.Context, templateID string, limit int) ([]*store.LogLine, error)
}

type templateEvidence struct {
	history         store.LogTemplateHistory
	correlationKey  string
	cardinality     string
	samples         []string
	cardinalityRows int
}

var fileLineRe = regexp.MustCompile(`(?:^|[\s"'(])([A-Za-z0-9_./-]+\.(?:go|rs|py|php|js|jsx|ts|tsx|java|cs|rb|kt|swift|c|cc|cpp|h|hpp):\d+)\b`)

func (q *Query) templateEvidence(
	ctx context.Context, t *store.LogTemplate, src *store.LogSource,
) templateEvidence {
	evidence := templateEvidence{}
	evidence.correlationKey = correlationKey(src, t.SampleLast)
	reader, ok := q.store.(templateEvidenceStore)
	if !ok {
		evidence.samples = nonEmptySamples(t.SampleLast, t.SampleFirst)
		return evidence
	}
	if history, err := reader.GetLogTemplateHistory(ctx, t.ID); err == nil && history != nil {
		evidence.history = *history
	}
	lines, err := reader.ListLogLinesForTemplateEvidence(ctx, t.ID, templateEvidenceLineLimit)
	if err != nil {
		evidence.samples = nonEmptySamples(t.SampleLast, t.SampleFirst)
		return evidence
	}
	evidence.cardinalityRows = len(lines)
	evidence.cardinality = maskedCardinality(t.Masked, lines)
	for _, line := range lines {
		evidence.samples = appendDistinct(evidence.samples, line.Line, 3)
	}
	if len(evidence.samples) == 0 {
		evidence.samples = nonEmptySamples(t.SampleLast, t.SampleFirst)
	}
	return evidence
}

func correlationKey(src *store.LogSource, sample string) string {
	source := "unknown-source"
	if src != nil && src.Name != "" {
		source = src.Name
	}
	if strings.HasPrefix(strings.ToLower(sample), "logwatch: published port exposure") ||
		strings.HasPrefix(strings.ToLower(sample), "logwatch: port exposure check") {
		if src != nil && src.RemoteHostID != "" {
			return "host:" + src.RemoteHostID + "|docker-port-exposure"
		}
		return source + "|docker-port-exposure"
	}
	match := fileLineRe.FindStringSubmatch(sample)
	if match == nil {
		return ""
	}
	return source + "|" + match[1]
}

func maskedCardinality(masked string, lines []*store.LogLine) string {
	values := map[string]map[string]struct{}{}
	for _, line := range lines {
		normalized, found := NormalizeWithValues(line.Line)
		if normalized != masked {
			continue
		}
		for _, value := range found {
			if values[value.Field] == nil {
				values[value.Field] = map[string]struct{}{}
			}
			values[value.Field][value.Value] = struct{}{}
		}
	}
	fields := make([]string, 0, len(values))
	for field := range values {
		fields = append(fields, field)
	}
	sort.Slice(fields, func(i, j int) bool {
		return evidenceFieldRank(fields[i]) < evidenceFieldRank(fields[j]) ||
			(evidenceFieldRank(fields[i]) == evidenceFieldRank(fields[j]) && fields[i] < fields[j])
	})
	parts := make([]string, 0, min(len(fields), 6))
	for _, field := range fields {
		set := values[field]
		part := fmt.Sprintf("%s=%d distinct", field, len(set))
		if len(set) <= 4 && inlineMaskedValues(field, set) {
			items := make([]string, 0, len(set))
			for value := range set {
				items = append(items, truncate(value, 48))
			}
			sort.Strings(items)
			part += " [" + strings.Join(items, ", ") + "]"
		}
		parts = append(parts, part)
		if len(parts) == 6 {
			break
		}
	}
	return strings.Join(parts, "; ")
}

func evidenceFieldRank(field string) int {
	switch field {
	case "integer", "identifier", "timestamp", "duration", "uuid", "hex", "ip", "quoted":
		return 1
	default:
		return 0
	}
}

func inlineMaskedValues(field string, values map[string]struct{}) bool {
	switch field {
	case "timestamp", "uuid", "hex", "ip", "quoted":
		return false
	}
	lower := strings.ToLower(field)
	for _, fragment := range []string{"token", "secret", "password", "passwd", "authorization", "session", "request_id", "trace_id", "uuid"} {
		if strings.Contains(lower, fragment) {
			return false
		}
	}
	for value := range values {
		if unsafeInlineValueRe.MatchString(value) {
			return false
		}
	}
	return true
}

func appendDistinct(samples []string, sample string, limit int) []string {
	if strings.TrimSpace(sample) == "" || len(samples) >= limit {
		return samples
	}
	for _, existing := range samples {
		if existing == sample {
			return samples
		}
	}
	return append(samples, sample)
}

func nonEmptySamples(candidates ...string) []string {
	var samples []string
	for _, candidate := range candidates {
		samples = appendDistinct(samples, candidate, 3)
	}
	return samples
}

func renderHistory(t *store.LogTemplate, evidence templateEvidence) string {
	var parts []string
	parts = append(parts,
		"first_seen="+t.FirstSeen.UTC().Format(time.RFC3339),
		fmt.Sprintf("lifetime_count=%d", t.Count))
	if evidence.history.ObservedDistinctDays > 0 {
		parts = append(parts, fmt.Sprintf("observed_distinct_days=%d", evidence.history.ObservedDistinctDays))
	}
	if !evidence.history.ObservedFirstDay.IsZero() && !evidence.history.ObservedLastDay.IsZero() {
		parts = append(parts, fmt.Sprintf("observed_day_span=%s..%s",
			evidence.history.ObservedFirstDay.Format("2006-01-02"),
			evidence.history.ObservedLastDay.Format("2006-01-02")))
	}
	if gap := evidence.history.AverageObservedDayGap; gap > 0 {
		parts = append(parts, "observed_day_cadence="+gap.Round(time.Second).String())
	}
	if evidence.history.RetainedCount > 0 {
		parts = append(parts, fmt.Sprintf("retained_count=%d", evidence.history.RetainedCount))
	}
	if evidence.history.RetainedDistinctDays > 0 {
		parts = append(parts, fmt.Sprintf("retained_distinct_days=%d", evidence.history.RetainedDistinctDays))
	}
	return strings.Join(parts, " ")
}
