package report

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"standup/internal/store"
)

func task(ts time.Time, status string) store.Task {
	return store.Task{ID: ts.Format(time.RFC3339Nano), Text: ts.Format(time.RFC3339Nano), Status: status, Timestamp: ts}
}

var loc = time.UTC

func TestBuild(t *testing.T) {
	tests := []struct {
		name        string
		now         time.Time
		tasks       []store.Task
		yesterdayID []string
		todayID     []string
	}{
		{
			name: "run after meeting shows whole day so far",
			now:  time.Date(2026, 8, 14, 10, 43, 0, 0, loc),
			tasks: []store.Task{
				task(time.Date(2026, 8, 14, 8, 0, 0, 0, loc), "done"),
				task(time.Date(2026, 8, 14, 10, 42, 0, 0, loc), "in-progress"),
				task(time.Date(2026, 8, 14, 11, 30, 0, 0, loc), "done"), // future: out
			},
			yesterdayID: []string{},
			todayID: []string{
				time.Date(2026, 8, 14, 8, 0, 0, 0, loc).Format(time.RFC3339Nano),
				time.Date(2026, 8, 14, 10, 42, 0, 0, loc).Format(time.RFC3339Nano),
			},
		},
		{
			name: "pre-meeting run stops at cutoff",
			now:  time.Date(2026, 8, 14, 8, 0, 0, 0, loc),
			tasks: []store.Task{
				task(time.Date(2026, 8, 14, 7, 0, 0, 0, loc), "done"),         // before cutoff: in
				task(time.Date(2026, 8, 14, 9, 45, 0, 0, loc), "in-progress"), // after cutoff: out
			},
			yesterdayID: []string{},
			todayID:     []string{time.Date(2026, 8, 14, 7, 0, 0, 0, loc).Format(time.RFC3339Nano)},
		},
		{
			name: "all statuses reportable",
			now:  time.Date(2026, 8, 14, 10, 30, 0, 0, loc),
			tasks: []store.Task{
				task(time.Date(2026, 8, 13, 16, 0, 0, 0, loc), "in-progress"),
				task(time.Date(2026, 8, 13, 17, 0, 0, 0, loc), "todo"),
				task(time.Date(2026, 8, 13, 18, 0, 0, 0, loc), "done"),
				task(time.Date(2026, 8, 14, 9, 0, 0, 0, loc), "in-progress"),
				task(time.Date(2026, 8, 14, 9, 30, 0, 0, loc), "todo"),
			},
			yesterdayID: []string{
				time.Date(2026, 8, 13, 16, 0, 0, 0, loc).Format(time.RFC3339Nano),
				time.Date(2026, 8, 13, 17, 0, 0, 0, loc).Format(time.RFC3339Nano),
				time.Date(2026, 8, 13, 18, 0, 0, 0, loc).Format(time.RFC3339Nano),
			},
			todayID: []string{
				time.Date(2026, 8, 14, 9, 0, 0, 0, loc).Format(time.RFC3339Nano),
				time.Date(2026, 8, 14, 9, 30, 0, 0, loc).Format(time.RFC3339Nano),
			},
		},
		{
			name: "sorted regardless of input order",
			now:  time.Date(2026, 8, 14, 10, 30, 0, 0, loc),
			tasks: []store.Task{
				task(time.Date(2026, 8, 14, 9, 0, 0, 0, loc), "done"),
				task(time.Date(2026, 8, 13, 17, 0, 0, 0, loc), "done"),
				task(time.Date(2026, 8, 14, 8, 0, 0, 0, loc), "done"),
				task(time.Date(2026, 8, 13, 15, 0, 0, 0, loc), "done"),
			},
			yesterdayID: []string{
				time.Date(2026, 8, 13, 15, 0, 0, 0, loc).Format(time.RFC3339Nano),
				time.Date(2026, 8, 13, 17, 0, 0, 0, loc).Format(time.RFC3339Nano),
			},
			todayID: []string{
				time.Date(2026, 8, 14, 8, 0, 0, 0, loc).Format(time.RFC3339Nano),
				time.Date(2026, 8, 14, 9, 0, 0, 0, loc).Format(time.RFC3339Nano),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sec, err := Build(tt.tasks, tt.now, "09:30")
			require.NoError(t, err)
			assert.Equal(t, tt.yesterdayID, ids(sec.Yesterday))
			assert.Equal(t, tt.todayID, ids(sec.Today))
		})
	}
}

func ids(ts []store.Task) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

func TestBuildBadMeetingTime(t *testing.T) {
	for _, bad := range []string{"25:99", "abc", ""} {
		_, err := Build(nil, time.Now(), bad)
		assert.Error(t, err, "meeting time %q must fail", bad)
	}
}
