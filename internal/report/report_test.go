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

func d(h, m int, sec int) time.Time {
	return time.Date(2026, 8, 15, h, m, sec, 0, loc)
}

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
			// unfinished yesterday tasks carry over after today's own tasks
			todayID: []string{
				time.Date(2026, 8, 14, 9, 0, 0, 0, loc).Format(time.RFC3339Nano),
				time.Date(2026, 8, 14, 9, 30, 0, 0, loc).Format(time.RFC3339Nano),
				time.Date(2026, 8, 13, 16, 0, 0, 0, loc).Format(time.RFC3339Nano),
				time.Date(2026, 8, 13, 17, 0, 0, 0, loc).Format(time.RFC3339Nano),
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
			sec, err := Build(tt.tasks, tt.now, "09:30", Trailing(tt.now, 2))
			require.NoError(t, err)
			assert.Equal(t, tt.yesterdayID, ids(sec.Yesterday))
			assert.Equal(t, tt.todayID, ids(sec.Today))
			assert.Equal(t, tt.yesterdayID, ids(sec.Days[0].Tasks), "Days[0] mirrors Yesterday")
			assert.Equal(t, tt.todayID, ids(sec.Days[1].Tasks), "Days[1] mirrors Today")
		})
	}
}

func TestBuildCarryOver(t *testing.T) {
	now := d(10, 0, 0)
	unfinished := task(time.Date(2026, 8, 14, 16, 0, 0, 0, loc), "in-progress")
	finished := task(time.Date(2026, 8, 14, 17, 0, 0, 0, loc), "done")
	blocked := task(time.Date(2026, 8, 14, 18, 0, 0, 0, loc), "blocked")
	today1 := task(time.Date(2026, 8, 15, 8, 0, 0, 0, loc), "todo")
	today2 := task(time.Date(2026, 8, 15, 9, 0, 0, 0, loc), "todo")

	sec, err := Build([]store.Task{today2, unfinished, today1, finished, blocked}, now, "09:30", Trailing(now, 2))
	require.NoError(t, err)

	// Today keeps its own order ahead of carried tasks; only unfinished ones carry.
	assert.Equal(t, []string{today1.ID, today2.ID, unfinished.ID}, ids(sec.Today))
	assert.Equal(t, []string{unfinished.ID, finished.ID}, ids(sec.Yesterday), "blocked task moved out of Yesterday")
	assert.Empty(t, sec.Blockers[1:], "blockers listed once")
	assert.Equal(t, []string{blocked.ID}, ids(sec.Blockers), "blocked task lives in Blockers only")
}

func TestBuildCarryOverBlockedUntilResolved(t *testing.T) {
	now := d(9, 0, 0)
	blocked := task(time.Date(2026, 8, 14, 16, 0, 0, 0, loc), "blocked")
	sec, err := Build([]store.Task{blocked}, now, "09:30", Trailing(now, 2))
	require.NoError(t, err)
	assert.Equal(t, []string{blocked.ID}, ids(sec.Blockers), "blocked task still reported next day")
	assert.Empty(t, sec.Today, "blocked task is not duplicated into day sections")
	assert.Empty(t, sec.Yesterday)

	resolved := blocked
	resolved.Status = "done"
	sec, err = Build([]store.Task{resolved}, now, "09:30", Trailing(now, 2))
	require.NoError(t, err)
	assert.Empty(t, sec.Blockers, "resolved blocker disappears from blockers")
	assert.Empty(t, sec.Today, "done yesterday task does not carry")
}

func TestBuildRange(t *testing.T) {
	now := d(9, 0, 0)
	older := task(time.Date(2026, 8, 13, 10, 0, 0, 0, loc), "done")
	old := task(time.Date(2026, 8, 14, 10, 0, 0, 0, loc), "done")
	beforeCutoff := task(time.Date(2026, 8, 15, 8, 0, 0, 0, loc), "done")
	afterCutoff := task(time.Date(2026, 8, 15, 9, 45, 0, 0, loc), "todo")
	outside := task(time.Date(2026, 8, 12, 10, 0, 0, 0, loc), "done")

	sec, err := Build([]store.Task{afterCutoff, outside, older, beforeCutoff, old}, now, "09:30", Trailing(now, 3))
	require.NoError(t, err)

	require.Len(t, sec.Days, 3, "one section per day")
	assert.Equal(t, []string{"Thu 2026-08-13", "Yesterday", "Today"}, headings(sec))
	assert.Equal(t, []string{older.ID}, ids(sec.Days[0].Tasks))
	assert.Equal(t, []string{old.ID}, ids(sec.Days[1].Tasks))
	assert.Equal(t, []string{beforeCutoff.ID}, ids(sec.Days[2].Tasks), "cutoff still trims today only")
	assert.Nil(t, sec.Yesterday, "compat fields only set for the default window")
	assert.Nil(t, sec.Today)
}

func TestBuildRangeSingleDay(t *testing.T) {
	now := d(10, 0, 0)
	t1 := task(time.Date(2026, 8, 15, 8, 0, 0, 0, loc), "todo")
	y1 := task(time.Date(2026, 8, 14, 8, 0, 0, 0, loc), "in-progress")
	sec, err := Build([]store.Task{t1, y1}, now, "09:30", Trailing(now, 1))
	require.NoError(t, err)
	require.Len(t, sec.Days, 1)
	assert.Equal(t, []string{"Today"}, headings(sec))
	assert.Equal(t, []string{t1.ID}, ids(sec.Days[0].Tasks), "single day covers today only, no carry")
}

func TestBuildIncludesTaskExactlyAtMeetingCutoff(t *testing.T) {
	now := d(9, 0, 0)
	atCutoff := task(time.Date(2026, 8, 15, 9, 30, 0, 0, loc), "todo")

	sec, err := Build([]store.Task{atCutoff}, now, "09:30", Trailing(now, 1))
	require.NoError(t, err)
	assert.Equal(t, []string{atCutoff.ID}, ids(sec.Days[0].Tasks))
}

func TestBuildWeekendDefaultWindow(t *testing.T) {
	now := time.Date(2026, 8, 17, 9, 0, 0, 0, loc) // Monday
	fri := task(time.Date(2026, 8, 14, 16, 0, 0, 0, loc), "done")
	sat := task(time.Date(2026, 8, 15, 11, 0, 0, 0, loc), "done")
	mon := task(time.Date(2026, 8, 17, 8, 0, 0, 0, loc), "todo")

	sec, err := Build([]store.Task{sat, fri, mon}, now, "09:30", DefaultWindow(now))
	require.NoError(t, err)
	require.Len(t, sec.Days, 2, "Friday + Monday, weekend skipped")
	assert.Equal(t, []string{fri.ID}, ids(sec.Yesterday), "Friday is Monday's yesterday")
	assert.Equal(t, []string{mon.ID}, ids(sec.Today))
	assert.Equal(t, []string{fri.ID}, ids(sec.Days[0].Tasks))
	assert.Equal(t, []string{mon.ID}, ids(sec.Days[1].Tasks))
}

func TestBuildExplicitWindowKeepsDatedHeadings(t *testing.T) {
	now := d(9, 0, 0)
	a := task(time.Date(2026, 8, 10, 10, 0, 0, 0, loc), "done")
	b := task(time.Date(2026, 8, 11, 10, 0, 0, 0, loc), "done")

	sec, err := Build([]store.Task{a, b}, now, "09:30", []time.Time{
		StartOfDay(time.Date(2026, 8, 10, 0, 0, 0, 0, loc)),
		StartOfDay(time.Date(2026, 8, 11, 0, 0, 0, 0, loc)),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"Mon 2026-08-10", "Tue 2026-08-11"}, headings(sec),
		"historical windows get dated headings, not Yesterday/Today")
	assert.Nil(t, sec.Yesterday)
	assert.Nil(t, sec.Today)
	assert.Equal(t, []string{b.ID}, ids(sec.Days[1].Tasks), "no cutoff on historical days")
}

func TestBuildBadInputs(t *testing.T) {
	_, err := Build(nil, time.Now(), "09:30", nil)
	assert.Error(t, err, "empty date list must fail")
	_, err = Build(nil, time.Now(), "09:30", Trailing(time.Now(), 0))
	assert.Error(t, err, "zero days must fail")
	d1 := StartOfDay(time.Now())
	_, err = Build(nil, time.Now(), "09:30", []time.Time{d1, d1})
	assert.Error(t, err, "non-ascending dates must fail")
	for _, bad := range []string{"25:99", "abc", ""} {
		_, err = Build(nil, time.Now(), bad, Trailing(time.Now(), 2))
		assert.Error(t, err, "meeting time %q must fail", bad)
	}
}

func TestTrailing(t *testing.T) {
	now := d(9, 0, 0)
	assert.Nil(t, Trailing(now, 0))
	got := Trailing(now, 3)
	require.Len(t, got, 3)
	assert.Equal(t, "2026-08-13", got[0].Format("2006-01-02"))
	assert.Equal(t, "2026-08-15", got[2].Format("2006-01-02"))
	for _, day := range got {
		assert.Equal(t, 0, day.Hour()+day.Minute()+day.Second())
	}
}

func TestDefaultWindow(t *testing.T) {
	tests := []struct {
		now  time.Time
		want []string
	}{
		{time.Date(2026, 8, 17, 9, 0, 0, 0, loc), []string{"2026-08-14", "2026-08-17"}}, // Monday -> Friday
		{time.Date(2026, 8, 18, 9, 0, 0, 0, loc), []string{"2026-08-17", "2026-08-18"}}, // Tuesday -> Monday
	}
	for _, tt := range tests {
		got := DefaultWindow(tt.now)
		require.Len(t, got, 2)
		assert.Equal(t, tt.want, []string{got[0].Format("2006-01-02"), got[1].Format("2006-01-02")})
	}
}

func TestStartOfDay(t *testing.T) {
	in := time.Date(2026, 8, 15, 14, 59, 3, 0, loc)
	want := time.Date(2026, 8, 15, 0, 0, 0, 0, loc)
	assert.True(t, StartOfDay(in).Equal(want))
}

func TestLastWorkingDay(t *testing.T) {
	tests := []struct {
		now  time.Time
		want string
	}{
		{time.Date(2026, 8, 18, 9, 0, 0, 0, loc), "2026-08-17"}, // Tuesday -> Monday
		{time.Date(2026, 8, 17, 9, 0, 0, 0, loc), "2026-08-14"}, // Monday -> Friday
		{time.Date(2026, 8, 15, 9, 0, 0, 0, loc), "2026-08-14"}, // Saturday -> Friday
		{time.Date(2026, 8, 16, 9, 0, 0, 0, loc), "2026-08-14"}, // Sunday -> Friday
	}
	for _, tt := range tests {
		got := LastWorkingDay(tt.now)
		assert.Equal(t, tt.want, got.Format("2006-01-02"))
	}
}

func ids(ts []store.Task) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

func headings(sec Section) []string {
	out := make([]string, len(sec.Days))
	for i, d := range sec.Days {
		out[i] = d.Heading
	}
	return out
}

// The cutoff bounds today's section at the meeting, then at now once the
// meeting has passed. Running late in the day therefore looks identical for
// every meeting_time — by design, not because the setting is dead.
func TestMeetingTimeOnlyBitesBeforeTheMeeting(t *testing.T) {
	late := d(23, 54, 0)
	lateTask := task(d(23, 54, 0), "todo")
	for _, meeting := range []string{"00:01", "09:30", "23:00", "23:59"} {
		sec, err := Build([]store.Task{lateTask}, late, meeting, Trailing(late, 1))
		require.NoError(t, err, meeting)
		assert.Equal(t, []string{lateTask.ID}, ids(sec.Days[0].Tasks),
			"after the meeting the day is reported up to now (meeting_time=%s)", meeting)
	}

	early := d(8, 0, 0)
	afterMeeting := task(d(9, 45, 0), "todo")
	sec, err := Build([]store.Task{afterMeeting}, early, "09:30", Trailing(early, 1))
	require.NoError(t, err)
	assert.Empty(t, ids(sec.Days[0].Tasks), "work logged past the meeting waits for the next standup")

	sec, err = Build([]store.Task{afterMeeting}, early, "23:59", Trailing(early, 1))
	require.NoError(t, err)
	assert.Equal(t, []string{afterMeeting.ID}, ids(sec.Days[0].Tasks), "a later meeting includes it")
}
