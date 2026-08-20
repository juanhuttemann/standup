package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func seedStore(t *testing.T, ids ...string) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	var b strings.Builder
	for i, id := range ids {
		line, err := json.Marshal(Task{ID: id, Text: fmt.Sprintf("task %d", i), Status: "todo", Timestamp: time.Date(2026, 8, 15, 9, i, 0, 0, time.UTC)})
		require.NoError(t, err)
		b.Write(line)
		b.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(s.Path, []byte(b.String()), 0o644))
	return s
}

func TestFindByPrefix(t *testing.T) {
	s := seedStore(t, "aa111111-1111-1111-1111-111111111111", "ab222222-2222-2222-2222-222222222222", "bb333333-3333-3333-3333-333333333333")

	got, err := s.FindByPrefix("aa1")
	require.NoError(t, err)
	assert.Equal(t, "aa111111-1111-1111-1111-111111111111", got.ID)

	got, err = s.FindByPrefix("ab222222-2222-2222-2222-222222222222")
	require.NoError(t, err)
	assert.Equal(t, "ab222222-2222-2222-2222-222222222222", got.ID)

	_, err = s.FindByPrefix("a")
	assert.ErrorContains(t, err, "ambiguous")

	_, err = s.FindByPrefix("zz")
	assert.ErrorContains(t, err, "no task")
}

func TestAddListRoundtrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "tasks.jsonl"))
	require.NoError(t, err)

	base := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	s.Now = fixedClock(base)

	t1, err := s.Add("first")
	require.NoError(t, err)
	assert.Equal(t, "todo", t1.Status)
	assert.Equal(t, "first", t1.Text)
	assert.NotEmpty(t, t1.ID)
	assert.True(t, t1.Timestamp.Equal(base))

	t2, err := s.Add("second")
	require.NoError(t, err)
	s.Now = fixedClock(base.Add(time.Hour))
	t3, err := s.Add("third")
	require.NoError(t, err)

	got, err := s.List()
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []Task{t1, t2, t3}, got, "ordered by timestamp ascending")
}

func TestAddAtStampsGivenTime(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	s.Now = fixedClock(time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC))

	at := time.Date(2026, 8, 14, 16, 42, 0, 0, time.UTC)
	got, err := s.AddAt("imported commit", "done", at)
	require.NoError(t, err)
	assert.True(t, got.Timestamp.Equal(at), "explicit timestamp wins over the clock")
	assert.Equal(t, "done", got.Status)

	_, err = s.AddAt("bad status", "bogus", at)
	assert.ErrorContains(t, err, "invalid status")
	_, err = s.AddAt("  ", "done", at)
	assert.ErrorContains(t, err, "empty task text")
}

func TestAddEmptyText(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	_, err = s.Add("   ")
	assert.Error(t, err)
}

func TestJSONLFormatOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.jsonl")
	s, err := Open(path)
	require.NoError(t, err)
	s.Now = fixedClock(time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC))

	tk, err := s.Add("write tests")
	require.NoError(t, err)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	require.Len(t, lines, 1, "one object per line")

	var obj map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &obj))
	assert.ElementsMatch(t, []string{"id", "task", "status", "timestamp", "updated"}, keysOf(obj))
	assert.Equal(t, tk.ID, obj["id"])
	assert.Equal(t, "write tests", obj["task"])
	assert.Equal(t, "todo", obj["status"])

	parsed, err := time.Parse(time.RFC3339, obj["timestamp"].(string))
	require.NoError(t, err)
	assert.True(t, parsed.Equal(tk.Timestamp))

	updated, err := time.Parse(time.RFC3339, obj["updated"].(string))
	require.NoError(t, err)
	assert.True(t, updated.Equal(tk.Updated))
}

func keysOf(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func TestEmptyAndMissingFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.jsonl")
	s, err := Open(missing)
	require.NoError(t, err)
	got, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, got)

	empty := filepath.Join(dir, "empty.jsonl")
	require.NoError(t, os.WriteFile(empty, nil, 0o644))
	s.Path = empty
	got, err = s.List()
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestListRejectsInvalidPersistedRecords(t *testing.T) {
	tests := map[string]Task{
		"empty id":       {Text: "task", Status: "todo", Timestamp: time.Now()},
		"empty text":     {ID: "id", Text: "  ", Status: "todo", Timestamp: time.Now()},
		"invalid status": {ID: "id", Text: "task", Status: "invalid", Timestamp: time.Now()},
		"zero timestamp": {ID: "id", Text: "task", Status: "todo"},
	}
	for name, task := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tasks.jsonl")
			raw, err := json.Marshal(task)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, append(raw, '\n'), 0o644))
			s, err := Open(path)
			require.NoError(t, err)

			_, err = s.List()
			require.Error(t, err)
			assert.ErrorContains(t, err, "line 1")
		})
	}
}

func TestListReportsPhysicalLineForInvalidRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("\n\n{}\n"), 0o644))
	s, err := Open(path)
	require.NoError(t, err)

	_, err = s.List()
	assert.ErrorContains(t, err, "line 3")
}

func TestListDay(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)

	day1 := time.Date(2026, 8, 13, 23, 30, 0, 0, time.UTC)
	day2 := time.Date(2026, 8, 14, 0, 30, 0, 0, time.UTC)

	s.Now = fixedClock(day1)
	a, err := s.Add("late day1")
	require.NoError(t, err)
	s.Now = fixedClock(day2)
	b, err := s.Add("early day2")
	require.NoError(t, err)

	got, err := s.ListDay(time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, []Task{a}, got)

	got, err = s.ListDay(time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, []Task{b}, got)
}

func TestSetBranch(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	s.Now = fixedClock(time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC))
	tk, err := s.Add("task")
	require.NoError(t, err)
	assert.Empty(t, tk.Branch, "plain tasks carry no branch")

	tk, err = s.SetBranch(tk.ID, "main")
	require.NoError(t, err)
	assert.Equal(t, "main", tk.Branch)

	reloaded, err := Open(s.Path)
	require.NoError(t, err)
	tasks, err := reloaded.List()
	require.NoError(t, err)
	assert.Equal(t, "main", tasks[0].Branch, "branch survives a reload")

	_, err = s.SetBranch("no-such-id", "main")
	assert.Error(t, err, "unknown id must error")
}

func TestSetAuthor(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	s.Now = fixedClock(time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC))
	tk, err := s.Add("task")
	require.NoError(t, err)
	assert.Empty(t, tk.Author, "plain tasks carry no author")

	tk, err = s.SetAuthor(tk.ID, "alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", tk.Author)

	reloaded, err := Open(s.Path)
	require.NoError(t, err)
	tasks, err := reloaded.List()
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", tasks[0].Author, "author survives a reload")

	_, err = s.SetAuthor("no-such-id", "alice@example.com")
	assert.Error(t, err, "unknown id must error")
}

func TestSetStatus(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	s.Now = fixedClock(time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC))
	tk, err := s.Add("task")
	require.NoError(t, err)

	_, err = s.SetStatus(tk.ID, "garbage")
	assert.Error(t, err, "invalid status must be rejected")

	_, err = s.SetStatus("no-such-id", "done")
	assert.Error(t, err, "unknown id must error")

	got, err := s.SetStatus(tk.ID, "in-progress")
	require.NoError(t, err)
	assert.Equal(t, "in-progress", got.Status)

	persisted, err := s.List()
	require.NoError(t, err)
	require.Len(t, persisted, 1)
	assert.Equal(t, "in-progress", persisted[0].Status)
}

func TestDelete(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	s.Now = fixedClock(time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC))
	tk, err := s.Add("doomed")
	require.NoError(t, err)

	assert.Error(t, s.Delete("no-such-id"))
	require.NoError(t, s.Delete(tk.ID))

	got, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestAddStampsUpdated(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	now := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	s.Now = fixedClock(now)

	tk, err := s.Add("task")
	require.NoError(t, err)
	assert.True(t, tk.Updated.Equal(now), "add stamps Updated with the clock")
	assert.True(t, tk.Deleted.IsZero(), "fresh tasks are live")
}

func TestAddAtStampsImportTime(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	now := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	s.Now = fixedClock(now)

	at := time.Date(2026, 8, 14, 16, 42, 0, 0, time.UTC)
	tk, err := s.AddAt("imported", "done", at)
	require.NoError(t, err)
	assert.True(t, tk.Timestamp.Equal(at), "event time is the given time")
	assert.True(t, tk.Updated.Equal(now), "modification time is the import moment")
}

func TestMutationsBumpUpdated(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	t0 := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	s.Now = fixedClock(t0)
	tk, err := s.Add("task")
	require.NoError(t, err)

	t1 := t0.Add(time.Hour)
	s.Now = fixedClock(t1)

	st, err := s.SetStatus(tk.ID, "in-progress")
	require.NoError(t, err)
	assert.True(t, st.Updated.Equal(t1), "SetStatus bumps Updated")
	assert.True(t, st.Timestamp.Equal(t0), "event time preserved")

	ed, err := s.UpdateText(tk.ID, "renamed")
	require.NoError(t, err)
	assert.True(t, ed.Updated.Equal(t1), "UpdateText bumps Updated")

	au, err := s.SetAuthor(tk.ID, "alice@example.com")
	require.NoError(t, err)
	assert.True(t, au.Updated.Equal(t1), "SetAuthor bumps Updated")

	br, err := s.SetBranch(tk.ID, "main")
	require.NoError(t, err)
	assert.True(t, br.Updated.Equal(t1), "SetBranch bumps Updated")
}

func TestDeleteTombstones(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	t0 := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	s.Now = fixedClock(t0)
	tk, err := s.Add("doomed")
	require.NoError(t, err)

	t1 := t0.Add(time.Hour)
	s.Now = fixedClock(t1)
	require.NoError(t, s.Delete(tk.ID))

	live, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, live, "tombstoned tasks are hidden")

	all, err := s.Snapshot()
	require.NoError(t, err)
	require.Len(t, all, 1, "the tombstone stays on record")
	assert.True(t, all[0].Deleted.Equal(t1), "delete stamps Deleted with the clock")

	_, err = s.FindByPrefix(tk.ID[:8])
	assert.Error(t, err, "tombstoned tasks are not addressable")

	raw, err := os.ReadFile(s.Path)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "doomed", "the tombstone persists on disk")
}

func TestSnapshotIncludesTombstones(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	base := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	s.Now = fixedClock(base)
	live, err := s.Add("live")
	require.NoError(t, err)
	dead, err := s.Add("dead")
	require.NoError(t, err)
	require.NoError(t, s.Delete(dead.ID))

	all, err := s.Snapshot()
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, live.ID, all[0].ID)
	assert.Equal(t, dead.ID, all[1].ID)

	got, err := s.List()
	require.NoError(t, err)
	assert.Equal(t, []Task{live}, got, "List hides the tombstone")
}

func TestReplaceAll(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	ts := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	tasks := []Task{
		{ID: "aa111111-1111-1111-1111-111111111111", Text: "one", Status: "todo", Timestamp: ts, Updated: ts},
		{ID: "bb222222-2222-2222-2222-222222222222", Text: "two", Status: "done", Timestamp: ts, Updated: ts, Deleted: ts.Add(time.Hour)},
	}
	require.NoError(t, s.ReplaceAll(tasks))

	reloaded, err := Open(s.Path)
	require.NoError(t, err)
	all, err := reloaded.Snapshot()
	require.NoError(t, err)
	assert.Equal(t, tasks, all, "the full set survives a reload, tombstones included")

	for _, bad := range []Task{
		{ID: "x", Text: "y", Status: "bogus", Timestamp: ts},
		{ID: "x", Text: "  ", Status: "todo", Timestamp: ts},
		{Text: "y", Status: "todo", Timestamp: ts},
	} {
		assert.Error(t, s.ReplaceAll([]Task{bad}), "invalid record rejected: %+v", bad)
	}

	all, err = s.Snapshot()
	require.NoError(t, err)
	assert.Len(t, all, 2, "a rejected replace leaves the store untouched")
}

func TestModTimeFallsBackToTimestamp(t *testing.T) {
	ts := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	pre := Task{ID: "x", Text: "y", Status: "todo", Timestamp: ts}
	assert.True(t, pre.ModTime().Equal(ts), "pre-sync records fall back to Timestamp")
	pre.Updated = ts.Add(time.Hour)
	assert.True(t, pre.ModTime().Equal(ts.Add(time.Hour)), "Updated wins when set")
}

func TestAddWithStatus(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	s.Now = fixedClock(time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC))

	empty, err := s.AddWithStatus("blocked thing", "")
	require.NoError(t, err)
	assert.Equal(t, "todo", empty.Status, "empty status defaults to todo")

	blocked, err := s.AddWithStatus("waiting on infra", "blocked")
	require.NoError(t, err)
	assert.Equal(t, "blocked", blocked.Status)

	_, err = s.AddWithStatus("x", "garbage")
	assert.ErrorContains(t, err, "invalid status")
	assert.ErrorContains(t, err, "todo, in-progress, blocked, done", "error lists the valid statuses")

	_, err = s.AddWithStatus("   ", "blocked")
	assert.ErrorContains(t, err, "empty task text")
}

func TestBlockedStatusRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	s.Now = fixedClock(time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC))

	tk, err := s.AddWithStatus("waiting on infra", "blocked")
	require.NoError(t, err)

	got, err := s.SetStatus(tk.ID, "in-progress")
	require.NoError(t, err)
	assert.Equal(t, "in-progress", got.Status)

	got, err = s.SetStatus(tk.ID, "blocked")
	require.NoError(t, err)
	assert.Equal(t, "blocked", got.Status)
}

func TestListRange(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)

	// Midnight boundaries in local time: 23:30 on day1, 00:30 on day2.
	s.Now = fixedClock(time.Date(2026, 8, 13, 23, 30, 0, 0, time.UTC))
	a, err := s.Add("late day1")
	require.NoError(t, err)
	s.Now = fixedClock(time.Date(2026, 8, 14, 0, 30, 0, 0, time.UTC))
	b, err := s.Add("early day2")
	require.NoError(t, err)
	s.Now = fixedClock(time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC))
	c, err := s.Add("day3")
	require.NoError(t, err)

	got, err := s.ListRange(time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC), time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, []Task{a, b}, got)

	got, err = s.ListRange(time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 16, 23, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, []Task{b, c}, got)

	got, err = s.ListRange(time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC), time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, []Task{a, b}, got, "reversed bounds are normalized")
}

func TestUpdateText(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	at := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	s.Now = fixedClock(at)
	tk, err := s.Add("fixd typo")
	require.NoError(t, err)
	_, err = s.SetStatus(tk.ID, "in-progress")
	require.NoError(t, err)

	got, err := s.UpdateText(tk.ID, "fixed typo")
	require.NoError(t, err)
	assert.Equal(t, "fixed typo", got.Text)
	assert.Equal(t, "in-progress", got.Status, "status preserved")
	assert.True(t, got.Timestamp.Equal(at), "timestamp preserved")

	persisted, err := s.List()
	require.NoError(t, err)
	require.Len(t, persisted, 1)
	assert.Equal(t, "fixed typo", persisted[0].Text)
	assert.Equal(t, "in-progress", persisted[0].Status)

	_, err = s.UpdateText(tk.ID, "   ")
	assert.ErrorContains(t, err, "empty task text")
	_, err = s.UpdateText("no-such-id", "x")
	assert.ErrorContains(t, err, "unknown id")
}

func TestApplyBatchCommitsMixedOperationsOnce(t *testing.T) {
	s := seedStore(t, "edit-id", "status-id", "delete-id")
	now := time.Date(2026, 8, 17, 11, 30, 0, 0, time.UTC)
	s.Now = fixedClock(now)

	changes, err := s.ApplyBatch([]BatchOperation{
		{Kind: OperationCreate, Text: "new task", Status: "blocked"},
		{Kind: OperationEdit, ID: "edit-id", Text: "edited task"},
		{Kind: OperationStatus, ID: "status-id", Status: "done"},
		{Kind: OperationDelete, ID: "delete-id"},
	})
	require.NoError(t, err)
	require.Len(t, changes, 4)

	assert.Nil(t, changes[0].Before)
	require.NotNil(t, changes[0].After)
	assert.Equal(t, "new task", changes[0].After.Text)
	assert.Equal(t, "blocked", changes[0].After.Status)
	assert.True(t, changes[0].After.Timestamp.Equal(now))
	assert.Equal(t, "task 0", changes[1].Before.Text)
	assert.Equal(t, "edited task", changes[1].After.Text)
	assert.Equal(t, "todo", changes[2].Before.Status)
	assert.Equal(t, "done", changes[2].After.Status)
	assert.Equal(t, "task 2", changes[3].Before.Text)
	assert.Nil(t, changes[3].After)

	tasks, err := s.List()
	require.NoError(t, err)
	require.Len(t, tasks, 3)
	assert.Equal(t, "edited task", tasks[0].Text)
	assert.Equal(t, "done", tasks[1].Status)
	assert.Equal(t, "new task", tasks[2].Text)
}

func TestApplyBatchIsAtomic(t *testing.T) {
	s := seedStore(t, "existing-id")
	rawBefore, err := os.ReadFile(s.Path)
	require.NoError(t, err)

	tests := map[string][]BatchOperation{
		"empty create":   {{Kind: OperationCreate, Text: "  "}},
		"invalid status": {{Kind: OperationStatus, ID: "existing-id", Status: "invalid"}},
		"empty edit":     {{Kind: OperationEdit, ID: "existing-id", Text: "  "}},
		"unknown id":     {{Kind: OperationDelete, ID: "missing-id"}},
		"unknown kind":   {{Kind: OperationKind("archive"), ID: "existing-id"}},
	}
	for name, invalid := range tests {
		t.Run(name, func(t *testing.T) {
			operations := append([]BatchOperation{{Kind: OperationEdit, ID: "existing-id", Text: "would change"}}, invalid...)
			_, err := s.ApplyBatch(operations)
			require.Error(t, err)
			rawAfter, readErr := os.ReadFile(s.Path)
			require.NoError(t, readErr)
			assert.Equal(t, rawBefore, rawAfter, "failed batch must leave the file unchanged")
		})
	}
}

func TestApplyBatchOperationsObservePriorOperations(t *testing.T) {
	s := seedStore(t, "existing-id")

	changes, err := s.ApplyBatch([]BatchOperation{
		{Kind: OperationEdit, ID: "existing-id", Text: "renamed"},
		{Kind: OperationStatus, ID: "existing-id", Status: "done"},
	})
	require.NoError(t, err)
	require.Len(t, changes, 2)
	assert.Equal(t, "renamed", changes[1].Before.Text)
	assert.Equal(t, "done", changes[1].After.Status)
}

func TestApplyBatchEmptyIsNoOp(t *testing.T) {
	s := seedStore(t, "existing-id")
	rawBefore, err := os.ReadFile(s.Path)
	require.NoError(t, err)

	changes, err := s.ApplyBatch(nil)
	require.NoError(t, err)
	assert.Empty(t, changes)
	rawAfter, err := os.ReadFile(s.Path)
	require.NoError(t, err)
	assert.Equal(t, rawBefore, rawAfter)
}

func TestApplyBatchReplacementFailurePreservesOriginal(t *testing.T) {
	s := seedStore(t, "existing-id")
	rawBefore, err := os.ReadFile(s.Path)
	require.NoError(t, err)
	s.replace = func(_, _ string) error { return errors.New("replace failed") }

	_, err = s.ApplyBatch([]BatchOperation{{Kind: OperationEdit, ID: "existing-id", Text: "changed"}})
	require.ErrorContains(t, err, "replace failed")
	rawAfter, readErr := os.ReadFile(s.Path)
	require.NoError(t, readErr)
	assert.Equal(t, rawBefore, rawAfter)
	matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(s.Path), ".tasks.jsonl-*"))
	require.NoError(t, globErr)
	assert.Empty(t, matches, "failed replacements must clean up temporary files")
}

func TestApplyBatchPreservesStorePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	s := seedStore(t, "existing-id")
	require.NoError(t, os.Chmod(s.Path, 0o600))

	_, err := s.ApplyBatch([]BatchOperation{{Kind: OperationEdit, ID: "existing-id", Text: "changed"}})
	require.NoError(t, err)
	info, err := os.Stat(s.Path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestApplyBatchRejectsDuplicateStoredIDs(t *testing.T) {
	s := seedStore(t, "duplicate-id", "duplicate-id")
	rawBefore, err := os.ReadFile(s.Path)
	require.NoError(t, err)

	_, err = s.ApplyBatch([]BatchOperation{{Kind: OperationDelete, ID: "duplicate-id"}})
	require.ErrorContains(t, err, "duplicate id")
	rawAfter, readErr := os.ReadFile(s.Path)
	require.NoError(t, readErr)
	assert.Equal(t, rawBefore, rawAfter)
}

func TestConcurrentMutationsDoNotLoseUpdates(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)

	const count = 32
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, addErr := s.AddAt(fmt.Sprintf("task %d", i), "todo", time.Date(2026, 8, 17, 9, i, 0, 0, time.UTC))
			errs <- addErr
		}()
	}
	wg.Wait()
	close(errs)
	for addErr := range errs {
		require.NoError(t, addErr)
	}
	tasks, err := s.List()
	require.NoError(t, err)
	assert.Len(t, tasks, count)
}

// --- the batch path carries the same sync semantics as the single-task one ---

// `standup -p "drop that"` goes through ApplyBatch. If it dropped the line
// instead of tombstoning, the delete would never reach another machine.
func TestApplyBatchDeleteTombstones(t *testing.T) {
	s := seedStore(t, "delete-id", "keep-id")
	now := time.Date(2026, 8, 17, 11, 30, 0, 0, time.UTC)
	s.Now = fixedClock(now)

	changes, err := s.ApplyBatch([]BatchOperation{{Kind: OperationDelete, ID: "delete-id"}})
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.Nil(t, changes[0].After, "a delete still reports no After")

	live, err := s.List()
	require.NoError(t, err)
	require.Len(t, live, 1)
	assert.Equal(t, "keep-id", live[0].ID)

	all, err := s.Snapshot()
	require.NoError(t, err)
	require.Len(t, all, 2, "the record stays on file for sync")
	for _, task := range all {
		if task.ID == "delete-id" {
			assert.True(t, task.Deleted.Equal(now), "tombstoned at the delete time")
		}
	}
}

func TestApplyBatchStampsUpdated(t *testing.T) {
	s := seedStore(t, "edit-id", "status-id")
	now := time.Date(2026, 8, 17, 11, 30, 0, 0, time.UTC)
	s.Now = fixedClock(now)

	changes, err := s.ApplyBatch([]BatchOperation{
		{Kind: OperationCreate, Text: "new task"},
		{Kind: OperationEdit, ID: "edit-id", Text: "edited task"},
		{Kind: OperationStatus, ID: "status-id", Status: "done"},
	})
	require.NoError(t, err)
	require.Len(t, changes, 3)
	for i, change := range changes {
		require.NotNil(t, change.After)
		assert.True(t, change.After.Updated.Equal(now), "operation %d stamps Updated for last-writer-wins", i)
	}
}

func TestApplyBatchTreatsTombstonedIDAsUnknown(t *testing.T) {
	s := seedStore(t, "gone-id")
	s.Now = fixedClock(time.Date(2026, 8, 17, 11, 30, 0, 0, time.UTC))
	_, err := s.ApplyBatch([]BatchOperation{{Kind: OperationDelete, ID: "gone-id"}})
	require.NoError(t, err)

	_, err = s.ApplyBatch([]BatchOperation{{Kind: OperationEdit, ID: "gone-id", Text: "back from the dead"}})
	assert.ErrorContains(t, err, "unknown id", "a deleted task is gone to the batch path too")
}

func TestDeleteTwiceReportsUnknown(t *testing.T) {
	s := seedStore(t, "gone-id")
	s.Now = fixedClock(time.Date(2026, 8, 17, 11, 30, 0, 0, time.UTC))
	require.NoError(t, s.Delete("gone-id"))
	assert.ErrorContains(t, s.Delete("gone-id"), "unknown id")
}
