// severity.go — ordered regex severity classing with per-source
// overrides (log_sources.severity_rules_json).
package distill

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

type severityRule struct {
	re  *regexp.Regexp
	sev string
}

// criticalKeywordRe matches catastrophe keywords only for unstructured lines.
// An explicit application level remains authoritative even when one of these
// words appears inside a handled message or wrapped error value.
var criticalKeywordRe = regexp.MustCompile(`(?i)\b(panic|fatal|out of memory|oom-?kill(ed)?|sigsegv|segfault|data race)\b`)

// keywordRules are the fallback keyword heuristics, tried only when the
// line has no explicit structured log level. First match wins. The
// synthetic "logwatch:" collector events class as warn.
var keywordRules = []severityRule{
	// A truncated pull makes every conclusion from that window unreliable;
	// treat it as a health alarm, not ordinary warning noise.
	{regexp.MustCompile(`(?i)^logwatch: pull truncated\b`), store.SeverityCritical},
	{regexp.MustCompile(`(?i)(\berror\b|\berr=|exception|traceback|stack trace|refused|timed? ?out|unavailable|\bfailed\b|\bfailure\b)`), store.SeverityError},
	{regexp.MustCompile(`(?i)(\bwarn(ing)?\b|^logwatch:)`), store.SeverityWarn},
}

// namedLevelRe extracts an app's OWN logfmt-style level so a handled info
// event is not reclassified from error text carried as data.
var namedLevelRe = regexp.MustCompile(`(?i)(^|[\s,{\[])['"]?(level|log\.level|severity|lvl)['"]?\s*[:=]\s*['"]?([a-z]+)`)

func mapLevel(s string) string {
	switch strings.ToLower(strings.Trim(s, ":[]()")) {
	case "panic", "fatal", "critical", "crit", "emerg", "alert":
		return store.SeverityCritical
	case "error", "err":
		return store.SeverityError
	case "warn", "warning":
		return store.SeverityWarn
	case "info", "information", "debug", "trace", "notice", "verbose":
		return store.SeverityInfo
	}
	return ""
}

// explicitLevel returns the line's own structured log level, or "" when
// none is discernible. Checks a JSON `"level":"…"` field first, then the
// first few whitespace-delimited tokens for a bare level word. Scanning
// several tokens (not just the first) handles the very common
// "<app-timestamp> <level> <pkg> <msg>" layout — e.g. the Acme logs
// emit "2026-…Z\tinfo\tacme/service.go:110\t…", where the level is the
// SECOND field after docker's own timestamp has been split off.
func explicitLevel(line string) string {
	// Parse JSON rather than regex-scanning it. Only a direct/top-level level
	// field is authoritative; a nested payload's `{"level":"error"}` is
	// message data and must not override a leading INFO level.
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "{") {
		var object map[string]any
		if json.Unmarshal([]byte(trimmed), &object) == nil {
			levels := map[string]string{}
			for key, value := range object {
				if text, ok := value.(string); ok {
					levels[strings.ToLower(key)] = text
				}
			}
			for _, key := range []string{"level", "log.level", "severity", "lvl"} {
				if lvl := mapLevel(levels[key]); lvl != "" {
					return lvl
				}
			}
		}
	}
	// Only the leading fields carry the level; scanning the whole line
	// would match level words inside the message payload. 3 tokens covers
	// "<ts> <level> …" and "<ts> <caller> <level> …" without over-reaching.
	for i, tok := range strings.Fields(line) {
		if i >= 3 {
			break
		}
		if lvl := mapLevel(tok); lvl != "" {
			return lvl
		}
	}
	if m := namedLevelRe.FindStringSubmatch(line); m != nil {
		if lvl := mapLevel(m[3]); lvl != "" {
			return lvl
		}
	}
	return ""
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
	// 1. Operator-configured overrides win outright.
	for _, r := range c.overrides {
		if r.re.MatchString(line) {
			return r.sev
		}
	}
	// 2. The app's OWN structured level is authoritative. Message bodies
	//    routinely contain wrapped errors, SQL text, and exception names;
	//    keyword matching those values overrides the application's explicit
	//    decision that an event was handled.
	if lvl := explicitLevel(line); lvl != "" {
		return lvl
	}
	// 3. Keyword heuristics are only a fallback for genuinely unstructured
	//    lines with no level field/token.
	if criticalKeywordRe.MatchString(line) {
		return store.SeverityCritical
	}
	for _, r := range keywordRules {
		if r.re.MatchString(line) {
			return r.sev
		}
	}
	return store.SeverityInfo
}
