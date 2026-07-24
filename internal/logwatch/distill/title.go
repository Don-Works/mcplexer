package distill

import (
	"strings"
	"unicode"
)

const operatorTitleLimit = 140

// IsGenericMonitoringTitle reports whether title is the deterministic anomaly
// placeholder ("new error-class log template on …"). Operators cannot act on
// that shape in Chat; evidence-derived titles must replace it before paging.
func IsGenericMonitoringTitle(title string) bool {
	title = strings.ToLower(strings.TrimSpace(title))
	if title == "" {
		return true
	}
	if strings.HasPrefix(title, "new ") && strings.Contains(title, "-class log template on ") {
		return true
	}
	if strings.HasPrefix(title, "observed ") && strings.Contains(title, "-class monitoring event on ") {
		return true
	}
	if strings.HasPrefix(title, "still unresolved: new ") &&
		strings.Contains(title, "-class log template on ") {
		return true
	}
	return false
}

// OperatorSignature extracts a short, human-usable failure signature from a
// redacted sample or masked template. Empty when nothing actionable remains.
func OperatorSignature(sample, masked string) string {
	for _, candidate := range []string{sample, masked} {
		if sig := compactOperatorSignature(candidate); sig != "" {
			return sig
		}
	}
	return ""
}

// OperatorAnomalyTitle builds the Chat/task headline for a fired anomaly.
// Prefer a verified log signature over the generic novelty placeholder.
func OperatorAnomalyTitle(severity, hostName, sourceName string, count int64, isNew bool, sample, masked string) string {
	where := strings.Trim(hostName+"/"+sourceName, "/")
	if where == "" || where == "/" {
		where = "unknown-source"
	}
	if sig := OperatorSignature(sample, masked); sig != "" {
		if isNew {
			return truncateRunes(sig+" — "+where, operatorTitleLimit)
		}
		return truncateRunes(sig+" — recurring on "+where, operatorTitleLimit)
	}
	if isNew {
		return truncateRunes(
			"new "+severity+"-class log template on "+where, operatorTitleLimit)
	}
	return truncateRunes(
		"observed "+severity+"-class monitoring event on "+where, operatorTitleLimit)
}

// ImproveMonitoringTitle replaces a generic novelty title with an evidence
// signature when the worker (or anomaly body) already carries useful text.
func ImproveMonitoringTitle(title, body, sample, masked string) string {
	title = strings.TrimSpace(title)
	if !IsGenericMonitoringTitle(title) && title != "" {
		return truncateRunes(title, operatorTitleLimit)
	}
	if sig := OperatorSignature(sample, masked); sig != "" {
		return truncateRunes(sig, operatorTitleLimit)
	}
	if sig := OperatorSignature(firstEvidenceLine(body), ""); sig != "" {
		return truncateRunes(sig, operatorTitleLimit)
	}
	// Masked template text is still more actionable than the novelty placeholder.
	if m := strings.TrimSpace(masked); m != "" && !IsGenericMonitoringTitle(m) {
		return truncateRunes(m, operatorTitleLimit)
	}
	if title != "" {
		return truncateRunes(title, operatorTitleLimit)
	}
	return "monitoring incident"
}

func compactOperatorSignature(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Drop leading docker/journal decoration that adds no operator value.
	raw = ansiSeverityRe.ReplaceAllString(raw, "")
	raw = strings.Join(strings.Fields(raw), " ")
	// Prefer the clause after a common level token when present.
	lower := strings.ToLower(raw)
	for _, marker := range []string{" error ", " err ", " critical ", " fatal ", " warn ", " warning "} {
		if i := strings.Index(lower, marker); i >= 0 && i < 80 {
			raw = strings.TrimSpace(raw[i+1:])
			break
		}
	}
	// Strip ultra-common prefixes that are not the failure.
	for _, prefix := range []string{
		"ERROR ", "Error ", "error ", "ERR ", "WARN ", "WARNING ",
		"CRITICAL ", "FATAL ", "level=error ", "level=err ",
	} {
		raw = strings.TrimPrefix(raw, prefix)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || isLowValueSignature(raw) {
		return ""
	}
	return truncateRunes(raw, operatorTitleLimit)
}

func isLowValueSignature(s string) bool {
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "new ") && strings.Contains(lower, "log template") {
		return true
	}
	// Pure placeholders / ids are not actionable headlines.
	letters := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return letters < 8
}

func firstEvidenceLine(body string) string {
	skip := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*•"))
		if line == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSuffix(line, ":")) {
		case "observed evidence", "verified facts":
			skip = false
			continue
		case "hypotheses / unknowns", "hypotheses/unknowns", "next step", "next steps":
			skip = true
			continue
		case "template":
			continue
		}
		if skip {
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "template:") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "first sample:") {
			return strings.TrimSpace(line[len("first sample:"):])
		}
		return line
	}
	return ""
}

func truncateRunes(s string, limit int) string {
	runes := []rune(strings.TrimSpace(s))
	if limit <= 0 || len(runes) <= limit {
		return string(runes)
	}
	cut := limit - 1
	for cut > limit-24 && cut > 0 && !unicode.IsSpace(runes[cut]) {
		cut--
	}
	if cut <= 0 {
		cut = limit - 1
	}
	return strings.TrimSpace(string(runes[:cut])) + "…"
}
