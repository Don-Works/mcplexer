// monitoring_baseline_group.go — collapsing a source's templates into the
// cadence identities the learner actually reasons about.
//
// A redeploy shifts the line numbers embedded in code locations, and the masker
// protects those on purpose, so ONE job can own several template ids across a
// release boundary. Mining them separately splits the job's history at every
// deploy and is why a rule could not bootstrap between two releases three days
// apart. Grouping happens here, before any statistic is computed, so everything
// downstream sees one continuous job.
//
// This file is pure: it takes rows and returns groups. The policy that used to
// live in the SQL HAVING clause moved here for one specific reason — the
// minimum-sample floor must apply to the GROUP, not to each template. Applied
// per template it discards exactly the halves of a freshly-redeployed job that
// need to be added together.
package sqlite

import (
	"sort"

	"github.com/don-works/mcplexer/internal/store"
)

// baselineTemplate is one shortlisted template row.
type baselineTemplate struct {
	id     string
	masked string
	lines  int64
}

// baselineGroup is every template id sharing one cadence key.
type baselineGroup struct {
	key string
	// masked is the representative text, taken from the group's most
	// numerous template so the derived matcher is verified against the shape
	// most of the retained lines actually have.
	masked string
	ids    []string
	lines  int64
}

// baselineGroupByCadence folds templates into cadence groups and applies the
// shortlist policy to the folded totals.
//
// Ordering is by total line count ASCENDING, which is the opposite of what
// feels natural and is load bearing: the chattiest templates on a busy source
// are per-request logs, not scheduled jobs, so ordering by count DESC would
// fill every slot with them and squeeze out the cron-shaped signals this
// feature exists to find.
func baselineGroupByCadence(sourceID string, templates []baselineTemplate) []baselineGroup {
	byKey := map[string]*baselineGroup{}
	best := map[string]int64{}
	for _, t := range templates {
		key := store.CadenceKey(sourceID, t.masked)
		g, ok := byKey[key]
		if !ok {
			g = &baselineGroup{key: key}
			byKey[key] = g
		}
		g.ids = append(g.ids, t.id)
		g.lines += t.lines
		if t.lines >= best[key] {
			best[key], g.masked = t.lines, t.masked
		}
	}
	out := make([]baselineGroup, 0, len(byKey))
	for _, g := range byKey {
		if g.lines <= store.BaselineMinDeltas || g.lines > baselineMaxTemplateLines {
			continue
		}
		sort.Strings(g.ids)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].lines != out[j].lines {
			return out[i].lines < out[j].lines
		}
		return out[i].key < out[j].key
	})
	if len(out) > store.BaselineMaxTemplatesPerSource {
		out = out[:store.BaselineMaxTemplatesPerSource]
	}
	return out
}

// baselineGroupTemplateIDs flattens every template id across groups, which is
// what the arrival scan binds into its IN clause.
func baselineGroupTemplateIDs(groups []baselineGroup) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.ids...)
	}
	return out
}

// baselineCadenceByTemplate maps each template id to its cadence key, so the
// arrival stream can merge rows from several releases into one series.
func baselineCadenceByTemplate(groups []baselineGroup) map[string]string {
	out := make(map[string]string, len(groups))
	for _, g := range groups {
		for _, id := range g.ids {
			out[id] = g.key
		}
	}
	return out
}
