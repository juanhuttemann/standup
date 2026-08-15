package report

import (
	"fmt"
	"sort"
	"time"

	"standup/internal/store"
)

type Section struct {
	Yesterday []store.Task
	Today     []store.Task
}

func Build(tasks []store.Task, now time.Time, meetingTime string) (Section, error) {
	mt, err := time.Parse("15:04", meetingTime)
	if err != nil {
		return Section{}, fmt.Errorf("report: bad meeting time %q: %w", meetingTime, err)
	}
	cutoff := time.Date(now.Year(), now.Month(), now.Day(), mt.Hour(), mt.Minute(), 0, 0, now.Location())
	if cutoff.Before(now) {
		cutoff = now
	}

	var sec Section
	for _, t := range tasks {
		local := t.Timestamp.In(now.Location())
		switch {
		case sameDay(local, now.AddDate(0, 0, -1)):
			sec.Yesterday = append(sec.Yesterday, t)
		case sameDay(local, now) && t.Timestamp.Before(cutoff):
			sec.Today = append(sec.Today, t)
		}
	}
	sortByTime(sec.Yesterday)
	sortByTime(sec.Today)
	return sec, nil
}

func sortByTime(ts []store.Task) {
	sort.Slice(ts, func(i, j int) bool { return ts[i].Timestamp.Before(ts[j].Timestamp) })
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
