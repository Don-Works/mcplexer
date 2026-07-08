// severity.go — ordered regex severity classing with per-source
// overrides (log_sources.severity_rules_json).
package distill

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/don-works/mcplexer/internal/store"
)

type severityRule struct {
	re  *regexp.Regexp
	sev string
}

// defaultRules run in order; first match wins. The synthetic
// "logwatch:" collector events class as warn so discontinuities and
// truncations surface in digests without waking the worker alone.
var defaultRules = []severityRule{
	{regexp.MustCompile(`(?i)\b(panic|fatal|out of memory|oom-?kill(ed)?|sigsegv|segfault|data race)\b`), store.SeverityCritical},
	{regexp.MustCompile(`(?i)(\berror\b|\berr=|exception|traceback|stack trace|refused|timed? ?out|unavailable|\bfailed\b|\bfailure\b)`), store.SeverityError},
	{regexp.MustCompile(`(?i)(\bwarn(ing)?\b|^logwatch:)`), store.SeverityWarn},
}

// Classifier applies per-source overrides before the defaults.
type Classifier struct {
	overrides []severityRule
}

// NewClassifier parses severity_rules_json:
// [{"pattern": "<go regexp>", "severity": "error"}, ...]. Empty JSON
// (or "") yields the pure default classifier. Invalid rules error so
// misconfiguration is loud at source-config time, not silently info.
func NewClassifier(rulesJSON string) (*Classifier, error) {
	c := &Classifier{}
	if rulesJSON == "" {
		return c, nil
	}
	var raw []struct {
		Pattern  string `json:"pattern"`
		Severity string `json:"severity"`
	}
	if err := json.Unmarshal([]byte(rulesJSON), &raw); err != nil {
		return nil, fmt.Errorf("distill: severity_rules_json: %w", err)
	}
	for _, r := range raw {
		if !store.ValidSeverity(r.Severity) {
			return nil, fmt.Errorf("distill: severity rule %q: invalid severity %q", r.Pattern, r.Severity)
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("distill: severity rule %q: %w", r.Pattern, err)
		}
		c.overrides = append(c.overrides, severityRule{re: re, sev: r.Severity})
	}
	return c, nil
}

// Classify returns the severity of one line (overrides first, then
// defaults, then info).
func (c *Classifier) Classify(line string) string {
	for _, r := range c.overrides {
		if r.re.MatchString(line) {
			return r.sev
		}
	}
	for _, r := range defaultRules {
		if r.re.MatchString(line) {
			return r.sev
		}
	}
	return store.SeverityInfo
}
