package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	assert.ElementsMatch(t, []string{"id", "task", "status", "timestamp"}, keysOf(obj))
	assert.Equal(t, tk.ID, obj["id"])
	assert.Equal(t, "write tests", obj["task"])
	assert.Equal(t, "todo", obj["status"])

	parsed, err := time.Parse(time.RFC3339, obj["timestamp"].(string))
	require.NoError(t, err)
	assert.True(t, parsed.Equal(tk.Timestamp))
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
