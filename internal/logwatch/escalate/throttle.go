package escalate

import (
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type notifyMark struct {
	at   time.Time
	rank int
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
		if ok && now.Sub(last.at) < perTemplateCooldown && rank <= last.rank {
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

func (d *Dispatcher) evictLapsedTemplates(now time.Time) {
	for key, mark := range d.perTemplate {
		if now.Sub(mark.at) >= perTemplateCooldown {
			delete(d.perTemplate, key)
		}
	}
}
