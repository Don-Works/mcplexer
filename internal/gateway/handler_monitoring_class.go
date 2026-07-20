// handler_monitoring_class.go — how a triage decision is mapped onto a stable
// incident CLASS, which is what elects the one canonical task.
//
// This is the duplicate-filing fix at source. Migration 143's partial unique
// index on tasks(workspace_id, meta.$.logwatch_class) already guarantees one
// live task per class and lets a losing racer recover on ErrAlreadyExists
// rather than filing a sibling — that part works. What it cannot do is stop
// every run from computing a DIFFERENT class key out of reworded model free
// text, which is what actually produced the observed duplicate families.
package gateway

import (
	"context"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

func (h *handler) monitoringClassForTemplates(
	ctx context.Context, workspaceID string, ids []string, correlationKey string,
) (string, *store.MonitoringIncident, error) {
	mapped, err := h.store.ListMonitoringIncidentsByTemplateIDs(ctx, workspaceID, ids)
	if err != nil {
		return "", nil, err
	}
	if len(mapped) > 1 {
		classKeys := make([]string, 0, len(mapped))
		for _, incident := range mapped {
			classKeys = append(classKeys, incident.ClassKey)
		}
		return "", nil, fmt.Errorf("selected templates already belong to different incident classes: %s", strings.Join(classKeys, ", "))
	}
	if len(mapped) == 1 {
		return mapped[0].ClassKey, mapped[0], nil
	}
	if normalized := normalizeMonitoringCorrelationKey(correlationKey); normalized != "" {
		return "correlation:" + normalized, nil, nil
	}
	return "template:" + ids[0], nil, nil
}

// normalizeMonitoringCorrelationKey collapses the volatile parts of a
// model-authored correlation key so that rewordings of the SAME operational
// class land on the same incident instead of minting a new one.
//
// This is the duplicate-filing fix at source that the DB constraint cannot
// make: migration 143's unique index correctly guarantees one live task per
// class_key, but it is powerless when every run computes a DIFFERENT
// class_key. The key was previously prefixed verbatim, so
//
//	"source discontinuity (6 restarts)"
//	"source discontinuity (7 restarts)"
//	"Source Discontinuity — 6 restarts!"
//
// were three classes, three incidents and three sibling tasks for one problem.
// Normalising to "source discontinuity restarts" makes them one.
//
// The transform is deliberately dumb and total: lowercase, split on every
// non-alphanumeric rune (so "api-A|ordersync.go:42" tokenises the same way
// "api A ordersync go 42" does), then drop any token containing a digit —
// counts, line numbers, ports, pids and timestamps are exactly the parts that
// differ run to run. Pure string manipulation, no model call.
//
// It can only ever MERGE keys, never split one, and it cannot strand an
// existing incident: templates already linked to an incident are matched by
// ListMonitoringIncidentsByTemplateIDs above and never reach this line. A key
// that normalises to nothing (a pure counter) falls through to the
// template-derived class rather than colliding every empty key into one.
func normalizeMonitoringCorrelationKey(raw string) string {
	var (
		out    []string
		token  strings.Builder
		hasNum bool
	)
	// flush ends the current token, keeping it only if it carries no digit.
	flush := func() {
		if token.Len() > 0 && !hasNum {
			out = append(out, token.String())
		}
		token.Reset()
		hasNum = false
	}
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		switch {
		case r >= 'a' && r <= 'z':
			token.WriteRune(r)
		case r >= '0' && r <= '9':
			token.WriteRune(r)
			hasNum = true
		default:
			flush()
		}
	}
	flush()
	normalized := strings.Join(out, " ")
	if len(normalized) > 200 {
		normalized = normalized[:200]
	}
	return normalized
}
