// monitoring_baseline_identity.go — the identity a learned cadence is keyed on.
//
// Templates are identified by sha256(source_id, masked_text), so a template id
// is a pure function of the masked line. That is exactly right for triage and
// exactly wrong for learning a schedule, because the masker deliberately
// PROTECTS code locations from masking:
//
//	distill/mask.go taxonomyRules: `\b[A-Za-z0-9_./-]+\.(?:go|rs|py|...):\d+\b`
//	"Code locations are the monitor's strongest deterministic correlation key.
//	 A line number is an identifier here, not a varying numeric value."
//
// That is a good decision for grouping failures and a fatal one for cadence.
// A redeploy that shifts `ordersync.go:142` to `ordersync.go:151` produces
// different masked text, therefore a different template id, therefore a fresh
// mining history, day-coverage history and baseline row — for a job that did
// not change at all. Measured at the real incident: the completion template had
// 70.33h of history against a 72h floor, missing by 1.67 hours, purely because
// of a redeploy three days earlier. Observed deploy intervals were 17 days and
// then 3 days, so a rule can be permanently prevented from bootstrapping by an
// ordinary release cadence.
//
// This is a BOOTSTRAP defect only. Once promoted, a rule matches raw lines by
// substring (ObserveExpectedSignal uses instr(lower(l.line), ?)) and never
// consults a template id, so a live rule already survives redeploys untouched.
//
// The fix is a second, learning-only identity that additionally collapses the
// line number. Triage keeps the precise location it relies on; the learner gets
// a key that is stable across releases. Note that keying on the masked TEXT
// instead of the template id would change nothing whatsoever — the id is a hash
// of that text, so the two are the same information.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
)

// cadenceCodeLineRe matches a protected code location and captures everything
// except its line number. The file-extension list mirrors the masker's
// taxonomy rule, because this only needs to undo what that rule protected.
var cadenceCodeLineRe = regexp.MustCompile(
	`\b([A-Za-z0-9_./-]+\.(?:go|rs|py|php|js|jsx|ts|tsx|java|cs|rb|kt|swift|c|cc|cpp|h|hpp)):\d+\b`)

// CadenceNormalize collapses release-volatile atoms in a masked template.
//
// Only the line NUMBER inside a code location is replaced. The message text is
// left exactly as it is, so two genuinely different log statements stay
// different: this cannot merge templates that merely share a prefix, because it
// changes no character outside a `file.ext:123` match. The one merge it does
// perform is two locations in the SAME file whose surrounding text is already
// identical — which is to say, two lines an operator could not tell apart in
// the log either.
func CadenceNormalize(masked string) string {
	return cadenceCodeLineRe.ReplaceAllString(masked, "$1:<line>")
}

// CadenceKey is the stable per-source identity the learner mines, stores and
// names rules by. It is deliberately the same shape as a template id — a hex
// sha256 over source and text — so it drops into the existing template_id
// column, which carries no foreign key to log_templates precisely so that a
// pruned template cannot delete what was learned from it.
func CadenceKey(sourceID, masked string) string {
	sum := sha256.Sum256([]byte(sourceID + "\x00" + CadenceNormalize(masked)))
	return hex.EncodeToString(sum[:])
}
