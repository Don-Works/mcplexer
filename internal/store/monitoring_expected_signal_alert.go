// monitoring_expected_signal_alert.go — human-facing text for absence and
// collection incidents.
//
// The learner names a rule "auto/<hash-of-template>", which is a stable join
// key but meaningless to a person. Left in the alert headline it produces
// "CRITICAL · <system> · expected signal 'auto/9d8cc9032ba3' has stopped" — a
// pager message that tells the operator nothing. This file rebuilds the title
// and body to LEAD with what a human needs at a glance: WHAT stopped (the
// matched text, not the hash), WHERE (the source's display label), HOW ABNORMAL
// (expected vs observed), and WHEN it was last seen. The auto/ name survives as
// a footer identifier only.
//
// Rendering is pure deterministic string construction — no clock, store or
// model. MatchSubstring is arbitrary log text, so every fragment is stripped of
// control characters, collapsed to one line and hard-truncated before it can
// reach a downstream renderer.
package store

import (
	"fmt"
	"strings"
	"time"
)

// Fragment caps. A phone title must survive being re-truncated by every
// downstream renderer, so the matched-text fragment is capped hard; the body
// may show a little more but is still single-lined and bounded.
const (
	maxAlertSignalTitle = 72
	maxAlertSignalBody  = 200
	maxAlertSourceLabel = 64
)

// ExpectedSignalAlert carries the pre-resolved facts the human-facing incident
// text is built from. Source is the source's display label (its name or
// selector); the caller resolves it when it cheaply can and passes the raw
// source id otherwise. Every other field is copied straight off the rule,
// decision, observation and pull health — no derivation happens here beyond
// formatting.
type ExpectedSignalAlert struct {
	Outcome  ExpectedSignalOutcome
	Reason   string
	Match    string // MatchSubstring; empty means "any log line"
	Source   string // resolved display label, or the raw source id
	MinCount int64
	Window   time.Duration
	Observed int64      // matches seen in the window (0 for a clean absence)
	Total    int64      // lines of ANY kind from the source — collection liveness
	LastSeen *time.Time // last confirmed arrival, if known
	Failures int        // consecutive collection failures, for the collection body
	RuleName string     // stable auto/ join key — footer only
	Now      time.Time
}

// NewExpectedSignalAlert copies the facts a raising decision and its observation
// carry. LastSeen prefers the observed last match (which spans retained
// history) over the rule's latched last-signal time. When source is blank it
// falls back to the rule's raw source id so the text is never empty.
func NewExpectedSignalAlert(
	rule MonitoringExpectedSignal, d ExpectedSignalDecision,
	obs ExpectedSignalObservation, health SourceCollectionHealth, source string,
) ExpectedSignalAlert {
	last := rule.LastSignalAt
	if obs.LastMatchAt != nil {
		last = obs.LastMatchAt
	}
	if strings.TrimSpace(source) == "" {
		source = rule.SourceID
	}
	return ExpectedSignalAlert{
		Outcome: d.Outcome, Reason: d.Reason,
		Match: rule.MatchSubstring, Source: source,
		MinCount: rule.MinCount, Window: rule.Window(),
		Observed: obs.MatchCount, Total: obs.TotalLines,
		LastSeen: last, Failures: health.ConsecutiveFailures,
		RuleName: rule.Name, Now: d.WindowEnd.UTC(),
	}
}

// Title is the one-line headline. Collection reads "can't verify …" because the
// honest claim is lost visibility, not that the signal stopped.
func (a ExpectedSignalAlert) Title() string {
	signal := a.signalPhrase(maxAlertSignalTitle)
	source := a.sourceLabel()
	if a.Outcome == OutcomeSignalCollection {
		return fmt.Sprintf("can't verify %s from %s — %s", signal, source, a.collectionReason())
	}
	win := compactDuration(a.Window)
	return fmt.Sprintf("no %s from %s in %s (normally >=%d/%s, saw %d)%s",
		signal, source, win, a.MinCount, win, a.Observed, a.lastSeenClause())
}

// Body is the multi-line explanation. It leads with the same human context as
// the title and closes with the auto/ rule name as a stable footer identifier.
func (a ExpectedSignalAlert) Body() string {
	var b strings.Builder
	signal := a.signalPhrase(maxAlertSignalBody)
	source := a.sourceLabel()
	win := compactDuration(a.Window)
	if a.Outcome == OutcomeSignalCollection {
		fmt.Fprintf(&b, "Cannot verify %s from %s: %s\n", signal, source, a.collectionDetail())
		b.WriteString("This is a COLLECTION problem, not proof the signal stopped — " +
			"fix visibility first.\n")
	} else {
		fmt.Fprintf(&b, "No %s from %s in the last %s.\n", signal, source, win)
		fmt.Fprintf(&b, "Normally at least %d per %s; saw %d. Collection is healthy "+
			"(%d line(s) from this source in the window), so this is a real absence, "+
			"not lost visibility.\n", a.MinCount, win, a.Observed, a.Total)
	}
	b.WriteString(a.lastSeenLine())
	fmt.Fprintf(&b, "\nRule: %s (learned automatically) · evaluated %s",
		a.RuleName, a.Now.Format(time.RFC3339))
	return b.String()
}

// RecoveryTitle and RecoveryBody are the "it came back" counterparts, built
// from just the match and source so recovery reads as plainly as the raise.
func (a ExpectedSignalAlert) RecoveryTitle() string {
	return fmt.Sprintf("Recovered: %s from %s is arriving again",
		a.signalPhrase(maxAlertSignalTitle), a.sourceLabel())
}

func (a ExpectedSignalAlert) RecoveryBody() string {
	return fmt.Sprintf("%s from %s is arriving again as of %s.\n\nRule: %s (learned automatically)",
		a.signalPhrase(maxAlertSignalBody), a.sourceLabel(),
		a.Now.Format(time.RFC3339), a.RuleName)
}

// signalPhrase quotes the matched text, sanitised and truncated. An empty match
// means the rule expects ANY line from the source.
func (a ExpectedSignalAlert) signalPhrase(max int) string {
	frag := sanitizeAlertText(a.Match, max)
	if frag == "" {
		return "any log line"
	}
	return `"` + frag + `"`
}

func (a ExpectedSignalAlert) sourceLabel() string {
	s := sanitizeAlertText(a.Source, maxAlertSourceLabel)
	if s == "" {
		return "unknown source"
	}
	return s
}

func (a ExpectedSignalAlert) lastSeenClause() string {
	if a.LastSeen == nil {
		return "; not seen in retained history"
	}
	return "; last seen " + compactDuration(a.sinceLastSeen()) + " ago"
}

func (a ExpectedSignalAlert) lastSeenLine() string {
	if a.LastSeen == nil {
		return "Last seen: never within retained history.\n"
	}
	return fmt.Sprintf("Last seen: %s (%s ago).\n",
		a.LastSeen.UTC().Format(time.RFC3339), compactDuration(a.sinceLastSeen()))
}

func (a ExpectedSignalAlert) sinceLastSeen() time.Duration {
	ago := a.Now.Sub(a.LastSeen.UTC())
	if ago < 0 {
		return 0
	}
	return ago
}

func (a ExpectedSignalAlert) collectionReason() string {
	switch a.Reason {
	case ReasonSourceDisabled:
		return "the log source is disabled"
	case ReasonPullFailing:
		return "log collection is failing"
	case ReasonSourceSilent:
		return "the source went silent"
	default:
		return "collection cannot be verified"
	}
}

func (a ExpectedSignalAlert) collectionDetail() string {
	switch a.Reason {
	case ReasonSourceDisabled:
		return "the log source is disabled, so the expected signal cannot be checked at all"
	case ReasonPullFailing:
		return fmt.Sprintf("log collection has failed %d consecutive time(s), so the "+
			"expected signal cannot be checked", a.Failures)
	case ReasonSourceSilent:
		return fmt.Sprintf("the source produced no lines of any kind in the last %s, which is "+
			"indistinguishable from lost visibility", compactDuration(a.Window))
	default:
		return "the expected signal cannot be verified right now"
	}
}

// sanitizeAlertText makes arbitrary log text safe for a single-line headline:
// control characters and newlines become word breaks, runs of whitespace
// collapse, and the result is rune-truncated with an ellipsis. This is the
// injection guard — no fragment reaching a renderer carries a newline or a
// control byte.
func sanitizeAlertText(s string, max int) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		default:
			return r
		}
	}, s)
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	if max <= 0 {
		return cleaned
	}
	runes := []rune(cleaned)
	if len(runes) <= max {
		return cleaned
	}
	return strings.TrimSpace(string(runes[:max])) + "…"
}

// compactDuration renders a duration for a human at a glance: "45s", "30m",
// "1h", "2h30m", "3d", "3d4h". The two most significant units are enough for an
// operator; sub-second precision is noise on a pager. Units truncate rather
// than round so a value can never overflow into a nonsensical "1h60m".
func compactDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		h := int(d / time.Hour)
		m := int((d % time.Hour) / time.Minute)
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		days := int(d / (24 * time.Hour))
		h := int((d % (24 * time.Hour)) / time.Hour)
		if h == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd%dh", days, h)
	}
}
