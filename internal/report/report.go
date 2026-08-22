package report

import (
	"fmt"
	"sort"
	"time"

	"standup/internal/store"
)

// Group is one status band inside a day section. A standup separates what
// was finished from what is next; rendering both as identical bullets with an
// inline [status] tag left them indistinguishable at a glance.
type Group struct {
	Label  string
	Status string
	Tasks  []store.Task
}

// Day is one calendar-day section of a report, split into status groups
// (empty groups are dropped).
type Day struct {
	Heading string
	Groups  []Group
}

// Tasks flattens the day's groups in render order.
func (d Day) Tasks() []store.Task {
	var out []store.Task
	for _, g := range d.Groups {
		out = append(out, g.Tasks...)
	}
	return out
}

// Section is the deterministic projection of tasks into report sections.
// Days holds one entry per covered calendar day, oldest first.
type Section struct {
	Days     []Day
	Blockers []store.Task
}

// Empty reports whether the section has nothing to say.
func (s Section) Empty() bool {
	if len(s.Blockers) > 0 {
		return false
	}
	for _, d := range s.Days {
		if len(d.Groups) > 0 {
			return false
		}
	}
	return true
}

// bands are the day groups in render order: finished work first, then what is
// underway, then what is planned.
var bands = []Group{
	{Label: "Done", Status: "done"},
	{Label: "In progress", Status: "in-progress"},
	{Label: "Next", Status: "todo"},
}

// Build splits tasks into one section per given date (ascending, oldest
// first), applying the meeting-time cutoff to today only. Yesterday's
// unfinished tasks carry over into the last day (after its own tasks);
// blocked tasks move out of the day sections into Blockers. Each day is then
// split into status groups.
func Build(tasks []store.Task, now time.Time, meetingTime string, dates []time.Time) (Section, error) {
	mt, err := time.Parse("15:04", meetingTime)
	if err != nil {
		return Section{}, fmt.Errorf("report: bad meeting time %q: %w", meetingTime, err)
	}
	if err := checkDates(dates); err != nil {
		return Section{}, err
	}
	last := len(dates) - 1
	today := sameDay(dates[last], now)

	var sec Section
	days := make([][]store.Task, len(dates))
	assign(days, tasks, now, meetingCutoff(now, mt), dates, today)
	for i := range days {
		sortByTime(days[i])
	}
	sec.Blockers = collectBlockers(days)
	carryOver(days)

	relative := allRelative(dates, now)
	for i, d := range dates {
		sec.Days = append(sec.Days, Day{Heading: heading(d, now, relative), Groups: group(days[i])})
	}
	return sec, nil
}

func checkDates(dates []time.Time) error {
	if len(dates) == 0 {
		return fmt.Errorf("report: at least one day is required, got %d", len(dates))
	}
	for i := 1; i < len(dates); i++ {
		if !dates[i].After(dates[i-1]) {
			return fmt.Errorf("report: dates must be ascending (got %v then %v)", dates[i-1], dates[i])
		}
	}
	return nil
}

// group splits a day's tasks into the status bands, keeping time order within
// each and dropping the bands the day has no tasks for. An unknown status
// would be unreachable — the store validates every write — so the bands are
// exhaustive by construction.
func group(tasks []store.Task) []Group {
	var out []Group
	for _, band := range bands {
		g := Group{Label: band.Label, Status: band.Status}
		for _, t := range tasks {
			if t.Status == band.Status {
				g.Tasks = append(g.Tasks, t)
			}
		}
		if len(g.Tasks) > 0 {
			out = append(out, g)
		}
	}
	return out
}

// assign buckets tasks into their day section; today's section additionally
// respects the cutoff.
func assign(days [][]store.Task, tasks []store.Task, now, cutoff time.Time, dates []time.Time, today bool) {
	last := len(dates) - 1
	for _, t := range tasks {
		local := t.Timestamp.In(now.Location())
		for i, d := range dates {
			if !sameDay(local, d) {
				continue
			}
			if i == last && today && t.Timestamp.After(cutoff) {
				continue
			}
			days[i] = append(days[i], t)
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

// collectBlockers moves blocked tasks out of the day sections: they are
// reported in Blockers only, not duplicated per day.
func collectBlockers(days [][]store.Task) []store.Task {
	var blockers []store.Task
	for i := range days {
		kept := days[i][:0]
		for _, t := range days[i] {
			if t.Status == "blocked" {
				blockers = append(blockers, t)
				continue
			}
			kept = append(kept, t)
		}
		days[i] = kept
	}
	return blockers
}

// carryOver appends yesterday's unfinished tasks to the last day's section.
func carryOver(days [][]store.Task) {
	if len(days) < 2 {
		return
	}
	var carried []store.Task
	for _, t := range days[len(days)-2] {
		if t.Status != "done" {
			carried = append(carried, t)
		}
	}
	last := len(days) - 1
	days[last] = append(days[last], carried...)
}

// allRelative reports whether every day in the window has a relative name.
// Headings are all relative or all dated, never mixed: a report that read
// "Sat 2026-08-15", "Sun 2026-08-16", "Today" named the same axis two ways,
// and so did the default Monday window ("Fri 2026-08-14", "Today").
func allRelative(dates []time.Time, now time.Time) bool {
	for _, d := range dates {
		if !sameDay(d, now) && !sameDay(d, now.AddDate(0, 0, -1)) {
			return false
		}
	}
	return true
}

// heading names a day, relatively only when the whole window can be named
// that way.
func heading(day, now time.Time, relative bool) string {
	if !relative {
		return day.Format("Mon 2006-01-02")
	}
	if sameDay(day, now) {
		return "Today"
	}
	return "Yesterday"
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
