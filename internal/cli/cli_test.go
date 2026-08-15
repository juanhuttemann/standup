package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"standup/internal/agent"
	"standup/internal/config"
	"standup/internal/report"
	"standup/internal/store"
)

type fakeAss struct {
	added     []string
	addResult []store.Task
	addErr    error
	genCalls  int
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
	return f.genOut, f.genErr
}

var _ agent.Assistant = (*fakeAss)(nil)

func newHarness(t *testing.T, ass *fakeAss) (*store.Store, *cobra.Command, *bytes.Buffer) {
	t.Helper()
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

func TestTaskEntriesRefresh(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	st.Now = today(8, 0)
	added, err := st.Add("review pull request")
	require.NoError(t, err)

	entries, err := taskEntries(st)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].label, "[todo]")
	assert.Contains(t, entries[0].label, added.ID[:8])

	_, err = st.SetStatus(added.ID, "done")
	require.NoError(t, err)

	entries, err = taskEntries(st)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].label, "[done]")

	require.NoError(t, st.Delete(added.ID))
	entries, err = taskEntries(st)
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
	assert.Contains(t, buf.String(), "no tasks today")
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
	assert.Contains(t, buf.String(), "no tasks today")
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
