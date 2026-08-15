package report

import (
	"fmt"
	"sort"
	"time"

	"standup/internal/store"
)

// Day is one calendar-day section of a report.
type Day struct {
	Heading string
	Tasks   []store.Task
}

// Section is the deterministic projection of tasks into report sections.
// Days holds one entry per covered calendar day, oldest first. Yesterday and
// Today exist so the two-section generate template keeps working unchanged.
type Section struct {
	Days      []Day
	Blockers  []store.Task
	Yesterday []store.Task
	Today     []store.Task
}

// Build splits tasks into one section per given date (ascending, oldest
// first), applying the meeting-time cutoff to today only. Yesterday's
// unfinished tasks carry over into the last day (after its own tasks);
// blocked tasks move out of the day sections into Blockers. The Yesterday/
// Today compat fields are set only for the default window (last working day
// plus today), so explicit ranges keep dated headings.
func Build(tasks []store.Task, now time.Time, meetingTime string, dates []time.Time) (Section, error) {
	mt, err := time.Parse("15:04", meetingTime)
	if err != nil {
		return Section{}, fmt.Errorf("report: bad meeting time %q: %w", meetingTime, err)
	}
	if len(dates) == 0 {
		return Section{}, fmt.Errorf("report: at least one day is required, got %d", len(dates))
	}
	for i := 1; i < len(dates); i++ {
		if !dates[i].After(dates[i-1]) {
			return Section{}, fmt.Errorf("report: dates must be ascending (got %v then %v)", dates[i-1], dates[i])
		}
	}
	last := len(dates) - 1
	today := sameDay(dates[last], now)

	var sec Section
	for _, d := range dates {
		sec.Days = append(sec.Days, Day{Heading: heading(d, now, today)})
	}
	assign(&sec, tasks, now, meetingCutoff(now, mt), dates, today)
	for i := range sec.Days {
		sortByTime(sec.Days[i].Tasks)
	}
	collectBlockers(&sec)
	carryOver(&sec)
	if w := DefaultWindow(now); len(dates) == 2 && sameDay(dates[0], w[0]) && sameDay(dates[1], w[1]) {
		sec.Yesterday, sec.Today = sec.Days[0].Tasks, sec.Days[1].Tasks
	}
	return sec, nil
}

// assign buckets tasks into their day section; today's section additionally
// respects the cutoff.
func assign(sec *Section, tasks []store.Task, now, cutoff time.Time, dates []time.Time, today bool) {
	last := len(dates) - 1
	for _, t := range tasks {
		local := t.Timestamp.In(now.Location())
		for i, d := range dates {
			if !sameDay(local, d) {
				continue
			}
			if i == last && today && !t.Timestamp.Before(cutoff) {
				continue
			}
			sec.Days[i].Tasks = append(sec.Days[i].Tasks, t)
			break
		}
	}
}

// meetingCutoff is the latest timestamp today's section includes: the meeting
// time, or now once the meeting time has passed.
func meetingCutoff(now, mt time.Time) time.Time {
	cutoff := time.Date(now.Year(), now.Month(), now.Day(), mt.Hour(), mt.Minute(), 0, 0, now.Location())
	if cutoff.Before(now) {
		return now
	}
	return cutoff
}

// collectBlockers moves blocked tasks out of the day sections into Blockers:
// they are reported there only, not duplicated per day.
func collectBlockers(sec *Section) {
	for i := range sec.Days {
		kept := sec.Days[i].Tasks[:0]
		for _, t := range sec.Days[i].Tasks {
			if t.Status == "blocked" {
				sec.Blockers = append(sec.Blockers, t)
				continue
			}
			kept = append(kept, t)
		}
		sec.Days[i].Tasks = kept
	}
}

// carryOver appends yesterday's unfinished tasks to Today's section.
func carryOver(sec *Section) {
	if len(sec.Days) < 2 {
		return
	}
	var carried []store.Task
	for _, t := range sec.Days[len(sec.Days)-2].Tasks {
		if t.Status != "done" {
			carried = append(carried, t)
		}
	}
	last := len(sec.Days) - 1
	sec.Days[last].Tasks = append(sec.Days[last].Tasks, carried...)
}

// heading names a day relative to now; windows that do not end today are
// fully historical and get dated headings throughout.
func heading(day, now time.Time, windowEndsToday bool) string {
	if !windowEndsToday {
		return day.Format("Mon 2006-01-02")
	}
	switch {
	case sameDay(day, now):
		return "Today"
	case sameDay(day, now.AddDate(0, 0, -1)):
		return "Yesterday"
	default:
		return day.Format("Mon 2006-01-02")
	}
}

// StartOfDay returns midnight of t's calendar day in t's location.
func StartOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// Trailing returns the start-of-day timestamps of the n calendar days ending
// today (ascending).
func Trailing(now time.Time, n int) []time.Time {
	if n < 1 {
		return nil
	}
	days := make([]time.Time, n)
	for i := range days {
		days[i] = StartOfDay(now.AddDate(0, 0, -(n - 1 - i)))
	}
	return days
}

// DefaultWindow is the default report window: the last working day plus
// today — Monday reports cover Friday onward, matching the commits lookback.
func DefaultWindow(now time.Time) []time.Time {
	return []time.Time{StartOfDay(LastWorkingDay(now)), StartOfDay(now)}
}

// LastWorkingDay returns the most recent weekday strictly before now.
func LastWorkingDay(now time.Time) time.Time {
	d := now.AddDate(0, 0, -1)
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

func sortByTime(ts []store.Task) {
	sort.Slice(ts, func(i, j int) bool { return ts[i].Timestamp.Before(ts[j].Timestamp) })
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
