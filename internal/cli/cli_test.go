package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"standup/internal/agent"
	"standup/internal/config"
	"standup/internal/git"
	"standup/internal/report"
	"standup/internal/store"
)

type fakeAss struct {
	added     []string
	addResult []store.Task
	addErr    error
	genCalls  int
	genSec    *report.Section
	genOut    string
	genErr    error
}

func (f *fakeAss) AddTasks(ctx context.Context, rawText string) ([]store.Task, error) {
	f.added = append(f.added, rawText)
	if f.addErr != nil {
		return nil, f.addErr
	}
	return f.addResult, nil
}

func (f *fakeAss) Generate(ctx context.Context, sec report.Section) (string, error) {
	f.genCalls++
	*f.genSec = sec
	return f.genOut, f.genErr
}

var _ agent.Assistant = (*fakeAss)(nil)

func newHarness(t *testing.T, ass *fakeAss) (*store.Store, *cobra.Command, *bytes.Buffer) {
	t.Helper()
	ass.genSec = &report.Section{}
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	buf := &bytes.Buffer{}
	root := New(ass, st, config.Config{MeetingTime: "09:30"})
	root.SetOut(buf)
	root.SetErr(buf)
	return st, root, buf
}

func pipeStdin(t *testing.T, content string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	require.NoError(t, err)
	_, err = f.WriteString(content)
	require.NoError(t, err)
	_, err = f.Seek(0, 0)
	require.NoError(t, err)
	old := os.Stdin
	os.Stdin = f
	t.Cleanup(func() {
		os.Stdin = old
		if err := f.Close(); err != nil {
			t.Errorf("close stdin temp file: %v", err)
		}
	})
}

var today = func(h, m int) func() time.Time {
	return func() time.Time { return time.Date(2026, 8, 15, h, m, 0, 0, time.Local) }
}

func TestAddArgs(t *testing.T) {
	ass := &fakeAss{}
	_, root, _ := newHarness(t, ass)
	root.SetArgs([]string{"add", "hello", "world"})
	require.NoError(t, root.Execute())
	require.Len(t, ass.added, 1)
	assert.Equal(t, "hello world", ass.added[0])
}

func TestAddFlagShorthand(t *testing.T) {
	ass := &fakeAss{}
	_, root, _ := newHarness(t, ass)
	root.SetArgs([]string{"-a", "quick task"})
	require.NoError(t, root.Execute())
	require.Len(t, ass.added, 1)
	assert.Equal(t, "quick task", ass.added[0])
}

func TestAddStdin(t *testing.T) {
	ass := &fakeAss{}
	pipeStdin(t, "line1\nline2")
	_, root, _ := newHarness(t, ass)
	root.SetArgs([]string{"add"})
	require.NoError(t, root.Execute())
	require.Len(t, ass.added, 1)
	assert.Equal(t, "line1\nline2", ass.added[0])
}

func TestAddError(t *testing.T) {
	ass := &fakeAss{addErr: errors.New("boom")}
	_, root, _ := newHarness(t, ass)
	root.SetArgs([]string{"add", "task"})
	assert.Error(t, root.Execute())
}

func TestPainterStatusColors(t *testing.T) {
	p := painter{on: true}
	assert.Equal(t, ansiTodo+"todo"+ansiReset, p.status("todo"))
	assert.Equal(t, ansiInProgress+"in-progress"+ansiReset, p.status("in-progress"))
	assert.Equal(t, ansiBlocked+"blocked"+ansiReset, p.status("blocked"))
	assert.Equal(t, ansiDone+"done"+ansiReset, p.status("done"))
	assert.Equal(t, "bogus", p.status("bogus"))
	assert.Equal(t, ansiQuiet+"12:00"+ansiReset, p.quiet("12:00"))
}

func TestPainterOffForNonTerminal(t *testing.T) {
	p := newPainter(&bytes.Buffer{})
	assert.Equal(t, "todo", p.status("todo"))
	assert.Equal(t, "12:00", p.quiet("12:00"))
}

func TestTaskEntriesRefresh(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	st.Now = today(8, 0)
	added, err := st.Add("review pull request")
	require.NoError(t, err)

	entries, err := taskEntries(st, "", painter{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].label, "[todo]")
	assert.Contains(t, entries[0].label, added.ID[:8])

	_, err = st.SetStatus(added.ID, "done")
	require.NoError(t, err)

	entries, err = taskEntries(st, "", painter{on: true})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].label, "["+ansiDone+"done"+ansiReset+"]")

	require.NoError(t, st.Delete(added.ID))
	entries, err = taskEntries(st, "", painter{})
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestListPlain(t *testing.T) {
	ass := &fakeAss{}
	pipeStdin(t, "")
	st, root, buf := newHarness(t, ass)
	st.Now = today(8, 0)
	added, err := st.Add("write tests")
	require.NoError(t, err)
	root.SetArgs([]string{"list"})
	require.NoError(t, root.Execute())
	out := buf.String()
	assert.Contains(t, out, added.ID)
	assert.Contains(t, out, "todo")
	assert.Contains(t, out, "08:00")
	assert.Contains(t, out, "write tests")
}

func TestListEmpty(t *testing.T) {
	ass := &fakeAss{}
	pipeStdin(t, "")
	st, root, buf := newHarness(t, ass)
	st.Now = today(8, 0)
	root.SetArgs([]string{"list"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "no tasks")
}

func TestGenerate(t *testing.T) {
	ass := &fakeAss{genOut: "## Yesterday\n- did stuff"}
	st, root, buf := newHarness(t, ass)
	st.Now = today(7, 0)
	added, err := st.Add("fix bug")
	require.NoError(t, err)
	_, err = st.SetStatus(added.ID, "done")
	require.NoError(t, err)
	st.Now = today(8, 0)
	root.SetArgs([]string{"generate"})
	require.NoError(t, root.Execute())
	assert.Equal(t, 1, ass.genCalls)
	assert.Contains(t, buf.String(), "did stuff")
}

func TestGenerateEmpty(t *testing.T) {
	ass := &fakeAss{genOut: "should not appear"}
	st, root, buf := newHarness(t, ass)
	st.Now = today(8, 0)
	root.SetArgs([]string{"generate"})
	require.NoError(t, root.Execute())
	assert.Equal(t, 0, ass.genCalls)
	assert.Contains(t, buf.String(), "nothing to report")
}

func TestGenerateFlagShorthand(t *testing.T) {
	ass := &fakeAss{}
	st, root, buf := newHarness(t, ass)
	st.Now = today(8, 0)
	root.SetArgs([]string{"-g"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "nothing to report")
}

func TestListFlagShorthand(t *testing.T) {
	ass := &fakeAss{}
	pipeStdin(t, "")
	st, root, buf := newHarness(t, ass)
	st.Now = today(8, 0)
	root.SetArgs([]string{"-l"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "no tasks")
}

func TestDoneRmMissingIDUsage(t *testing.T) {
	for _, cmd := range []string{"done", "rm"} {
		t.Run(cmd, func(t *testing.T) {
			_, root, _ := newHarness(t, &fakeAss{})
			root.SetArgs([]string{cmd})
			err := root.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "usage: standup "+cmd+" <id>")
			assert.Contains(t, err.Error(), "standup list")
		})
	}
}

func TestAddEmptyTextUsage(t *testing.T) {
	for name, args := range map[string][]string{
		"no args":         {"add"},
		"empty string":    {"add", ""},
		"flag empty":      {"-a", ""},
		"flag whitespace": {"-a", "   "},
	} {
		t.Run(name, func(t *testing.T) {
			ass := &fakeAss{}
			_, root, _ := newHarness(t, ass)
			root.SetArgs(args)
			err := root.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), `usage: standup add "task text"`)
			assert.Empty(t, ass.added)
		})
	}
}

func TestAddPrintsSavedTasks(t *testing.T) {
	ass := &fakeAss{addResult: []store.Task{
		{ID: "1", Text: "Fixed login bug", Status: "todo"},
		{ID: "2", Text: "Deployed the API", Status: "todo"},
	}}
	_, root, buf := newHarness(t, ass)
	root.SetArgs([]string{"add", "fixd bug and deployd"})
	require.NoError(t, root.Execute())
	out := buf.String()
	assert.Contains(t, out, "Fixed login bug")
	assert.Contains(t, out, "Deployed the API")
}

func TestAddErrorReportsNoTasks(t *testing.T) {
	ass := &fakeAss{addErr: assert.AnError}
	_, root, buf := newHarness(t, ass)
	root.SetArgs([]string{"add", "x"})
	assert.Error(t, root.Execute())
	assert.Contains(t, buf.String(), "Error")
	assert.NotContains(t, buf.String(), "\n- ")
}

func TestAborted(t *testing.T) {
	assert.True(t, aborted(promptui.ErrInterrupt))
	assert.True(t, aborted(io.EOF))
	assert.True(t, aborted(errors.New("^D")), "promptui surfaces ^D as an error")
	assert.False(t, aborted(errors.New("real error")))
	assert.False(t, aborted(nil))
}

func TestSpinner(t *testing.T) {
	var buf bytes.Buffer
	s := newSpinner(&buf, true, "adding tasks")
	require.NoError(t, s.Start())
	require.NoError(t, s.Stop())
	assert.Contains(t, buf.String(), "adding tasks")
	assert.Contains(t, buf.String(), "\r")
}

func TestSpinnerInactive(t *testing.T) {
	var buf bytes.Buffer
	s := newSpinner(&buf, false, "adding tasks")
	require.NoError(t, s.Start())
	require.NoError(t, s.Stop())
	assert.Empty(t, buf.String())
}

func TestSpinPropagatesFnError(t *testing.T) {
	err := spin("adding tasks", func() error { return assert.AnError })
	assert.ErrorIs(t, err, assert.AnError)
}

func TestGenerateAlias(t *testing.T) {
	ass := &fakeAss{genOut: "## Today\n- x"}
	st, root, buf := newHarness(t, ass)
	st.Now = today(8, 0)
	_, err := st.Add("x")
	require.NoError(t, err)
	_, err = st.SetStatus(mustIDs(t, st)[0], "done")
	require.NoError(t, err)
	root.SetArgs([]string{"g"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "## Today")
}

func mustIDs(t *testing.T, st *store.Store) []string {
	t.Helper()
	tasks, err := st.List()
	require.NoError(t, err)
	out := make([]string, len(tasks))
	for i, tk := range tasks {
		out[i] = tk.ID
	}
	return out
}

func TestDone(t *testing.T) {
	ass := &fakeAss{}
	st, root, _ := newHarness(t, ass)
	added, err := st.Add("ship it")
	require.NoError(t, err)
	root.SetArgs([]string{"done", added.ID})
	require.NoError(t, root.Execute())
	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "done", tasks[0].Status)
}

func TestDoneIDPrefix(t *testing.T) {
	ass := &fakeAss{}
	st, root, _ := newHarness(t, ass)
	added, err := st.Add("ship it")
	require.NoError(t, err)
	root.SetArgs([]string{"done", added.ID[:8]})
	require.NoError(t, root.Execute())
	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "done", tasks[0].Status)
}

func TestDoneAmbiguousPrefix(t *testing.T) {
	ass := &fakeAss{}
	st, root, _ := newHarness(t, ass)
	_, err := st.Add("one")
	require.NoError(t, err)
	root.SetArgs([]string{"done", "x"})
	err = root.Execute()
	require.Error(t, err)
}

func TestDoneEmptyIDArg(t *testing.T) {
	st, root, _ := newHarness(t, &fakeAss{})
	added, err := st.Add("only task")
	require.NoError(t, err)
	for _, arg := range []string{""} {
		root.SetArgs([]string{"done", arg})
		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "usage:")
	}
	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "todo", tasks[0].Status)
	assert.Equal(t, added.Status, tasks[0].Status)
}

func TestDoneNoArgs(t *testing.T) {
	ass := &fakeAss{}
	_, root, _ := newHarness(t, ass)
	root.SetArgs([]string{"done"})
	assert.Error(t, root.Execute())
}

func TestRm(t *testing.T) {
	ass := &fakeAss{}
	st, root, _ := newHarness(t, ass)
	added, err := st.Add("ship it")
	require.NoError(t, err)
	root.SetArgs([]string{"rm", added.ID})
	require.NoError(t, root.Execute())
	tasks, err := st.List()
	require.NoError(t, err)
	assert.Empty(t, tasks)
}

func seedDays(t *testing.T, st *store.Store) {
	t.Helper()
	st.Now = func() time.Time { return time.Date(2026, 8, 13, 10, 0, 0, 0, time.Local) }
	_, err := st.Add("older task #api")
	require.NoError(t, err)
	st.Now = func() time.Time { return time.Date(2026, 8, 14, 10, 0, 0, 0, time.Local) }
	_, err = st.Add("yesterday task #infra")
	require.NoError(t, err)
	st.Now = func() time.Time { return time.Date(2026, 8, 15, 8, 0, 0, 0, time.Local) }
	_, err = st.Add("today task #api")
	require.NoError(t, err)
}

func TestListDate(t *testing.T) {
	pipeStdin(t, "")
	st, root, buf := newHarness(t, &fakeAss{})
	seedDays(t, st)
	root.SetArgs([]string{"list", "--date", "2026-08-14"})
	require.NoError(t, root.Execute())
	out := buf.String()
	assert.Contains(t, out, "yesterday task #infra")
	assert.NotContains(t, out, "today task")
	assert.NotContains(t, out, "older task")
}

func TestListDateEmptySameMessage(t *testing.T) {
	pipeStdin(t, "")
	st, root, buf := newHarness(t, &fakeAss{})
	st.Now = today(8, 0)
	root.SetArgs([]string{"list", "--date", "2026-07-01"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "no tasks")
}

func TestListDateUnparsable(t *testing.T) {
	pipeStdin(t, "")
	_, root, _ := newHarness(t, &fakeAss{})
	root.SetArgs([]string{"list", "--date", "tomorrow"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage")
}

func TestListDays(t *testing.T) {
	pipeStdin(t, "")
	st, root, buf := newHarness(t, &fakeAss{})
	seedDays(t, st)
	root.SetArgs([]string{"list", "--days", "3"})
	require.NoError(t, root.Execute())
	out := buf.String()
	i1 := strings.Index(out, "older task")
	i2 := strings.Index(out, "yesterday task")
	i3 := strings.Index(out, "today task")
	require.GreaterOrEqual(t, i1, 0)
	assert.Greater(t, i2, i1, "oldest first")
	assert.Greater(t, i3, i2, "oldest first")
}

func TestListDateAndDaysMutuallyExclusive(t *testing.T) {
	pipeStdin(t, "")
	_, root, _ := newHarness(t, &fakeAss{})
	root.SetArgs([]string{"list", "--date", "2026-08-14", "--days", "3"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestListTagFilter(t *testing.T) {
	pipeStdin(t, "")
	st, root, buf := newHarness(t, &fakeAss{})
	seedDays(t, st)
	root.SetArgs([]string{"list", "--days", "3", "--tag", "#API"})
	require.NoError(t, root.Execute())
	out := buf.String()
	assert.Contains(t, out, "older task #api", "case-insensitive substring match")
	assert.Contains(t, out, "today task #api")
	assert.NotContains(t, out, "#infra")
}

func TestListTagNoMatch(t *testing.T) {
	pipeStdin(t, "")
	st, root, buf := newHarness(t, &fakeAss{})
	st.Now = today(8, 0)
	_, err := st.Add("plain task")
	require.NoError(t, err)
	root.SetArgs([]string{"list", "--tag", "api"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "no tasks", "tag-less tasks never match")
}

func TestGenerateDaysArg(t *testing.T) {
	ass := &fakeAss{genOut: "## Today"}
	st, root, _ := newHarness(t, ass)
	seedDays(t, st)
	st.Now = today(9, 0)
	root.SetArgs([]string{"generate", "3"})
	require.NoError(t, root.Execute())
	require.Len(t, ass.genSec.Days, 3)
	assert.Equal(t, []string{"Thu 2026-08-13", "Yesterday", "Today"},
		[]string{ass.genSec.Days[0].Heading, ass.genSec.Days[1].Heading, ass.genSec.Days[2].Heading})
}

func TestGenerateDaysDefaultTwo(t *testing.T) {
	ass := &fakeAss{genOut: "x"}
	st, root, _ := newHarness(t, ass)
	seedDays(t, st)
	st.Now = today(9, 0)
	root.SetArgs([]string{"generate"})
	require.NoError(t, root.Execute())
	require.Len(t, ass.genSec.Days, 2)
	assert.NotNil(t, ass.genSec.Yesterday, "compat fields set for the default window")
}

func TestGenerateBadDaysArg(t *testing.T) {
	for _, arg := range []string{"0", "abc"} {
		_, root, _ := newHarness(t, &fakeAss{})
		root.SetArgs([]string{"generate", arg})
		err := root.Execute()
		require.Error(t, err, "arg %q must fail", arg)
		assert.Contains(t, err.Error(), "usage")
	}
}

func TestGenerateCarryOverInPrompt(t *testing.T) {
	ass := &fakeAss{genOut: "x"}
	st, root, _ := newHarness(t, ass)
	st.Now = func() time.Time { return time.Date(2026, 8, 14, 16, 0, 0, 0, time.Local) }
	unfinished, err := st.Add("finish auth")
	require.NoError(t, err)
	st.Now = today(8, 0)
	root.SetArgs([]string{"generate"})
	require.NoError(t, root.Execute())
	found := false
	for _, tk := range ass.genSec.Today {
		if tk.ID == unfinished.ID {
			found = true
		}
	}
	assert.True(t, found, "unfinished yesterday task carried into Today")
}

func TestGenerateOutputFile(t *testing.T) {
	ass := &fakeAss{genOut: "## Today\n- did stuff"}
	st, root, buf := newHarness(t, ass)
	st.Now = today(8, 0)
	_, err := st.Add("fix bug")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "standup.md")
	root.SetArgs([]string{"generate", "-o", path})
	require.NoError(t, root.Execute())
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "## Today\n- did stuff\n", string(b))
	assert.Empty(t, buf.String(), "stdout silent when a path is given")
}

func TestGenerateOutputFileTruncates(t *testing.T) {
	ass := &fakeAss{genOut: "short"}
	st, root, _ := newHarness(t, ass)
	st.Now = today(8, 0)
	_, err := st.Add("fix bug")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "standup.md")
	require.NoError(t, os.WriteFile(path, []byte("much longer previous content that must be truncated"), 0o644))
	root.SetArgs([]string{"generate", "-o", path})
	require.NoError(t, root.Execute())
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "short\n", string(b))
}

func TestGenerateOutputFileUnwritable(t *testing.T) {
	ass := &fakeAss{genOut: "x"}
	st, root, _ := newHarness(t, ass)
	st.Now = today(8, 0)
	_, err := st.Add("fix bug")
	require.NoError(t, err)
	root.SetArgs([]string{"generate", "-o", filepath.Join(t.TempDir(), "no-such-dir", "out.md")})
	assert.Error(t, root.Execute(), "unwritable path surfaces as command failure")
}

func TestGenerateBlockedSection(t *testing.T) {
	ass := &fakeAss{genOut: "x"}
	st, root, _ := newHarness(t, ass)
	st.Now = func() time.Time { return time.Date(2026, 8, 14, 16, 0, 0, 0, time.Local) }
	blocked, err := st.AddWithStatus("waiting on infra", "blocked")
	require.NoError(t, err)
	st.Now = today(9, 0)
	root.SetArgs([]string{"generate"})
	require.NoError(t, root.Execute())
	require.Len(t, ass.genSec.Blockers, 1)
	assert.Equal(t, blocked.ID, ass.genSec.Blockers[0].ID)
}

func TestCommits(t *testing.T) {
	ass := &fakeAss{addResult: []store.Task{{ID: "1", Text: "fix login bug"}}}
	st, root, buf := newHarness(t, ass)
	st.Now = today(9, 0)
	old := gitLog
	gitLog = func(dir string, since time.Time) ([]git.Commit, error) {
		assert.Equal(t, ".", dir)
		assert.True(t, since.Equal(time.Date(2026, 8, 14, 0, 0, 0, 0, time.Local)), "default lookback: last working day")
		return []git.Commit{
			{Subject: "fix login bug", When: time.Date(2026, 8, 14, 10, 0, 0, 0, time.Local)},
			{Subject: "write tests", When: time.Date(2026, 8, 14, 16, 0, 0, 0, time.Local)},
		}, nil
	}
	t.Cleanup(func() { gitLog = old })
	root.SetArgs([]string{"commits"})
	require.NoError(t, root.Execute())
	require.Len(t, ass.added, 1)
	assert.Equal(t, "fix login bug\n\nwrite tests", ass.added[0], "blank-line separated so offline mode splits them")
	assert.Contains(t, buf.String(), "fix login bug")
}

func TestCommitsDaysArg(t *testing.T) {
	ass := &fakeAss{}
	st, root, _ := newHarness(t, ass)
	st.Now = today(9, 0)
	old := gitLog
	gitLog = func(dir string, since time.Time) ([]git.Commit, error) {
		assert.True(t, since.Equal(time.Date(2026, 8, 13, 0, 0, 0, 0, time.Local)), "3 days: since start of two days ago")
		return nil, nil
	}
	t.Cleanup(func() { gitLog = old })
	root.SetArgs([]string{"commits", "3"})
	require.NoError(t, root.Execute())
}

func TestCommitsBadArg(t *testing.T) {
	_, root, _ := newHarness(t, &fakeAss{})
	root.SetArgs([]string{"commits", "zero"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage")
}

func TestCommitsEmpty(t *testing.T) {
	ass := &fakeAss{}
	st, root, buf := newHarness(t, ass)
	st.Now = today(9, 0)
	old := gitLog
	gitLog = func(dir string, since time.Time) ([]git.Commit, error) { return nil, nil }
	t.Cleanup(func() { gitLog = old })
	root.SetArgs([]string{"commits"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "no commits found")
	assert.Empty(t, ass.added)
}

func TestEditArg(t *testing.T) {
	ass := &fakeAss{}
	st, root, _ := newHarness(t, ass)
	at := time.Date(2026, 8, 15, 8, 0, 0, 0, time.Local)
	st.Now = func() time.Time { return at }
	added, err := st.Add("fixd typo")
	require.NoError(t, err)
	_, err = st.SetStatus(added.ID, "in-progress")
	require.NoError(t, err)

	root.SetArgs([]string{"edit", added.ID, "fixed", "typo"})
	require.NoError(t, root.Execute())

	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "fixed typo", tasks[0].Text)
	assert.Equal(t, "in-progress", tasks[0].Status, "status preserved")
	assert.True(t, tasks[0].Timestamp.Equal(at), "timestamp preserved")
}

func TestEditEmptyReplacementRejected(t *testing.T) {
	ass := &fakeAss{}
	st, root, _ := newHarness(t, ass)
	st.Now = today(8, 0)
	added, err := st.Add("keep me")
	require.NoError(t, err)
	root.SetArgs([]string{"edit", added.ID, "   "})
	err = root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty task text")
}

func TestEditMissingIDUsage(t *testing.T) {
	_, root, _ := newHarness(t, &fakeAss{})
	root.SetArgs([]string{"edit"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage: standup edit <id>")
}

func TestEditViaEditor(t *testing.T) {
	ass := &fakeAss{}
	st, root, _ := newHarness(t, ass)
	st.Now = today(8, 0)
	added, err := st.Add("fixd typo")
	require.NoError(t, err)

	script := filepath.Join(t.TempDir(), "fake-editor.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf 'edited in editor' > \"$1\"\n"), 0o755))
	t.Setenv("EDITOR", script)

	root.SetArgs([]string{"edit", added.ID[:8]})
	require.NoError(t, root.Execute())

	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "edited in editor", tasks[0].Text, "editor round-trips text, whitespace arg still opens editor")
}

func TestEditEditorFailure(t *testing.T) {
	ass := &fakeAss{}
	st, root, _ := newHarness(t, ass)
	st.Now = today(8, 0)
	added, err := st.Add("keep me")
	require.NoError(t, err)

	script := filepath.Join(t.TempDir(), "failing-editor.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nexit 3\n"), 0o755))
	t.Setenv("EDITOR", script)

	root.SetArgs([]string{"edit", added.ID})
	err = root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "editor")

	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "keep me", tasks[0].Text, "failed editor leaves text untouched")
}
