package skillregistry

import (
	"strings"
)

// DefaultRefinementQuorum is the number of agents (counted as distinct
// proposals with the same skill + fuzzy-matched friction) required to
// promote a proposal from "pending" to "candidate". Picked at 3 to
// balance signal vs. noise:
//   - 1 is just one agent's gripe — Goodhart waiting to happen.
//   - 2 might be two sessions of the same agent retrying the same
//     workflow on the same friction; still single-data-point territory.
//   - 3 is the first count where you can reasonably expect independent
//     hands hitting the same wall. Operators can dial this up via the
//     callable constant when their workspace has more agents firing
//     proposals to keep the inbox tight.
//
// Configurable knob: callers pass an explicit threshold to
// QuorumThreshold(); this constant is just the default.
const DefaultRefinementQuorum = 3

// QuorumThreshold returns the effective quorum count. Pass 0 to use
// the default; positive values override. Negative values are clamped
// to 1 so a misconfigured "always candidate" can't silently flip the
// safety gate off.
func QuorumThreshold(override int) int {
	if override <= 0 {
		return DefaultRefinementQuorum
	}
	return override
}

// FuzzyFrictionKey normalises a friction string into a stable key the
// quorum aggregator can group by. The goal is "did two agents complain
// about the same thing?" without forcing both to type the same words.
//
// THIS MILESTONE: deliberately simple — lowercase + collapse whitespace
// + take first 50 runes. That catches the common case where two agents
// describe the same friction in mostly-the-same language ("ffmpeg fails
// on h.265" vs "ffmpeg fails on h.265 with -c:v copy") but won't catch
// paraphrase ("the encoder can't handle hevc"). The aggregator
// documents this limitation so a future refinement (heh) can swap in
// embedding-similarity or LLM-based clustering without rewriting any
// of the call sites.
//
// Stable across goroutines; pure function.
func FuzzyFrictionKey(friction string) string {
	s := strings.ToLower(strings.TrimSpace(friction))
	// Collapse runs of whitespace into single spaces so "foo  bar" and
	// "foo bar" hash the same.
	fields := strings.Fields(s)
	s = strings.Join(fields, " ")
	const keyLen = 50
	if len(s) <= keyLen {
		return s
	}
	// Rune-safe truncation: a multi-byte UTF-8 codepoint at the cut
	// boundary would otherwise leave the string mid-codepoint and the
	// LIKE-pattern would corrupt the DB substring lookup.
	runes := []rune(s)
	if len(runes) <= keyLen {
		return s
	}
	return string(runes[:keyLen])
}

// SimilarFrictionPattern returns the substring the SQLite store should
// LIKE-match against a freshly-inserted proposal's friction. Today
// this is just FuzzyFrictionKey — paired with `lower(friction) LIKE
// lower(?)` on the DB side, two proposals whose first 50 lower-cased
// characters overlap will be counted as "the same friction". A future
// refinement can return a richer match expression (token-set, embed
// k-NN) without touching the SQL caller — keep CountSimilarProposals's
// signature stable.
func SimilarFrictionPattern(friction string) string {
	return FuzzyFrictionKey(friction)
}

// QuorumReached reports whether `count` of similar proposals is enough
// to flip a freshly-inserted proposal to `candidate` status. The
// proposal being inserted IS counted in `count` — that's why the
// canonical caller passes the count taken AFTER the INSERT.
func QuorumReached(count, threshold int) bool {
	return count >= QuorumThreshold(threshold)
}
