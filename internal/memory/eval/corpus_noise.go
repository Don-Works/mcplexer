package eval

import (
	"fmt"
	"strings"
	"time"
)

// corpus_noise.go generates the two distractor populations ScaledCorpus mixes
// around the labeled gold documents.
//
//	RIVALS      share exactly ONE discriminative token with one labeled query,
//	            wrapped in prose that answers nothing. They are what makes the
//	            candidate pool contested: without them the FTS pool for a query
//	            like "payment idempotency double charge bug" contains the gold
//	            answer and almost nothing else, and a "500-document saturated
//	            pool" is a fiction for that query. Real production distractors
//	            DO share vocabulary; the ones that share none are the easy case.
//	BACKGROUND  share no topical vocabulary at all and reach the pool only via
//	            generic connectives, exactly as an unrelated production memory
//	            does through the OR-joined FTS query.
//
// The invariant that keeps both honest: a noise document may carry AT MOST ONE
// discriminative token, and no noise key ever appears in a query's
// RelevantKeys. It can crowd the pool and it can outrank a weak gold document
// on freshness or staleness, but it can never out-match the gold answer on
// coverage and it is never a correct answer. TestScaledCorpusIsHonest asserts
// both directly rather than trusting this comment.

// noiseThemes are the "brand voice guide" analogue: plausible agent-memory
// content that shares NO topical vocabulary with any labeled query. Each theme
// contributes an equal share of both noise populations.
var noiseThemes = []struct{ slug, body string }{
	{"voice-guide", "House voice guide. Keep sentences short and confident. Avoid hedging adverbs. Prefer active phrasing over passive constructions."},
	{"standup-note", "Standup summary. Nothing surprising landed overnight. The board is clear and no one raised a blocker during the round."},
	{"campaign-copy", "Campaign copy draft. The headline leads with the outcome. The subhead names the audience. Every paragraph ends on a verb."},
	{"design-token", "Design token pass. Spacing steps move to a four unit scale. Corner radii collapse into three named sizes."},
	{"travel-plan", "Travel plan. Depart Tuesday morning, return Friday evening. Book the aisle seat and skip the hold luggage."},
	{"recipe-card", "Recipe card. Warm the pan before the oil goes in. Season at the end, taste twice, and rest the dish before plating."},
	{"reading-list", "Reading list. Two essays on attention, one long profile, and a short book about weather forecasting."},
	{"gym-log", "Training log. Easy pace for forty minutes, then eight strides. Legs felt heavy but the session finished clean."},
	{"invoice-note", "Invoice note. Terms are thirty days from issue. Late items roll into the following statement without a reminder."},
	{"garden-note", "Garden note. The south bed drains badly after rain. Move the pots before the first frost arrives."},
}

// connectives are generic, non-discriminative English tokens that also appear
// in the labeled queries. They are sprinkled into background documents because
// sanitizeFTS5Query OR-joins every query term with no stopword list, so in
// production ANY document containing a common word becomes a pool candidate.
//
// This list doubles as the stopword list for discriminativeTokens: a query
// token that appears here carries no relevance signal and is never grafted
// into a rival.
var connectives = []string{
	"which", "does", "use", "what", "is", "how", "should", "be",
	"why", "was", "a", "the", "this", "does not", "used", "being",
}

// connectiveSet is connectives exploded into individual tokens.
var connectiveSet = func() map[string]struct{} {
	out := make(map[string]struct{}, len(connectives))
	for _, w := range connectives {
		for _, t := range tokens(w) {
			out[t] = struct{}{}
		}
	}
	return out
}()

// rivalTemplates graft a single query token into a sentence that explicitly
// records a NON-answer. None of these words appear in any labeled query, so
// the template itself never adds a second discriminative token.
var rivalTemplates = []string{
	"Someone mentioned %s in passing; no decision was recorded.",
	"The %s thread was closed with nothing agreed.",
	"A question about %s is still open and unowned.",
	"Filed under %s for later triage; nothing was concluded.",
}

// discriminativeTokens returns the topical tokens of a query: lowercased,
// de-duplicated, at least three characters, and not a generic connective.
// Those are the only tokens that can carry real relevance signal.
func discriminativeTokens(query string) []string {
	out := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	for _, tok := range tokens(query) {
		if len(tok) < 3 {
			continue
		}
		if _, generic := connectiveSet[tok]; generic {
			continue
		}
		if _, dup := seen[tok]; dup {
			continue
		}
		seen[tok] = struct{}{}
		out = append(out, tok)
	}
	return out
}

// TopicalVocabulary is the union of every query's discriminative tokens — the
// full set of tokens that carry relevance signal in this corpus. Exported so
// the honesty test can assert the at-most-one-topical-token invariant over the
// generated noise instead of trusting the generator.
func TopicalVocabulary(queries []FixtureQuery) map[string]struct{} {
	out := make(map[string]struct{})
	for _, q := range queries {
		for _, tok := range discriminativeTokens(q.Query) {
			out[tok] = struct{}{}
		}
	}
	return out
}

// rivalNoise builds perQuery contested distractors for every labeled query.
// Each carries exactly one of that query's discriminative tokens and alternates
// between the rival-fresh and rival-old bands, so every query is opposed from
// both ends of the age distribution.
func rivalNoise(queries []FixtureQuery, now time.Time, perQuery int) []FixtureMemory {
	out := make([]FixtureMemory, 0, len(queries)*perQuery)
	n := 0
	for qi, q := range queries {
		toks := discriminativeTokens(q.Query)
		if len(toks) == 0 {
			continue
		}
		for j := 0; j < perQuery; j++ {
			th := noiseThemes[(qi+j)%len(noiseThemes)]
			graft := fmt.Sprintf(rivalTemplates[j%len(rivalTemplates)], toks[j%len(toks)])
			out = append(out, FixtureMemory{
				Key:       fmt.Sprintf("rival-%04d", n),
				Name:      fmt.Sprintf("%s-rival-%04d", th.slug, n),
				Content:   fmt.Sprintf("%s %s Entry %d.", th.body, graft, n),
				Tags:      []string{"noise", th.slug},
				UpdatedAt: now.Add(-rivalAge(j, n)),
			})
			n++
		}
	}
	return out
}

// rivalAge alternates a query's rivals between the two rival bands by their
// within-query index, so perQuery >= minRivalsPerQuery guarantees both. The
// global index only spreads the age inside the chosen band.
func rivalAge(withinQuery, global int) time.Duration {
	if withinQuery%2 == 0 {
		return spanAge(0, rivalFreshMaxHours-1, global, time.Hour)
	}
	return spanAge(rivalOldMinDays, rivalOldMaxDays, global, day)
}

// backgroundNoise generates the topically-inert population: rotating themed
// bodies salted with a distinct index and a rotating slice of connectives, so
// each document is textually unique (BM25 sees real variation), carries no
// relevance signal, and still reaches the pool for multi-word probes.
func backgroundNoise(now time.Time, n int) []FixtureMemory {
	out := make([]FixtureMemory, 0, n)
	for i := 0; i < n; i++ {
		th := noiseThemes[i%len(noiseThemes)]
		out = append(out, FixtureMemory{
			Key:       fmt.Sprintf("noise-%04d", i),
			Name:      fmt.Sprintf("%s-%04d", th.slug, i),
			Content:   noiseBody(th.body, i),
			Tags:      []string{"noise", th.slug},
			UpdatedAt: now.Add(-backgroundAge(i)),
		})
	}
	return out
}

// backgroundAge rotates the inert population through ALL FOUR age bands. Any
// band left exclusively to one label class would reintroduce the age/relevance
// confound this corpus exists to remove.
func backgroundAge(i int) time.Duration {
	switch i % 4 {
	case 0:
		return spanAge(0, rivalFreshMaxHours-1, i, time.Hour)
	case 1:
		return spanAge(goldFreshMinDays, goldFreshMaxDays, i, day)
	case 2:
		return spanAge(goldOldMinDays, goldOldMaxDays, i, day)
	default:
		return spanAge(rivalOldMinDays, rivalOldMaxDays, i, day)
	}
}

// noiseBody appends a deterministic rotating window of connectives plus an
// entry number to a theme body.
func noiseBody(body string, i int) string {
	var b strings.Builder
	b.WriteString(body)
	fmt.Fprintf(&b, " Entry %d.", i)
	for j := 0; j < 4; j++ {
		b.WriteString(" ")
		b.WriteString(connectives[(i+j)%len(connectives)])
	}
	b.WriteString(" noted for later review.")
	return b.String()
}

// tokens lowercases and splits on anything that is not an ASCII alphanumeric —
// a close enough stand-in for the FTS5 unicode61 tokenizer for corpus
// generation, corpus auditing, and the hashed bag-of-words test embedder.
func tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}
