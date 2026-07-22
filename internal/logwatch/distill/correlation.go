package distill

import "strings"

// NormalizeCorrelationKey turns an evidence-derived correlation key into the
// stable incident-class suffix shared by deterministic anomaly filing and the
// later worker-triage path. Keeping this transform in distill prevents the two
// paths from agreeing on the evidence but minting different classes.
//
// Volatile tokens containing digits are dropped (counts, line numbers, ports,
// timestamps and ids); punctuation and case differences collapse to spaces.
func NormalizeCorrelationKey(raw string) string {
	var (
		out    []string
		token  strings.Builder
		hasNum bool
	)
	flush := func() {
		if token.Len() > 0 && !hasNum {
			out = append(out, token.String())
		}
		token.Reset()
		hasNum = false
	}
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		switch {
		case r >= 'a' && r <= 'z':
			token.WriteRune(r)
		case r >= '0' && r <= '9':
			token.WriteRune(r)
			hasNum = true
		default:
			flush()
		}
	}
	flush()
	normalized := strings.Join(out, " ")
	if len(normalized) > 200 {
		normalized = normalized[:200]
	}
	return normalized
}
