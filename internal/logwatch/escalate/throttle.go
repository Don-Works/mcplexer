package escalate

import (
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type notifyMark struct {
	at   time.Time
	rank int
}

// templateCooldown is the quiet period the same key must observe at this
// severity. Critical is deliberately tighter than everything else: the cooldown
// exists to stop one noisy template chattering, not to overrule the persistence
// policy's judgement that a critical incident is due another reminder.
func templateCooldown(severity string) time.Duration {
	if store.SeverityRank(severity) >= store.SeverityRank(store.SeverityCritical) {
		return perTemplateCooldownCritical
	}
	return perTemplateCooldown
}

// throttled keeps independent hourly budgets for critical and lower-severity
// traffic. A severity increase for the same template bypasses its cooldown.
func (d *Dispatcher) throttled(workspaceID, templateID, severity string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	rank := store.SeverityRank(severity)
	if templateID != "" {
		last, ok := d.perTemplate[workspaceID+"/"+templateID]
		if ok && now.Sub(last.at) < templateCooldown(severity) && rank <= last.rank {
			return "per-template cooldown"
		}
	}

	recent := d.perHour[workspaceID][:0]
	critical, lower := 0, 0
	for _, mark := range d.perHour[workspaceID] {
		if now.Sub(mark.at) >= time.Hour {
			continue
		}
		recent = append(recent, mark)
		if mark.rank >= store.SeverityRank(store.SeverityCritical) {
			critical++
		} else {
			lower++
		}
	}
	d.perHour[workspaceID] = recent
	if rank >= store.SeverityRank(store.SeverityCritical) {
		if critical >= maxCriticalNotifiesPerHour {
			return "workspace critical notify cap"
		}
		return ""
	}
	if lower >= maxNotifiesPerHour {
		return "workspace hourly notify cap"
	}
	return ""
}

func (d *Dispatcher) recordNotify(workspaceID, templateID, severity string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	mark := notifyMark{at: d.now(), rank: store.SeverityRank(severity)}
	if templateID != "" {
		d.perTemplate[workspaceID+"/"+templateID] = mark
	}
	d.perHour[workspaceID] = append(d.perHour[workspaceID], mark)
	d.evictLapsedTemplates(mark.at)
}

// evictLapsedTemplates prunes against the LONGEST cooldown, so an entry always
// outlives the shorter critical window that may also consult it.
func (d *Dispatcher) evictLapsedTemplates(now time.Time) {
	for key, mark := range d.perTemplate {
		if now.Sub(mark.at) >= perTemplateCooldown {
			delete(d.perTemplate, key)
		}
	}
}
