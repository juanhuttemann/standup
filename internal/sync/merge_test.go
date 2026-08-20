package sync

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"standup/internal/store"
)

var (
	t0 = time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Hour)
	t2 = t0.Add(2 * time.Hour)
	t3 = t0.Add(3 * time.Hour)
)

func task(id, text string, ts time.Time) store.Task {
	return store.Task{ID: id, Text: text, Status: "todo", Timestamp: ts, Updated: ts}
}

func ids(tasks []store.Task) []string {
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.ID)
	}
	return out
}

func TestMergeEmpty(t *testing.T) {
	res := Merge(nil, nil)
	assert.Empty(t, res.Merged)
	assert.Empty(t, res.Push)
	assert.Zero(t, res.Pulled)
	assert.Zero(t, res.Resolved)
}

func TestMergeLocalOnly(t *testing.T) {
	l := task("aa1", "local task", t0)
	res := Merge([]store.Task{l}, nil)
	assert.Equal(t, []store.Task{l}, res.Merged)
	assert.Equal(t, []store.Task{l}, res.Push, "local-only records are pushed")
	assert.Zero(t, res.Pulled)
}

func TestMergeRemoteOnly(t *testing.T) {
	r := task("bb2", "remote task", t0)
	res := Merge(nil, []store.Task{r})
	assert.Equal(t, []store.Task{r}, res.Merged)
	assert.Empty(t, res.Push)
	assert.Equal(t, 1, res.Pulled)
}

func TestMergeIdentical(t *testing.T) {
	l := task("aa1", "same", t0)
	r := task("aa1", "same", t0)
	res := Merge([]store.Task{l}, []store.Task{r})
	require.Len(t, res.Merged, 1)
	assert.Empty(t, res.Push, "identical records are not pushed")
	assert.Zero(t, res.Pulled, "identical records are not pulled")
}

func TestMergeLocalNewerWins(t *testing.T) {
	l := task("aa1", "edited locally", t0)
	l.Updated = t2
	r := task("aa1", "original", t0)
	r.Updated = t1
	res := Merge([]store.Task{l}, []store.Task{r})
	require.Len(t, res.Merged, 1)
	assert.Equal(t, "edited locally", res.Merged[0].Text)
	assert.Equal(t, []store.Task{l}, res.Push)
	assert.Zero(t, res.Pulled)
}

func TestMergeRemoteNewerWins(t *testing.T) {
	l := task("aa1", "original", t0)
	l.Updated = t1
	r := task("aa1", "edited remotely", t0)
	r.Updated = t2
	res := Merge([]store.Task{l}, []store.Task{r})
	require.Len(t, res.Merged, 1)
	assert.Equal(t, "edited remotely", res.Merged[0].Text)
	assert.Empty(t, res.Push)
	assert.Equal(t, 1, res.Pulled)
}

func TestMergeTieBreaksToRemote(t *testing.T) {
	l := task("aa1", "local wording", t0)
	l.Updated = t1
	r := task("aa1", "remote wording", t0)
	r.Updated = t1
	res := Merge([]store.Task{l}, []store.Task{r})
	require.Len(t, res.Merged, 1)
	assert.Equal(t, "remote wording", res.Merged[0].Text, "documented tie-break: remote wins")
	assert.Equal(t, 1, res.Pulled)
}

func TestMergeTombstoneBeatsOlderLive(t *testing.T) {
	l := task("aa1", "live", t0)
	l.Updated = t1
	r := task("aa1", "deleted elsewhere", t0)
	r.Updated = t1
	r.Deleted = t2
	res := Merge([]store.Task{l}, []store.Task{r})
	require.Len(t, res.Merged, 1)
	assert.True(t, res.Merged[0].Deleted.Equal(t2), "the newer tombstone wins")
	assert.Empty(t, res.Push)
	assert.Equal(t, 1, res.Pulled)
}

func TestMergeLiveBeatsOlderTombstone(t *testing.T) {
	l := task("aa1", "edited after the delete", t0)
	l.Updated = t2
	r := task("aa1", "deleted elsewhere", t0)
	r.Deleted = t1
	res := Merge([]store.Task{l}, []store.Task{r})
	require.Len(t, res.Merged, 1)
	assert.True(t, res.Merged[0].Deleted.IsZero(), "a newer edit resurrects")
	assert.Equal(t, []store.Task{l}, res.Push)
}

func TestMergeTombstoneNewerThanEdit(t *testing.T) {
	l := task("aa1", "deleted locally", t0)
	l.Updated = t1
	l.Deleted = t3
	r := task("aa1", "edited remotely", t0)
	r.Updated = t2
	res := Merge([]store.Task{l}, []store.Task{r})
	require.Len(t, res.Merged, 1)
	assert.True(t, res.Merged[0].Deleted.Equal(t3), "event time is max(Updated, Deleted)")
	assert.Equal(t, []store.Task{l}, res.Push)
	assert.Zero(t, res.Pulled)
}

func TestMergeParallelImportDedupe(t *testing.T) {
	// The same commit imported on two machines: different ids, same text|day.
	l := task("aa1", "fix: login bug", t0)
	l.Updated = t1
	r := task("bb2", "fix: login bug", t0)
	r.Updated = t2
	res := Merge([]store.Task{l}, []store.Task{r})
	require.Len(t, res.Merged, 2, "both records stay on file")
	assert.Equal(t, 1, res.Resolved)

	var live, dead []store.Task
	for _, m := range res.Merged {
		if m.Deleted.IsZero() {
			live = append(live, m)
		} else {
			dead = append(dead, m)
		}
	}
	require.Len(t, live, 1)
	assert.Equal(t, "aa1", live[0].ID, "the earliest import survives")
	require.Len(t, dead, 1)
	assert.Equal(t, "bb2", dead[0].ID)
	assert.True(t, dead[0].Deleted.Equal(t2), "the duplicate is tombstoned at its own event time")
	assert.Contains(t, ids(res.Push), "bb2", "the tombstoned duplicate is pushed so the remote converges")
}

func TestMergeDedupeTieBreaksToLowestID(t *testing.T) {
	l := task("bb2", "same commit", t0)
	r := task("aa1", "same commit", t0)
	res := Merge([]store.Task{l}, []store.Task{r})
	assert.Equal(t, 1, res.Resolved)
	for _, m := range res.Merged {
		if m.Deleted.IsZero() {
			assert.Equal(t, "aa1", m.ID, "equal event times: lowest id survives")
		}
	}
}

func TestMergeDedupeIgnoresTombstones(t *testing.T) {
	l := task("aa1", "same text", t0)
	l.Deleted = t1
	r := task("bb2", "same text", t0)
	res := Merge([]store.Task{l}, []store.Task{r})
	assert.Zero(t, res.Resolved, "tombstones are not deduped")
	assert.Len(t, res.Merged, 2)
}

func TestMergeSortedOutput(t *testing.T) {
	a := task("aa1", "third", t2)
	b := task("bb2", "first", t0)
	c := task("cc3", "second", t1)
	res := Merge([]store.Task{a, b}, []store.Task{c})
	assert.Equal(t, []string{"bb2", "cc3", "aa1"}, ids(res.Merged), "merged is sorted by timestamp")
	assert.Equal(t, []string{"aa1", "bb2"}, ids(res.Push), "push is sorted by id")
	assert.Equal(t, 1, res.Pulled)
}

func TestMergeRemoteOnlyTombstonePropagates(t *testing.T) {
	r := task("bb2", "deleted on another machine", t0)
	r.Deleted = t1
	res := Merge(nil, []store.Task{r})
	require.Len(t, res.Merged, 1)
	assert.True(t, res.Merged[0].Deleted.Equal(t1))
	assert.Equal(t, 1, res.Pulled)
	assert.Empty(t, res.Push)
}
