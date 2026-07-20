// monitoring_expected_signal_schedule.go — active-hours arithmetic for
// expected-signal rules. Split out of monitoring_expected_signal_eval.go to
// keep both files inside the 300-line gate.
//
// All arithmetic happens in the RULE'S location rather than UTC, so a DST shift
// moves an alert boundary with the operator's wall clock instead of against it.
// A learned rule is always-on, so none of this runs for one; it exists for
// hand-authored business-hours rules, where getting the boundary wrong is how a
// rule fires at 3am and gets the channel muted.
package store

import "time"

const maxActiveWalkbackDays = 8

// activePeriodStart returns the start of the contiguous active period
// containing now, and whether now is active at all. All arithmetic happens in
// the rule's location, so DST shifts move the boundary with the operator's
// wall clock rather than against it.
func activePeriodStart(
	rule MonitoringExpectedSignal, now time.Time, loc *time.Location,
) (time.Time, bool) {
	local := now.In(loc)
	start, end := rule.ActiveStartMinute, rule.ActiveEndMinute
	allDay := start == end || (start == 0 && end >= expectedSignalDayMinutes)
	if allDay {
		return allDayPeriodStart(rule, local, loc)
	}
	minute := local.Hour()*60 + local.Minute()
	if start < end {
		if !dayActive(rule, local.Weekday()) || minute < start || minute >= end {
			return time.Time{}, false
		}
		return atLocalMinute(local, start, loc), true
	}
	// Wrapping window (e.g. 22:00-06:00): the period may have begun yesterday.
	if minute >= start {
		if !dayActive(rule, local.Weekday()) {
			return time.Time{}, false
		}
		return atLocalMinute(local, start, loc), true
	}
	if minute < end {
		yesterday := local.AddDate(0, 0, -1)
		if !dayActive(rule, yesterday.Weekday()) {
			return time.Time{}, false
		}
		return atLocalMinute(yesterday, start, loc), true
	}
	return time.Time{}, false
}

// allDayPeriodStart walks back over consecutive active days so a Mon-Fri 24h
// rule treats the whole working week as one period rather than restarting its
// window every midnight.
func allDayPeriodStart(
	rule MonitoringExpectedSignal, local time.Time, loc *time.Location,
) (time.Time, bool) {
	if !dayActive(rule, local.Weekday()) {
		return time.Time{}, false
	}
	day := local
	for i := 0; i < maxActiveWalkbackDays; i++ {
		previous := day.AddDate(0, 0, -1)
		if !dayActive(rule, previous.Weekday()) {
			break
		}
		day = previous
	}
	return atLocalMinute(day, 0, loc), true
}

func dayActive(rule MonitoringExpectedSignal, day time.Weekday) bool {
	return rule.ActiveDaysMask&(1<<uint(day)) != 0
}

// atLocalMinute builds local midnight + minute in loc, then normalises to UTC.
func atLocalMinute(local time.Time, minute int, loc *time.Location) time.Time {
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return midnight.Add(time.Duration(minute) * time.Minute).UTC()
}
