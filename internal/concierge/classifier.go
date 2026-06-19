// classifier.go — concierge per-turn signal classifier. Maps a user
// reply onto a ChatTurnLabel. The rule-based default ships with the
// daemon; a future model-backed classifier (Haiku-class one-shot) can
// substitute in for the ambiguous middle without touching call sites.
//
// Why rule-first: the friction extractor only needs to surface obvious
// negative signals to produce useful refinement proposals. A
// confirmation-vs-neutral coin-flip is acceptable noise; missing every
// "no, that's wrong" or "thanks!" is not. Rules nail the high-signal
// cases at zero LLM cost; a model can take the middle later.
package concierge

import (
	"regexp"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// ClassifyOutput is what Classify returns.
type ClassifyOutput struct {
	Label      string  // one of store.ChatTurnLabel*
	Confidence float64 // 0..1, classifier self-rated
	Kind       string  // store.ChatTurnClassifierRule | ChatTurnClassifierModel
}

// Classifier is the abstraction over the per-turn label producer.
// Plug a model-backed one in without changing concierge.Service.
type Classifier interface {
	Classify(userMessage, assistantMessage string) ClassifyOutput
}

// RuleClassifier is the default rule-based classifier. Order of
// evaluation matters: frustration > correction > escalation > redirect
// > confirmation > neutral. A turn with both "this is wrong" + "thanks
// for trying" classifies as correction (the actionable signal trumps
// the politeness).
type RuleClassifier struct{}

// NewRuleClassifier constructs a RuleClassifier. Stateless and
// concurrency-safe.
func NewRuleClassifier() *RuleClassifier {
	return &RuleClassifier{}
}

// patterns are evaluated against the lower-cased user message. Order
// of the slice = priority. Compiled once at package init.
type pattern struct {
	label      string
	rx         *regexp.Regexp
	confidence float64
}

var classifierPatterns = []pattern{
	// Frustration — explicit unhappiness.
	{store.ChatTurnLabelFrustration, regexp.MustCompile(`\b(stop|fucking|fuck|ridiculous|hate this|why are you|come on|ugh|seriously\?|are you kidding)\b`), 0.9},
	{store.ChatTurnLabelFrustration, regexp.MustCompile(`!{2,}`), 0.7},  // multiple exclamation marks
	{store.ChatTurnLabelFrustration, regexp.MustCompile(`\?{2,}`), 0.7}, // "??" — exasperation

	// Correction — agent got something wrong.
	{store.ChatTurnLabelCorrection, regexp.MustCompile(`\b(no,? that(’|')?s wrong|that(’|')?s not (right|correct)|incorrect|you(’|')?re wrong|wrong answer|that(’|')?s not what i meant)\b`), 0.95},
	{store.ChatTurnLabelCorrection, regexp.MustCompile(`\b(actually|i meant|i said|let me clarify|no,? i)\b`), 0.7},
	{store.ChatTurnLabelCorrection, regexp.MustCompile(`^(no\b|nope\b|nah\b)`), 0.75},

	// Escalation — wants a human / different model / different person.
	{store.ChatTurnLabelEscalation, regexp.MustCompile(`\b(talk to (a )?human|get me (a )?human|escalate|need a person|switch to (a )?different)\b`), 0.85},

	// Redirect — same conversation, different topic. Often "OK now let's…".
	{store.ChatTurnLabelRedirect, regexp.MustCompile(`\b(now |next |let(’|')?s switch|change of topic|moving on|different question)\b`), 0.6},

	// Confirmation — positive ack.
	{store.ChatTurnLabelConfirmation, regexp.MustCompile(`\b(thanks!?|thank you|perfect|exactly|that(’|')?s it|yes,? that(’|')?s right|great,? thanks|brilliant|nice)\b`), 0.85},
	{store.ChatTurnLabelConfirmation, regexp.MustCompile(`^(yes\b|yep\b|yeah\b|yup\b|👍|🎉|❤️)`), 0.7},
}

// Classify returns the highest-priority pattern match, defaulting to
// neutral when nothing fires. The classifier doesn't need to "see" the
// assistant message for the v1 ruleset, but the signature carries it
// so a future model-backed classifier can use the prior context.
func (RuleClassifier) Classify(userMessage, _assistantMessage string) ClassifyOutput {
	msg := strings.ToLower(strings.TrimSpace(userMessage))
	if msg == "" {
		return ClassifyOutput{
			Label:      store.ChatTurnLabelNeutral,
			Confidence: 0.5,
			Kind:       store.ChatTurnClassifierRule,
		}
	}
	for _, p := range classifierPatterns {
		if p.rx.MatchString(msg) {
			return ClassifyOutput{
				Label:      p.label,
				Confidence: p.confidence,
				Kind:       store.ChatTurnClassifierRule,
			}
		}
	}
	return ClassifyOutput{
		Label:      store.ChatTurnLabelNeutral,
		Confidence: 0.5,
		Kind:       store.ChatTurnClassifierRule,
	}
}
