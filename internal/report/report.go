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

// Build splits tasks into one section per calendar day over the trailing
// `days` days (oldest first), applying the meeting-time cutoff to today only.
// Yesterday's unfinished tasks carry over into Today (after today's own
// tasks); Blockers lists every blocked task from the covered days.
func Build(tasks []store.Task, now time.Time, meetingTime string, days int) (Section, error) {
	if days < 1 {
		return Section{}, fmt.Errorf("report: day count must be >= 1, got %d", days)
	}
	mt, err := time.Parse("15:04", meetingTime)
	if err != nil {
		return Section{}, fmt.Errorf("report: bad meeting time %q: %w", meetingTime, err)
	}
	cutoff := meetingCutoff(now, mt)

	var sec Section
	for i := 0; i < days; i++ {
		sec.Days = append(sec.Days, Day{Heading: heading(now.AddDate(0, 0, -(days-1-i)), now)})
	}
	for _, t := range tasks {
		if i, ok := dayIndex(t, now, days, cutoff); ok {
			sec.Days[i].Tasks = append(sec.Days[i].Tasks, t)
		}
	}
	for i := range sec.Days {
		sortByTime(sec.Days[i].Tasks)
	}
	collectBlockers(&sec)
	carryOver(&sec)
	if days == 2 {
		sec.Yesterday, sec.Today = sec.Days[0].Tasks, sec.Days[1].Tasks
	}
	return sec, nil
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

// dayIndex finds the covered-day slot for a task; today's slot additionally
// respects the cutoff.
func dayIndex(t store.Task, now time.Time, days int, cutoff time.Time) (int, bool) {
	local := t.Timestamp.In(now.Location())
	for i := 0; i < days; i++ {
		day := now.AddDate(0, 0, -(days - 1 - i))
		if !sameDay(local, day) {
			continue
		}
		if i == days-1 && !t.Timestamp.Before(cutoff) {
			return 0, false
		}
		return i, true
	}
	return 0, false
}

func collectBlockers(sec *Section) {
	for i := range sec.Days {
		for _, t := range sec.Days[i].Tasks {
			if t.Status == "blocked" {
				sec.Blockers = append(sec.Blockers, t)
			}
		}
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

func heading(day, now time.Time) string {
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
