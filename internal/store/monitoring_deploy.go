// monitoring_deploy.go — recognising a deploy, so its restart gap can be
// SUBTRACTED FROM EVIDENCE.
//
// The structural rule this file obeys: a known cause is subtracted from
// evidence, never used as a filter on output. A deploy therefore does two
// things and no others —
//
//  1. corrects the signal's IDENTITY, so a release that shifts line numbers
//     does not mint a new template and restart the learning clock
//     (CadenceKey, monitoring_baseline_identity.go); and
//  2. excises the restart window from the LEARNER'S HISTORY, so the gap a
//     restart creates never becomes part of what "normal" means.
//
// It must never gate a severity path, open a grace window, or mute anything at
// evaluation time. An earlier revision of this file did exactly that — a
// bounded, expiring deploy-grace window that suppressed absence raises — and it
// was removed deliberately. This session had already found two suppression
// mechanisms that between them hid a dead alert channel for six days, and a
// grace window is a third with the same failure shape: if it fails to expire,
// or one deploy chains into the next, a real outage inside the window is
// swallowed. Subtracting the evidence instead means there is no window, nothing
// to expire, and nothing that can fail that way. The problem dissolves rather
// than being managed.
//
// The two detectors stay separate for free. Error alerting ("something is
// BROKEN") runs through distill's severity path and never consults anything
// here; there is no suppression state for it to read, because none exists.
package store

import (
	"regexp"
	"sort"
	"time"
)

// DeployBannerMaxOccurrences rejects a template that merely LOOKS like a
// startup banner but fires constantly. A real deploy banner is emitted once per
// release; a line emitted continuously is an ordinary log line whose text
// happens to match, and honouring it would excise most of a job's history from
// its own baseline.
const DeployBannerMaxOccurrences = 5

// deployBannerRules match the MASKED template text, never a raw line. The
// version string changes on every release — "v5.7.7" masks to "v<n>.<n>.<n>" —
// so matching the masked shape is what makes one rule cover every future
// release without anybody updating it.
//
// These are deliberately narrow. Over-matching now discards real arrivals from
// the learner's evidence, which is a quieter failure than a false alert but
// still a failure: too much excision and a genuine cadence stops being provable.
var deployBannerRules = []*regexp.Regexp{
	// "running version: v<n>.<n>.<n>" — the measured production banner.
	regexp.MustCompile(`(?i)\b(running|starting|started|booting|booted|launching)\b.{0,40}\bversion\b`),
	regexp.MustCompile(`(?i)\bversion\b.{0,40}\b(starting|started|booting|booted)\b`),
	// Conventional startup banners.
	regexp.MustCompile(`(?i)\b(server|service|application|daemon|worker)\b.{0,24}\b(starting|started|listening|ready)\b`),
	regexp.MustCompile(`(?i)\b(starting|started)\b.{0,24}\b(server|service|application|daemon)\b`),
	regexp.MustCompile(`(?i)\blistening on\b`),
}

// IsDeployBanner reports whether a masked template is a startup/version banner.
//
// Callers MUST additionally require the template to be info severity. An error
// line mentioning a version ("unsupported version <n>") is a failure, not a
// release, and excising history around it would let a genuinely broken period
// be quietly dropped from the record of what normal looks like.
func IsDeployBanner(masked string) bool {
	for _, re := range deployBannerRules {
		if re.MatchString(masked) {
			return true
		}
	}
	return false
}

// DeploySpans a gap reports whether a deploy happened strictly inside the
// interval (prev, ts], which is what makes that gap a restart artefact rather
// than evidence of cadence.
//
// deploys MUST be sorted ascending; the miner sorts once per source and reuses
// it across every template, so this stays a binary search rather than a scan.
func DeploySpansGap(deploys []time.Time, prev, ts time.Time) bool {
	i := sort.Search(len(deploys), func(i int) bool { return deploys[i].After(prev) })
	return i < len(deploys) && !deploys[i].After(ts)
}
