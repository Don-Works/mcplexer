// monitoring_baseline_terminal.go — refusing jobs whose only visible terminal
// line is CONDITIONAL on there being nothing to do.
//
// This exists because of a real, measured job. A scheduled invoice sync on a
// production host logs "finished scheduled job for invoice sync" in its source,
// and that line has NEVER been observed in seven days of retention — because the
// job always returns early through a "no invoices to send" branch. The only
// terminal line the learner can actually see is the early return.
//
// Promoting that early return as the job's success signal is worse than
// promoting nothing. It looks textbook — perfectly periodic, continuous, high
// confidence — and it inverts the alert: the rule stays green for as long as the
// job has no work, and fires "invoice sync has stopped!" the first time the
// customer actually has invoices to send. A false alarm on the day the system
// starts working correctly is precisely the failure this whole feature is biased
// against.
//
// HOW IT IS DETECTED. The line's own text asserts a null outcome. A message that
// says "there was nothing to do" is, by construction, emitted on the branch
// taken when there is nothing to do — it cannot be an unconditional terminal,
// and no amount of statistics can reveal that, because the statistics of a
// reliably-idle job are indistinguishable from those of a reliably-working one.
// Text is the only evidence that carries the distinction, so text is what is
// read. This is lexical matching over the MASKED template (trap 3: never
// file:line), with no model and no network.
//
// WHY A LEXICON IS ENOUGH HERE. The other shape of this problem — a job with two
// branches that BOTH log, alternating — is already handled upstream and needs no
// lexicon: each branch fires only on the subset of runs that took it, so each
// branch's inter-arrival sample is irregular and
// EvaluateBaselineCandidate rejects it as BaselineRejectIrregular. The dangerous
// residue is exactly the case above, where the sibling branch never fires at all
// and the early return therefore looks perfectly regular. That residue is what
// this file covers.
//
// WHY OVER-MATCHING IS ACCEPTABLE. A false positive here costs one job that is
// not auto-monitored, recorded with a reason an operator can read and override
// by authoring a rule by hand. A false negative costs a 3am page on a healthy
// system, which gets the channel muted and the feature switched off. The
// asymmetry is enormous, so the lexicon errs towards refusing.
package store

import (
	"regexp"
	"strings"
)

// baselineNullOutcomeLiterals are phrases that state outright that no work was
// performed. Matched as case-insensitive substrings of the masked template.
var baselineNullOutcomeLiterals = []string{
	"nothing to do",
	"nothing to send",
	"nothing to process",
	"nothing changed",
	"nothing found",
	"no work to do",
	"no work found",
	"no changes",
	"already up to date",
	"already up-to-date",
	"already processed",
	"already complete",
	"queue is empty",
	"queue empty",
	"empty queue",
	"empty batch",
	"empty result",
	"not due yet",
	"not due",
	"nothing due",
	"no-op",
	"noop",
	"skipping",
	"skipped",
	"idle, ",
	"idle - ",
}

// baselineNullOutcomePatterns catch the productive shapes a literal list cannot
// enumerate: "no <thing> to <verb>", "no <thing> found", "0 <thing> processed".
// Each is anchored on a word boundary so "north" never reads as "no r...".
var baselineNullOutcomePatterns = []*regexp.Regexp{
	// "no invoices to send", "no pending orders to process"
	regexp.MustCompile(`\bno (?:[a-z<>_.-]+ ){1,3}to [a-z]+`),
	// "nothing to send", "nothing to reconcile"
	regexp.MustCompile(`\bnothing to [a-z]+`),
	// "no invoices found", "no rows remaining", "no messages queued"
	regexp.MustCompile(`\bno [a-z<>_.-]+ (?:found|available|due|pending|queued|remaining|outstanding|eligible)\b`),
	// "no new invoices", "no pending work", "no unsent messages"
	regexp.MustCompile(`\bno (?:new|pending|unsent|outstanding|matching|eligible|remaining) [a-z<>_.-]+`),
}

// ConditionalTerminalPhrase returns the phrase that marks masked as a
// conditional early-return line, or "" when the line makes no null-outcome
// claim. Comparison is case-insensitive over whitespace-collapsed text, because
// masking already collapses runs of whitespace and an operator-facing reason
// should quote what was actually matched.
func ConditionalTerminalPhrase(masked string) string {
	normalized := baselineNormalizeSpace(strings.ToLower(masked))
	if normalized == "" {
		return ""
	}
	for _, literal := range baselineNullOutcomeLiterals {
		if strings.Contains(normalized, literal) {
			return literal
		}
	}
	for _, pattern := range baselineNullOutcomePatterns {
		if hit := pattern.FindString(normalized); hit != "" {
			return hit
		}
	}
	return ""
}

// baselineNormalizeSpace collapses every run of whitespace to a single space and
// trims the ends, so a template broken across a tab or a double space still
// matches the same phrase.
func baselineNormalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
