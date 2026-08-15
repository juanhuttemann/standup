package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	defaults "standup/config"
	"standup/internal/agent"
	"standup/internal/config"
	"standup/internal/git"
	"standup/internal/report"
	"standup/internal/store"
)

type fakeAssistant struct {
	added     []string
	addResult []store.Task
	addErr    error
	genCalls  int
	genSec    *report.Section
	genSecs   []report.Section
	genOut    string
	genErr    error
}

func (f *fakeAssistant) AddTasks(ctx context.Context, rawText string) ([]store.Task, error) {
	f.added = append(f.added, rawText)
	if f.addErr != nil {
		return nil, f.addErr
	}
	return f.addResult, nil
}

func (f *fakeAssistant) Generate(ctx context.Context, sec report.Section) (string, error) {
	f.genCalls++
	*f.genSec = sec
	f.genSecs = append(f.genSecs, sec)
	return f.genOut, f.genErr
}

var _ agent.Assistant = (*fakeAssistant)(nil)

func newHarness(t *testing.T, assistant *fakeAssistant) (*store.Store, *cobra.Command, *bytes.Buffer) {
	t.Helper()
	assistant.genSec = &report.Section{}
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	buf := &bytes.Buffer{}
	root := New(func() (Deps, error) {
		return Deps{Assistant: func() (agent.Assistant, error) { return assistant, nil }, Raw: assistant, Store: st, Config: config.Config{MeetingTime: "09:30"}}, nil
	})
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
	assistant := &fakeAssistant{}
	_, root, _ := newHarness(t, assistant)
	root.SetArgs([]string{"add", "hello", "world"})
	require.NoError(t, root.Execute())
	require.Len(t, assistant.added, 1)
	assert.Equal(t, "hello world", assistant.added[0])
}

func TestAddFlagShorthand(t *testing.T) {
	assistant := &fakeAssistant{}
	_, root, _ := newHarness(t, assistant)
	root.SetArgs([]string{"-a", "quick task"})
	require.NoError(t, root.Execute())
	require.Len(t, assistant.added, 1)
	assert.Equal(t, "quick task", assistant.added[0])
}

func TestAddStdin(t *testing.T) {
	assistant := &fakeAssistant{}
	pipeStdin(t, "line1\nline2")
	_, root, _ := newHarness(t, assistant)
	root.SetArgs([]string{"add"})
	require.NoError(t, root.Execute())
	require.Len(t, assistant.added, 1)
	assert.Equal(t, "line1\nline2", assistant.added[0])
}

func TestAddStdinNormalizesCRLF(t *testing.T) {
	assistant := &fakeAssistant{addResult: []store.Task{{Text: "crlf task"}}}
	pipeStdin(t, "crlf task\r\n")
	_, root, _ := newHarness(t, assistant)
	root.SetArgs([]string{"add", "--raw"})
	require.NoError(t, root.Execute())
	assert.Equal(t, "crlf task\n", assistant.added[0], "Windows CRLF is normalized at stdin ingest")
}

func TestAddError(t *testing.T) {
	assistant := &fakeAssistant{addErr: errors.New("boom")}
	_, root, _ := newHarness(t, assistant)
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

	entries, err := taskEntries(st, "", st.Now(), painter{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].label, "[todo]")
	assert.Contains(t, entries[0].label, added.ID[:8])

	_, err = st.SetStatus(added.ID, "done")
	require.NoError(t, err)

	entries, err = taskEntries(st, "", st.Now(), painter{on: true})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].label, "["+ansiDone+"done"+ansiReset+"]")

	require.NoError(t, st.Delete(added.ID))
	entries, err = taskEntries(st, "", st.Now(), painter{})
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestListPlain(t *testing.T) {
	assistant := &fakeAssistant{}
	pipeStdin(t, "")
	st, root, buf := newHarness(t, assistant)
	st.Now = today(8, 0)
	added, err := st.Add("write tests")
	require.NoError(t, err)
	root.SetArgs([]string{"list"})
	require.NoError(t, root.Execute())
	out := buf.String()
	assert.Contains(t, out, added.ID[:8], "plain output shows the short id")
	assert.NotContains(t, out, added.ID, "full UUID ids stay out of plain output")
	assert.Contains(t, out, "todo")
	assert.Contains(t, out, "08:00")
	assert.Contains(t, out, "write tests")
}

func TestListEmpty(t *testing.T) {
	assistant := &fakeAssistant{}
	pipeStdin(t, "")
	st, root, buf := newHarness(t, assistant)
	st.Now = today(8, 0)
	root.SetArgs([]string{"list"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "no tasks")
}

func TestGenerate(t *testing.T) {
	assistant := &fakeAssistant{genOut: "## Yesterday\n- did stuff"}
	st, root, buf := newHarness(t, assistant)
	st.Now = today(7, 0)
	added, err := st.Add("fix bug")
	require.NoError(t, err)
	_, err = st.SetStatus(added.ID, "done")
	require.NoError(t, err)
	st.Now = today(8, 0)
	root.SetArgs([]string{"generate"})
	require.NoError(t, root.Execute())
	assert.Equal(t, 1, assistant.genCalls)
	assert.Contains(t, buf.String(), "did stuff")
}

func TestGenerateEmpty(t *testing.T) {
	assistant := &fakeAssistant{genOut: "should not appear"}
	st, root, buf := newHarness(t, assistant)
	st.Now = today(8, 0)
	root.SetArgs([]string{"generate"})
	require.NoError(t, root.Execute())
	assert.Equal(t, 0, assistant.genCalls)
	assert.Contains(t, buf.String(), "nothing to report")
}

func TestGenerateFlagShorthand(t *testing.T) {
	assistant := &fakeAssistant{}
	st, root, buf := newHarness(t, assistant)
	st.Now = today(8, 0)
	root.SetArgs([]string{"-g"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "nothing to report")
}

func TestListFlagShorthand(t *testing.T) {
	assistant := &fakeAssistant{}
	pipeStdin(t, "")
	st, root, buf := newHarness(t, assistant)
	st.Now = today(8, 0)
	root.SetArgs([]string{"-l"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "no tasks")
}

func TestDoneRmMissingIDUsage(t *testing.T) {
	for _, cmd := range []string{"done", "rm"} {
		t.Run(cmd, func(t *testing.T) {
			_, root, _ := newHarness(t, &fakeAssistant{})
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
			assistant := &fakeAssistant{}
			_, root, _ := newHarness(t, assistant)
			root.SetArgs(args)
			err := root.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), `usage: standup add "task text"`)
			assert.Empty(t, assistant.added)
		})
	}
}

func TestAddPrintsSavedTasks(t *testing.T) {
	assistant := &fakeAssistant{addResult: []store.Task{
		{ID: "1", Text: "Fixed login bug", Status: "todo"},
		{ID: "2", Text: "Deployed the API", Status: "todo"},
	}}
	_, root, buf := newHarness(t, assistant)
	root.SetArgs([]string{"add", "fixd bug and deployd"})
	require.NoError(t, root.Execute())
	out := buf.String()
	assert.Contains(t, out, "Fixed login bug")
	assert.Contains(t, out, "Deployed the API")
}

func TestAddErrorReportsNoTasks(t *testing.T) {
	assistant := &fakeAssistant{addErr: assert.AnError}
	_, root, buf := newHarness(t, assistant)
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
	assistant := &fakeAssistant{genOut: "## Today\n- x"}
	st, root, buf := newHarness(t, assistant)
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
	assistant := &fakeAssistant{}
	st, root, _ := newHarness(t, assistant)
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
	assistant := &fakeAssistant{}
	st, root, _ := newHarness(t, assistant)
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
	assistant := &fakeAssistant{}
	st, root, _ := newHarness(t, assistant)
	_, err := st.Add("one")
	require.NoError(t, err)
	root.SetArgs([]string{"done", "x"})
	err = root.Execute()
	require.Error(t, err)
}

func TestDoneEmptyIDArg(t *testing.T) {
	st, root, _ := newHarness(t, &fakeAssistant{})
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
	assistant := &fakeAssistant{}
	_, root, _ := newHarness(t, assistant)
	root.SetArgs([]string{"done"})
	assert.Error(t, root.Execute())
}

func TestRm(t *testing.T) {
	assistant := &fakeAssistant{}
	st, root, buf := newHarness(t, assistant)
	added, err := st.Add("ship it")
	require.NoError(t, err)
	root.SetArgs([]string{"rm", added.ID})
	require.NoError(t, root.Execute())
	tasks, err := st.List()
	require.NoError(t, err)
	assert.Empty(t, tasks)
	assert.Contains(t, buf.String(), "- removed: ship it")
}

func TestRmEchoFoldsMultilineTaskText(t *testing.T) {
	st, root, buf := newHarness(t, &fakeAssistant{})
	added, err := st.AddAt("feat: big thing\n\nbody line", "done", time.Date(2026, 8, 15, 12, 0, 0, 0, time.Local))
	require.NoError(t, err)
	root.SetArgs([]string{"rm", added.ID})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "- removed: feat: big thing body line",
		"rm echoes one row like every other command, so multi-line text can be verified before deletion")
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
	st, root, buf := newHarness(t, &fakeAssistant{})
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
	st, root, buf := newHarness(t, &fakeAssistant{})
	st.Now = today(8, 0)
	root.SetArgs([]string{"list", "--date", "2026-07-01"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "no tasks")
}

func TestListDateUnparsable(t *testing.T) {
	pipeStdin(t, "")
	_, root, _ := newHarness(t, &fakeAssistant{})
	root.SetArgs([]string{"list", "--date", "tomorrow"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage")
}

func TestListDays(t *testing.T) {
	pipeStdin(t, "")
	st, root, buf := newHarness(t, &fakeAssistant{})
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
	_, root, _ := newHarness(t, &fakeAssistant{})
	root.SetArgs([]string{"list", "--date", "2026-08-14", "--days", "3"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestListTagFilter(t *testing.T) {
	pipeStdin(t, "")
	st, root, buf := newHarness(t, &fakeAssistant{})
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
	st, root, buf := newHarness(t, &fakeAssistant{})
	st.Now = today(8, 0)
	_, err := st.Add("plain task")
	require.NoError(t, err)
	root.SetArgs([]string{"list", "--tag", "api"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "no tasks", "tag-less tasks never match")
}

func TestListTagMatchesLiteralTokenOnly(t *testing.T) {
	pipeStdin(t, "")
	st, root, buf := newHarness(t, &fakeAssistant{})
	st.Now = today(8, 0)
	_, err := st.Add("Fixed login bug #auth")
	require.NoError(t, err)
	_, err = st.Add("Call the API about caching")
	require.NoError(t, err)
	_, err = st.Add("Patched the endpoint #fix, among others")
	require.NoError(t, err)

	root.SetArgs([]string{"list", "--tag", "fix"})
	require.NoError(t, root.Execute())
	out := buf.String()
	assert.NotContains(t, out, "#auth", "substring of another tag never matches")
	assert.NotContains(t, out, "Call the API", "plain words never match without a #token")
	assert.Contains(t, out, "#fix", "literal #token matches, trailing punctuation tolerated")
}

func TestListFlattensMultilineTasks(t *testing.T) {
	pipeStdin(t, "")
	st, root, buf := newHarness(t, &fakeAssistant{})
	st.Now = today(8, 0)
	_, err := st.AddWithStatus("fix login bug\n\nThe token was expired.", "done")
	require.NoError(t, err)
	root.SetArgs([]string{"list"})
	require.NoError(t, root.Execute())
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, 1, "multi-line task renders as one row")
	assert.Contains(t, lines[0], "fix login bug The token was expired.")
}

func TestFallbackEditor(t *testing.T) {
	if runtime.GOOS == "windows" {
		assert.Equal(t, "notepad", fallbackEditor())
		return
	}
	assert.Equal(t, "vi", fallbackEditor())
}

func TestGenerateDaysArg(t *testing.T) {
	assistant := &fakeAssistant{genOut: "## Today"}
	st, root, _ := newHarness(t, assistant)
	seedDays(t, st)
	st.Now = today(9, 0)
	root.SetArgs([]string{"generate", "3"})
	require.NoError(t, root.Execute())
	require.Len(t, assistant.genSec.Days, 3)
	assert.Equal(t, []string{"Thu 2026-08-13", "Yesterday", "Today"},
		[]string{assistant.genSec.Days[0].Heading, assistant.genSec.Days[1].Heading, assistant.genSec.Days[2].Heading})
}

func TestGenerateDaysDefaultTwo(t *testing.T) {
	assistant := &fakeAssistant{genOut: "x"}
	st, root, _ := newHarness(t, assistant)
	seedDays(t, st)
	st.Now = today(9, 0)
	root.SetArgs([]string{"generate"})
	require.NoError(t, root.Execute())
	require.Len(t, assistant.genSec.Days, 2)
	assert.NotNil(t, assistant.genSec.Yesterday, "compat fields set for the default window")
}

func TestGenerateBadDaysArg(t *testing.T) {
	for _, arg := range []string{"0", "abc"} {
		_, root, _ := newHarness(t, &fakeAssistant{})
		root.SetArgs([]string{"generate", arg})
		err := root.Execute()
		require.Error(t, err, "arg %q must fail", arg)
		assert.Contains(t, err.Error(), "usage")
	}
}

func TestGenerateCarryOverInPrompt(t *testing.T) {
	assistant := &fakeAssistant{genOut: "x"}
	st, root, _ := newHarness(t, assistant)
	st.Now = func() time.Time { return time.Date(2026, 8, 14, 16, 0, 0, 0, time.Local) }
	unfinished, err := st.Add("finish auth")
	require.NoError(t, err)
	st.Now = today(8, 0)
	root.SetArgs([]string{"generate"})
	require.NoError(t, root.Execute())
	found := false
	for _, tk := range assistant.genSec.Today {
		if tk.ID == unfinished.ID {
			found = true
		}
	}
	assert.True(t, found, "unfinished yesterday task carried into Today")
}

func TestGenerateOutputFile(t *testing.T) {
	assistant := &fakeAssistant{genOut: "## Today\n- did stuff"}
	st, root, buf := newHarness(t, assistant)
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
	assistant := &fakeAssistant{genOut: "short"}
	st, root, _ := newHarness(t, assistant)
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

func TestGenerateFromToWindow(t *testing.T) {
	assistant := &fakeAssistant{genOut: "x"}
	st, root, _ := newHarness(t, assistant)
	seedDays(t, st)
	st.Now = today(9, 0)
	root.SetArgs([]string{"generate", "--from", "2026-08-13", "--to", "2026-08-14"})
	require.NoError(t, root.Execute())
	require.Len(t, assistant.genSec.Days, 2)
	assert.Equal(t, []string{"Thu 2026-08-13", "Fri 2026-08-14"},
		[]string{assistant.genSec.Days[0].Heading, assistant.genSec.Days[1].Heading},
		"explicit historical windows get dated headings, no cutoff")
	assert.Nil(t, assistant.genSec.Yesterday)
}

func TestGenerateFromToUsage(t *testing.T) {
	for _, args := range [][]string{
		{"generate", "--from", "2026-08-13"},
		{"generate", "--to", "2026-08-14"},
		{"generate", "--from", "bogus", "--to", "2026-08-14"},
		{"generate", "--from", "2026-08-14", "--to", "2026-08-13"},
		{"generate", "3", "--from", "2026-08-13", "--to", "2026-08-14"},
	} {
		_, root, _ := newHarness(t, &fakeAssistant{})
		root.SetArgs(args)
		err := root.Execute()
		require.Error(t, err, "args %v must fail", args)
		assert.Contains(t, err.Error(), "usage")
	}
}

func TestGenerateWeekendAwareDefault(t *testing.T) {
	assistant := &fakeAssistant{genOut: "x"}
	st, root, _ := newHarness(t, assistant)
	// Saturday 2026-08-15: the default window is Friday + Saturday.
	st.Now = func() time.Time { return time.Date(2026, 8, 15, 9, 0, 0, 0, time.Local) }
	_, err := st.AddWithStatus("friday work", "done")
	require.NoError(t, err)
	st.Now = func() time.Time { return time.Date(2026, 8, 15, 8, 0, 0, 0, time.Local) }
	// seed a friday task by stamping it directly
	_, err = st.AddAt("friday task", "done", time.Date(2026, 8, 14, 16, 0, 0, 0, time.Local))
	require.NoError(t, err)
	root.SetArgs([]string{"generate"})
	require.NoError(t, root.Execute())
	require.Len(t, assistant.genSec.Days, 2, "Friday + today")
	assert.Equal(t, []string{"friday task"}, taskTextsOf(assistant.genSec.Yesterday))
}

func taskTextsOf(ts []store.Task) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Text
	}
	return out
}

func TestGenerateClip(t *testing.T) {
	assistant := &fakeAssistant{genOut: "## Today\n- did stuff"}
	st, root, _ := newHarness(t, assistant)
	st.Now = today(8, 0)
	_, err := st.Add("fix bug")
	require.NoError(t, err)
	var got string
	old := copyToClipboard
	copyToClipboard = func(text string) error { got = text; return nil }
	t.Cleanup(func() { copyToClipboard = old })
	root.SetArgs([]string{"generate", "--clip"})
	require.NoError(t, root.Execute())
	assert.Equal(t, "## Today\n- did stuff", got)
}

func TestGenerateClipError(t *testing.T) {
	assistant := &fakeAssistant{genOut: "x"}
	st, root, _ := newHarness(t, assistant)
	st.Now = today(8, 0)
	_, err := st.Add("fix bug")
	require.NoError(t, err)
	old := copyToClipboard
	copyToClipboard = func(text string) error { return errors.New("no clipboard") }
	t.Cleanup(func() { copyToClipboard = old })
	root.SetArgs([]string{"generate", "--clip"})
	assert.Error(t, root.Execute(), "clipboard failure surfaces")
}

func TestGenerateWebhookPostsReport(t *testing.T) {
	var gotBody struct{ Text string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		b, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.NoError(t, json.Unmarshal(b, &gotBody))
	}))
	t.Cleanup(srv.Close)

	assistant := &fakeAssistant{genOut: "## Today\n- [done] ship (08:00)"}
	st, root, _ := newHarness(t, assistant)
	st.Now = today(8, 0)
	_, err := st.Add("ship")
	require.NoError(t, err)
	root.SetArgs([]string{"generate", "--webhook", srv.URL})
	require.NoError(t, root.Execute())
	assert.Equal(t, "## Today\n- [done] ship (08:00)", gotBody.Text,
		"the report is posted as Slack-compatible JSON text")
}

func TestGenerateWebhookFailureSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	assistant := &fakeAssistant{genOut: "x"}
	st, root, _ := newHarness(t, assistant)
	st.Now = today(8, 0)
	_, err := st.Add("ship")
	require.NoError(t, err)
	root.SetArgs([]string{"generate", "--webhook", srv.URL})
	err = root.Execute()
	require.Error(t, err, "webhook failure surfaces")
	assert.Contains(t, err.Error(), "webhook")
}

// mailHarness is newHarness plus SMTP config — mail needs smtp_* settings.
func mailHarness(t *testing.T, assistant *fakeAssistant) (*store.Store, *cobra.Command) {
	t.Helper()
	assistant.genSec = &report.Section{}
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	root := New(func() (Deps, error) {
		return Deps{
			Assistant: func() (agent.Assistant, error) { return assistant, nil },
			Raw:       assistant,
			Store:     st,
			Config: config.Config{
				MeetingTime: "09:30",
				SMTPHost:    "smtp.example.com",
				SMTPPort:    587,
				SMTPUser:    "me@example.com",
				MailFrom:    "standup@example.com",
			},
		}, nil
	})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	return st, root
}

func TestGenerateMailSendsReport(t *testing.T) {
	var gotAddr, gotFrom, gotMsg string
	var gotTo []string
	old := smtpSend
	smtpSend = func(addr string, _ smtp.Auth, from string, to []string, msg []byte) error {
		gotAddr, gotFrom, gotTo, gotMsg = addr, from, to, string(msg)
		return nil
	}
	t.Cleanup(func() { smtpSend = old })

	assistant := &fakeAssistant{genOut: "## Today\n- [done] ship (08:00)"}
	st, root := mailHarness(t, assistant)
	st.Now = today(8, 0)
	_, err := st.Add("ship")
	require.NoError(t, err)
	root.SetArgs([]string{"generate", "--mail", "team@example.com"})
	require.NoError(t, root.Execute())

	assert.Equal(t, "smtp.example.com:587", gotAddr)
	assert.Equal(t, "standup@example.com", gotFrom, "mail_from wins over smtp_user")
	assert.Equal(t, []string{"team@example.com"}, gotTo)
	assert.Contains(t, gotMsg, "Subject: standup")
	assert.Contains(t, gotMsg, "\r\n\r\n## Today\r\n- [done] ship (08:00)", "LF normalized to CRLF for mail")
}

func TestGenerateMailFailureSurfaces(t *testing.T) {
	old := smtpSend
	smtpSend = func(string, smtp.Auth, string, []string, []byte) error { return errors.New("boom") }
	t.Cleanup(func() { smtpSend = old })

	assistant := &fakeAssistant{genOut: "x"}
	st, root := mailHarness(t, assistant)
	st.Now = today(8, 0)
	_, err := st.Add("ship")
	require.NoError(t, err)
	root.SetArgs([]string{"generate", "--mail", "team@example.com"})
	err = root.Execute()
	require.Error(t, err, "SMTP failure surfaces")
	assert.Contains(t, err.Error(), "mail")
}

func TestGenerateMailRequiresSMTPHost(t *testing.T) {
	old := smtpSend
	smtpSend = func(string, smtp.Auth, string, []string, []byte) error {
		t.Fatal("smtpSend must not run without a configured host")
		return nil
	}
	t.Cleanup(func() { smtpSend = old })

	assistant := &fakeAssistant{genOut: "x"}
	st, root, _ := newHarness(t, assistant) // no smtp_* config
	st.Now = today(8, 0)
	_, err := st.Add("ship")
	require.NoError(t, err)
	root.SetArgs([]string{"generate", "--mail", "team@example.com"})
	err = root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "smtp_host")
}

func TestDoctorChecks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)
	srv.Close() // unreachable variant below uses the closed URL

	good := t.TempDir()
	assert.NoError(t, checkWritable(filepath.Join(good, "tasks.jsonl")))
	assert.NoError(t, checkWritable(filepath.Join(good, "fresh", "nested", "tasks.jsonl")),
		"doctor is the first command after installing — the data dir does not exist yet")

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(live.Close)
	assert.NoError(t, reachable(live.URL))
	assert.Error(t, reachable(srv.URL), "closed endpoint is unreachable")
}

func TestDoctorOfflineSkipsEndpoint(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	st.Now = today(8, 0)
	oldIdent := gitIdentity
	gitIdentity = func(dir string) (string, error) { return "me@example.com", nil }
	t.Cleanup(func() { gitIdentity = oldIdent })
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_MODEL", "")

	assistant := &fakeAssistant{}
	buf := &bytes.Buffer{}
	root := New(func() (Deps, error) {
		return Deps{
			Assistant: func() (agent.Assistant, error) { return assistant, nil },
			Raw:       assistant,
			Store:     st,
			Config:    config.Config{MeetingTime: "09:30", Offline: true, DataFile: st.Path},
		}, nil
	})
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"doctor"})
	require.NoError(t, root.Execute(), "offline doctor never requires endpoint env")
	assert.Contains(t, buf.String(), "offline mode")
	assert.Contains(t, buf.String(), "ok   data file writable")
	assert.Contains(t, buf.String(), "ok   git identity (me@example.com)")
}

func TestDoctorFailsOnMissingEnv(t *testing.T) {
	pipeStdin(t, "")
	st, _, _ := newHarness(t, &fakeAssistant{})
	st.Now = today(8, 0)
	oldIdent := gitIdentity
	gitIdentity = func(dir string) (string, error) { return "me@example.com", nil }
	t.Cleanup(func() { gitIdentity = oldIdent })
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_MODEL", "")
	buf := &bytes.Buffer{}
	root := New(func() (Deps, error) {
		return Deps{
			Assistant: func() (agent.Assistant, error) { return &fakeAssistant{}, nil },
			Raw:       &fakeAssistant{},
			Store:     st,
			Config:    config.Config{MeetingTime: "09:30", DataFile: filepath.Join(t.TempDir(), "tasks.jsonl")},
		}, nil
	})
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"doctor"})
	err := root.Execute()
	require.Error(t, err, "missing provider env is a failure in online mode")
	assert.Contains(t, buf.String(), "fail env OPENAI_BASE_URL")
}

func TestGenerateOutputFileUnwritable(t *testing.T) {
	assistant := &fakeAssistant{genOut: "x"}
	st, root, _ := newHarness(t, assistant)
	st.Now = today(8, 0)
	_, err := st.Add("fix bug")
	require.NoError(t, err)
	root.SetArgs([]string{"generate", "-o", filepath.Join(t.TempDir(), "no-such-dir", "out.md")})
	assert.Error(t, root.Execute(), "unwritable path surfaces as command failure")
}

func TestGenerateHonorsConfiguredTimezone(t *testing.T) {
	assistant := &fakeAssistant{genOut: "x"}
	assistant.genSec = &report.Section{}
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	// Fri 23:30 UTC = Sat 08:30 in Tokyo — before the 09:30 meeting cutoff,
	// so in Tokyo Friday is still in the window; in UTC it is not.
	st.Now = func() time.Time { return time.Date(2026, 8, 14, 23, 30, 0, 0, time.UTC) }
	_, err = st.Add("ship")
	require.NoError(t, err)
	root := New(func() (Deps, error) {
		return Deps{
			Assistant: func() (agent.Assistant, error) { return assistant, nil },
			Raw:       assistant,
			Store:     st,
			Config:    config.Config{MeetingTime: "09:30", Timezone: "Asia/Tokyo"},
		}, nil
	})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"generate"})
	require.NoError(t, root.Execute())
	assert.Len(t, assistant.genSec.Days, 2, "window computed in the configured timezone, not the machine's")
}

func TestGenerateBadTimezoneFails(t *testing.T) {
	assistant := &fakeAssistant{genOut: "x"}
	assistant.genSec = &report.Section{}
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	root := New(func() (Deps, error) {
		return Deps{
			Assistant: func() (agent.Assistant, error) { return assistant, nil },
			Raw:       assistant,
			Store:     st,
			Config:    config.Config{MeetingTime: "09:30", Timezone: "Mars/Olympus"},
		}, nil
	})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"generate"})
	err = root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timezone")
}

func TestGenerateBlockedSection(t *testing.T) {
	assistant := &fakeAssistant{genOut: "x"}
	st, root, _ := newHarness(t, assistant)
	st.Now = func() time.Time { return time.Date(2026, 8, 14, 16, 0, 0, 0, time.Local) }
	blocked, err := st.AddWithStatus("waiting on infra", "blocked")
	require.NoError(t, err)
	st.Now = today(9, 0)
	root.SetArgs([]string{"generate"})
	require.NoError(t, root.Execute())
	require.Len(t, assistant.genSec.Blockers, 1)
	assert.Equal(t, blocked.ID, assistant.genSec.Blockers[0].ID)
}

func TestCommitsStampsCommitTime(t *testing.T) {
	assistant := &fakeAssistant{}
	st, root, buf := newHarness(t, assistant)
	st.Now = today(9, 0)
	fri := time.Date(2026, 8, 14, 10, 0, 0, 0, time.Local)
	old := gitLog
	gitLog = func(dir string, since time.Time) ([]git.Commit, error) {
		assert.Equal(t, ".", dir)
		assert.True(t, since.Equal(time.Date(2026, 8, 14, 0, 0, 0, 0, time.Local)), "default lookback: last working day")
		return []git.Commit{
			{Hash: "h1", Subject: "fix login bug", Body: "fix login bug", When: fri},
			{Hash: "h2", Subject: "write tests", Body: "write tests", When: time.Date(2026, 8, 14, 16, 0, 0, 0, time.Local)},
		}, nil
	}
	t.Cleanup(func() { gitLog = old })
	root.SetArgs([]string{"commits"})
	require.NoError(t, root.Execute())

	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.True(t, tasks[0].Timestamp.Equal(fri), "task timestamp is the commit time, not the import time")
	assert.Equal(t, "done", tasks[0].Status, "shipped commits land as done")
	assert.Equal(t, "fix login bug", tasks[0].Text)
	assert.Contains(t, buf.String(), "- [done] fix login bug")
	assert.Empty(t, assistant.added, "commit ingestion is deterministic — no model involved")
}

func TestFilterRepos(t *testing.T) {
	cases := []struct {
		name            string
		paths, inc, exc []string
		want            []string
	}{
		{"no globs keeps all", []string{".", "lib"}, nil, nil, []string{".", "lib"}},
		{"include keeps only matches", []string{"api", "vendor"}, []string{"a*"}, nil, []string{"api"}},
		{"exclude drops matches", []string{"api", "vendor"}, nil, []string{"v*"}, []string{"api"}},
		{"exclude beats include", []string{"api", "alt"}, []string{"a*"}, []string{"alt"}, []string{"api"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := filterRepos(tc.paths, config.Config{ReposInclude: tc.inc, ReposExclude: tc.exc})
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	t.Run("bad pattern surfaces", func(t *testing.T) {
		_, err := filterRepos([]string{"x"}, config.Config{ReposExclude: []string{"["}})
		assert.ErrorContains(t, err, "repos glob")
	})
}

func TestCommitsReposGlobsFilterCollection(t *testing.T) {
	assistant := &fakeAssistant{}
	assistant.genSec = &report.Section{}
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	root := New(func() (Deps, error) {
		return Deps{
			Assistant: func() (agent.Assistant, error) { return assistant, nil },
			Raw:       assistant,
			Store:     st,
			Config:    config.Config{MeetingTime: "09:30", ReposExclude: []string{"*/vendor", "vendor"}},
		}, nil
	})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)

	var queried []string
	oldLog, oldSubs := gitLog, gitSubmodules
	gitLog = func(dir string, since time.Time) ([]git.Commit, error) {
		queried = append(queried, dir)
		return nil, nil
	}
	gitSubmodules = func(dir string) ([]string, error) {
		if dir == "." {
			return []string{"vendor"}, nil
		}
		return nil, nil
	}
	t.Cleanup(func() { gitLog, gitSubmodules = oldLog, oldSubs })
	root.SetArgs([]string{"commits"})
	require.NoError(t, root.Execute())
	assert.Equal(t, []string{"."}, queried, "excluded submodule never reaches the collector")
}

func TestCommitsBranchFlagRecords(t *testing.T) {
	st, root, _ := newHarness(t, &fakeAssistant{})
	old := gitLog
	gitLog = func(dir string, since time.Time) ([]git.Commit, error) {
		return []git.Commit{{Hash: "h1", Body: "feat: x", Branch: "main", When: time.Date(2026, 8, 14, 10, 0, 0, 0, time.Local)}}, nil
	}
	t.Cleanup(func() { gitLog = old })
	root.SetArgs([]string{"commits", "--branch"})
	require.NoError(t, root.Execute())
	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "main", tasks[0].Branch, "--branch records the commit's branch")
}

func TestCommitsWithoutBranchFlagStaysBlank(t *testing.T) {
	st, root, _ := newHarness(t, &fakeAssistant{})
	old := gitLog
	gitLog = func(dir string, since time.Time) ([]git.Commit, error) {
		return []git.Commit{{Hash: "h1", Body: "feat: x", Branch: "main", When: time.Date(2026, 8, 14, 10, 0, 0, 0, time.Local)}}, nil
	}
	t.Cleanup(func() { gitLog = old })
	root.SetArgs([]string{"commits"})
	require.NoError(t, root.Execute())
	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Empty(t, tasks[0].Branch, "no flag, no attribution — rows stay clean")
}

func TestListShowsBranchWhenRecorded(t *testing.T) {
	st, root, buf := newHarness(t, &fakeAssistant{})
	st.Now = today(8, 0)
	added, err := st.Add("feat: x")
	require.NoError(t, err)
	_, err = st.SetBranch(added.ID, "main")
	require.NoError(t, err)
	root.SetArgs([]string{"list"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "[main]", "list rows attribute the branch when recorded")
}

func TestCommitsAllAuthorsRecordsAuthor(t *testing.T) {
	st, root, _ := newHarness(t, &fakeAssistant{})
	old := gitLogAll
	gitLogAll = func(dir string, since time.Time) ([]git.Commit, error) {
		return []git.Commit{
			{Hash: "h1", Body: "alice work", Author: "alice@example.com", When: time.Date(2026, 8, 14, 10, 0, 0, 0, time.Local)},
			{Hash: "h2", Body: "bob work", Author: "bob@example.com", When: time.Date(2026, 8, 14, 11, 0, 0, 0, time.Local)},
		}, nil
	}
	t.Cleanup(func() { gitLogAll = old })
	root.SetArgs([]string{"commits", "--all-authors"})
	require.NoError(t, root.Execute())
	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, "alice@example.com", tasks[0].Author)
	assert.Equal(t, "bob@example.com", tasks[1].Author)
}

func TestCommitsWithoutAllAuthorsUsesPersonalLog(t *testing.T) {
	st, root, _ := newHarness(t, &fakeAssistant{})
	old, oldAll := gitLog, gitLogAll
	gitLog = func(dir string, since time.Time) ([]git.Commit, error) {
		return []git.Commit{{Hash: "h1", Body: "my work", Author: "me@example.com", When: time.Date(2026, 8, 14, 10, 0, 0, 0, time.Local)}}, nil
	}
	gitLogAll = func(dir string, since time.Time) ([]git.Commit, error) {
		t.Fatal("without --all-authors the personal log is used")
		return nil, nil
	}
	t.Cleanup(func() { gitLog, gitLogAll = old, oldAll })
	root.SetArgs([]string{"commits"})
	require.NoError(t, root.Execute())
	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Empty(t, tasks[0].Author, "personal imports stay unattributed")
}

func TestGenerateTeamGroupsByAuthor(t *testing.T) {
	assistant := &fakeAssistant{genOut: "- [done] work (10:00)"}
	st, root, buf := newHarness(t, assistant)
	st.Now = today(8, 0)
	for _, seed := range []struct{ text, author string }{
		{"alice work", "alice@example.com"},
		{"bob work", "bob@example.com"},
		{"my manual task", ""},
	} {
		tk, err := st.Add(seed.text)
		require.NoError(t, err)
		if seed.author != "" {
			_, err = st.SetAuthor(tk.ID, seed.author)
			require.NoError(t, err)
		}
	}
	root.SetArgs([]string{"generate", "--team"})
	require.NoError(t, root.Execute())
	require.Len(t, assistant.genSecs, 3, "one render per author group plus the unattributed group")
	assert.Contains(t, buf.String(), "## alice@example.com")
	assert.Contains(t, buf.String(), "## bob@example.com")
	for _, sec := range assistant.genSecs {
		for _, day := range sec.Days {
			for i := 1; i < len(day.Tasks); i++ {
				assert.Equal(t, day.Tasks[0].Author, day.Tasks[i].Author, "every section is single-author")
			}
		}
	}
}

func TestGenerateTeamWithoutAuthorsRendersSolo(t *testing.T) {
	assistant := &fakeAssistant{genOut: "- [done] work (10:00)"}
	st, root, buf := newHarness(t, assistant)
	st.Now = today(8, 0)
	_, err := st.Add("plain task")
	require.NoError(t, err)
	root.SetArgs([]string{"generate", "--team"})
	require.NoError(t, root.Execute())
	require.Len(t, assistant.genSecs, 1, "no recorded authors: one section, no headings")
	assert.NotContains(t, buf.String(), "@example.com")
}

func TestCommitsIncludesSubmodules(t *testing.T) {
	st, root, _ := newHarness(t, &fakeAssistant{})
	var queried []string
	oldLog, oldSubs := gitLog, gitSubmodules
	gitLog = func(dir string, since time.Time) ([]git.Commit, error) {
		queried = append(queried, dir)
		return []git.Commit{{Hash: "h-" + dir, Body: "work in " + dir, When: time.Date(2026, 8, 14, 10, 0, 0, 0, time.Local)}}, nil
	}
	gitSubmodules = func(dir string) ([]string, error) {
		if dir == "." {
			return []string{"lib"}, nil
		}
		return nil, nil
	}
	t.Cleanup(func() { gitLog, gitSubmodules = oldLog, oldSubs })
	root.SetArgs([]string{"commits"})
	require.NoError(t, root.Execute())
	assert.Equal(t, []string{".", "lib"}, queried, "submodule paths feed the collector as extra repos")
	tasks, err := st.List()
	require.NoError(t, err)
	assert.Len(t, tasks, 2, "commits from both the repo and its submodule are imported")
}

func TestCommitsMultiRepoDedupesAndSorts(t *testing.T) {
	st, root, _ := newHarness(t, &fakeAssistant{})
	dirs := []string{t.TempDir(), t.TempDir()}
	for _, d := range dirs {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	old := gitLog
	gitLog = func(dir string, since time.Time) ([]git.Commit, error) {
		switch dir {
		case dirs[0]:
			return []git.Commit{{Hash: "b", Body: "later", When: time.Date(2026, 8, 14, 16, 0, 0, 0, time.Local)}}, nil
		default:
			return []git.Commit{{Hash: "a", Body: "earlier", When: time.Date(2026, 8, 14, 9, 0, 0, 0, time.Local)}}, nil
		}
	}
	t.Cleanup(func() { gitLog = old })
	root.SetArgs([]string{"commits", dirs[0], dirs[1]})
	require.NoError(t, root.Execute())
	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, "earlier", tasks[0].Text, "commits ordered by time across repos")
}

func TestCommitsSkipsAlreadyImported(t *testing.T) {
	st, root, buf := newHarness(t, &fakeAssistant{})
	st.Now = today(9, 0)
	when := time.Date(2026, 8, 14, 10, 0, 0, 0, time.Local)
	_, err := st.AddAt("fix login bug", "done", when)
	require.NoError(t, err)
	old := gitLog
	gitLog = func(dir string, since time.Time) ([]git.Commit, error) {
		return []git.Commit{{Hash: "h1", Body: "fix login bug", When: when}}, nil
	}
	t.Cleanup(func() { gitLog = old })
	root.SetArgs([]string{"commits"})
	require.NoError(t, root.Execute())
	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 1, "re-running commits never duplicates tasks")
	assert.Contains(t, buf.String(), "skipped 1 already imported")
}

func TestCommitsContinuesPastBadCommit(t *testing.T) {
	st, root, buf := newHarness(t, &fakeAssistant{})
	old := gitLog
	gitLog = func(dir string, since time.Time) ([]git.Commit, error) {
		return []git.Commit{
			{Hash: "bad", Body: "", When: time.Date(2026, 8, 14, 10, 0, 0, 0, time.Local)},
			{Hash: "good", Body: "feat: add login", When: time.Date(2026, 8, 14, 11, 0, 0, 0, time.Local)},
		}, nil
	}
	t.Cleanup(func() { gitLog = old })
	root.SetArgs([]string{"commits"})
	require.NoError(t, root.Execute(), "one bad commit must not abort the import")
	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 1, "the valid commit after the bad one is still imported")
	assert.Equal(t, "feat: add login", tasks[0].Text)
	assert.Contains(t, buf.String(), "skipped", "the bad commit is reported, not silently dropped")
}

func TestCommitsDaysArg(t *testing.T) {
	_, root, _ := newHarness(t, &fakeAssistant{})
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
	_, root, _ := newHarness(t, &fakeAssistant{})
	root.SetArgs([]string{"commits", "zero"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage")
}

func TestCommitsEmptyHintsAtIdentity(t *testing.T) {
	_, root, buf := newHarness(t, &fakeAssistant{})
	old := gitLog
	gitLog = func(dir string, since time.Time) ([]git.Commit, error) { return nil, nil }
	t.Cleanup(func() { gitLog = old })
	root.SetArgs([]string{"commits"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "no commits found since")
	assert.Contains(t, buf.String(), "user.email", "zero-match hint names the likely cause")
}

func TestEditArg(t *testing.T) {
	assistant := &fakeAssistant{}
	st, root, _ := newHarness(t, assistant)
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
	assistant := &fakeAssistant{}
	st, root, _ := newHarness(t, assistant)
	st.Now = today(8, 0)
	added, err := st.Add("keep me")
	require.NoError(t, err)
	root.SetArgs([]string{"edit", added.ID, "   "})
	err = root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty task text")
}

func TestEditMissingIDUsage(t *testing.T) {
	_, root, _ := newHarness(t, &fakeAssistant{})
	root.SetArgs([]string{"edit"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage: standup edit <id>")
}

func TestEditViaEditor(t *testing.T) {
	assistant := &fakeAssistant{}
	st, root, _ := newHarness(t, assistant)
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
	assistant := &fakeAssistant{}
	st, root, _ := newHarness(t, assistant)
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

func TestDoneEchoesRow(t *testing.T) {
	st, root, buf := newHarness(t, &fakeAssistant{})
	added, err := st.Add("ship it")
	require.NoError(t, err)
	root.SetArgs([]string{"done", added.ID})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "- [done] ship it")
}

func TestRmEchoesRow(t *testing.T) {
	st, root, buf := newHarness(t, &fakeAssistant{})
	added, err := st.Add("ship it")
	require.NoError(t, err)
	root.SetArgs([]string{"rm", added.ID})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "- removed: ship it")
}

func TestEditEchoesRow(t *testing.T) {
	st, root, buf := newHarness(t, &fakeAssistant{})
	st.Now = today(8, 0)
	added, err := st.Add("fixd typo")
	require.NoError(t, err)
	root.SetArgs([]string{"edit", added.ID, "fixed typo"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "- [todo] fixed typo")
}

func TestStatusSetsStatus(t *testing.T) {
	st, root, buf := newHarness(t, &fakeAssistant{})
	added, err := st.Add("waiting on infra")
	require.NoError(t, err)
	root.SetArgs([]string{"status", added.ID, "blocked"})
	require.NoError(t, root.Execute())
	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "blocked", tasks[0].Status)
	assert.Contains(t, buf.String(), "- [blocked] waiting on infra")

	root.SetArgs([]string{"status", added.ID, "in-progress"})
	require.NoError(t, root.Execute(), "blocked tasks can be unblocked")
	tasks, err = st.List()
	require.NoError(t, err)
	assert.Equal(t, "in-progress", tasks[0].Status)
}

func TestStatusInvalidRejected(t *testing.T) {
	st, root, _ := newHarness(t, &fakeAssistant{})
	added, err := st.Add("keep")
	require.NoError(t, err)
	root.SetArgs([]string{"status", added.ID, "bogus"})
	err = root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid status")
}

func TestStatusUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"one"}} {
		_, root, _ := newHarness(t, &fakeAssistant{})
		root.SetArgs(append([]string{"status"}, args...))
		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "usage: standup status <id> <status>")
	}
}

// rawAss fails if the model path is taken; the deterministic assistant must
// be used instead.
type rawAss struct{ called *bool }

func (r *rawAss) AddTasks(ctx context.Context, rawText string) ([]store.Task, error) {
	*r.called = true
	return nil, errors.New("model must not be called with --raw")
}

func (r *rawAss) Generate(ctx context.Context, sec report.Section) (string, error) {
	return "", nil
}

var _ agent.Assistant = (*rawAss)(nil)

func TestColorReport(t *testing.T) {
	in := "## Today\n- [todo] a (09:00)\n- [done] b (10:00)\n- [in-progress] c (11:00)\n- [blocked] d (12:00)\n"
	on := colorReport(in, painter{on: true})
	assert.Contains(t, on, "["+painter{on: true}.status("todo")+"]")
	assert.Contains(t, on, "["+painter{on: true}.status("done")+"]")
	assert.Contains(t, on, "["+painter{on: true}.status("in-progress")+"]")
	assert.Contains(t, on, "["+painter{on: true}.status("blocked")+"]")
	assert.Equal(t, in, colorReport(in, painter{on: false}), "colors off: verbatim")
}

func TestAddEchoesStatus(t *testing.T) {
	assistant := &fakeAssistant{addResult: []store.Task{{ID: "x", Text: "hello world", Status: "todo"}}}
	_, root, buf := newHarness(t, assistant)
	root.SetArgs([]string{"add", "hello world"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "- [todo] hello world")
}

func TestAddRawBypassesModel(t *testing.T) {
	called := false
	model := &rawAss{called: &called}
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	raw, err := agent.Local(config.Config{
		GenerateInputTemplate: "x",
		DaysTemplate:          "x",
	}, st)
	require.NoError(t, err)
	buf := &bytes.Buffer{}
	root := New(func() (Deps, error) {
		return Deps{Assistant: func() (agent.Assistant, error) { return model, nil }, Raw: raw, Store: st, Config: config.Config{MeetingTime: "09:30"}}, nil
	})
	root.SetOut(buf)
	root.SetErr(buf)

	root.SetArgs([]string{"add", "--raw", "verbatim one\n\nverbatim two"})
	require.NoError(t, root.Execute())
	assert.False(t, called, "--raw never contacts the model")
	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, "verbatim one", tasks[0].Text, "text stored verbatim, paragraph-split")
	assert.Equal(t, "verbatim two", tasks[1].Text)
}

func TestReadOnlyCommandsSkipAssistant(t *testing.T) {
	old := gitLog
	gitLog = func(dir string, since time.Time) ([]git.Commit, error) { return nil, nil }
	t.Cleanup(func() { gitLog = old })

	for name, args := range map[string][]string{
		"list":    {"list"},
		"done":    {"done", "%s"},
		"rm":      {"rm", "%s"},
		"status":  {"status", "%s", "done"},
		"edit":    {"edit", "%s", "new text"},
		"commits": {"commits"},
	} {
		t.Run(name, func(t *testing.T) {
			pipeStdin(t, "")
			called := false
			st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
			require.NoError(t, err)
			st.Now = today(8, 0)
			added, err := st.Add("some task")
			require.NoError(t, err)
			buf := &bytes.Buffer{}
			root := New(func() (Deps, error) {
				return Deps{
					Assistant: func() (agent.Assistant, error) {
						called = true
						return nil, errors.New("credentials must not be required here")
					},
					Raw:    &fakeAssistant{},
					Store:  st,
					Config: config.Config{MeetingTime: "09:30"},
				}, nil
			})
			root.SetOut(buf)
			root.SetErr(buf)
			var final []string
			for _, a := range args {
				final = append(final, strings.ReplaceAll(a, "%s", added.ID))
			}
			root.SetArgs(final)
			require.NoError(t, root.Execute(), "%s must not need credentials", name)
			assert.False(t, called)
		})
	}
}

func TestAddUsesAssistant(t *testing.T) {
	pipeStdin(t, "")
	_, root, _ := newHarness(t, &fakeAssistant{addResult: []store.Task{{ID: "1", Text: "cleaned"}}})
	root.SetArgs([]string{"add", "raw text"})
	require.NoError(t, root.Execute())
}

func TestHelpAndVersionNeverLoadConfig(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--version"}, {}, {"help"}} {
		root := New(func() (Deps, error) { return Deps{}, errors.New("config must not load") })
		root.Version = "test"
		buf := &bytes.Buffer{}
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs(args)
		require.NoError(t, root.Execute(), "args %v must not touch config", args)
	}
}

func TestInitCmdWritesDefaults(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	unsetCliEnv(t, "STANDUP_CONFIG_DIR")
	_, root, buf := newHarness(t, &fakeAssistant{})
	root.SetArgs([]string{"init"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), filepath.Join(xdg, "standup"))
	for _, name := range []string{"config.yaml", "agent.yaml"} {
		_, err := os.Stat(filepath.Join(xdg, "standup", name))
		require.NoError(t, err, "%s written", name)
	}
}

func TestUpdateAvailable(t *testing.T) {
	old := latestTag
	latestTag = func() (string, error) { return "v0.6.0", nil }
	t.Cleanup(func() { latestTag = old })
	_, root, buf := newHarness(t, &fakeAssistant{})
	root.Version = "0.5.0"
	root.SetArgs([]string{"update"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "update available: v0.6.0 (current: v0.5.0)")
	assert.Contains(t, buf.String(), "install.sh", "the message says how to update")
}

func TestUpdateUpToDate(t *testing.T) {
	old := latestTag
	latestTag = func() (string, error) { return "v0.5.0", nil }
	t.Cleanup(func() { latestTag = old })
	_, root, buf := newHarness(t, &fakeAssistant{})
	root.Version = "0.5.0"
	root.SetArgs([]string{"update"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "up to date (v0.5.0)")
}

func TestUpdateNetworkErrorFails(t *testing.T) {
	old := latestTag
	latestTag = func() (string, error) { return "", errors.New("no network") }
	t.Cleanup(func() { latestTag = old })
	_, root, _ := newHarness(t, &fakeAssistant{})
	root.Version = "0.5.0"
	root.SetArgs([]string{"update"})
	assert.Error(t, root.Execute())
}

func TestLatestTagFollowsRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://github.com/x/standup/releases/tag/v0.6.0")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	oldURL, oldTag := releasesLatestURL, latestTag
	releasesLatestURL = srv.URL
	latestTag = func() (string, error) { return latestReleaseTag() }
	t.Cleanup(func() { releasesLatestURL, latestTag = oldURL, oldTag })

	tag, err := latestTag()
	require.NoError(t, err)
	assert.Equal(t, "v0.6.0", tag)
}

func TestSkillInstallWritesBothRoots(t *testing.T) {
	unsetCliEnv(t, "STANDUP_CONFIG_DIR")
	repo := t.TempDir()
	t.Chdir(repo)
	root := New(func() (Deps, error) { return Deps{}, errors.New("skill install never loads deps") })
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"skill", "install"})
	require.NoError(t, root.Execute())

	for _, p := range []string{
		filepath.Join(".agents", "skills", "standup", "SKILL.md"),
		filepath.Join(".claude", "skills", "standup", "SKILL.md"),
	} {
		b, err := os.ReadFile(filepath.Join(repo, p))
		require.NoError(t, err, "%s written as a real file (symlinks break on Windows)", p)
		assert.Equal(t, defaults.SkillMD, string(b), "%s carries the embedded skill verbatim", p)
	}
	assert.Contains(t, buf.String(), filepath.Join(".agents", "skills", "standup", "SKILL.md"))

	require.NoError(t, root.Execute(), "second install refreshes in place (idempotent)")
}

func TestSkillInstallGlobalUsesHome(t *testing.T) {
	unsetCliEnv(t, "STANDUP_CONFIG_DIR")
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	t.Chdir(repo)
	root := New(func() (Deps, error) { return Deps{}, errors.New("skill install never loads deps") })
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"skill", "install", "--global"})
	require.NoError(t, root.Execute())

	for _, p := range []string{
		filepath.Join(home, ".agents", "skills", "standup", "SKILL.md"),
		filepath.Join(home, ".claude", "skills", "standup", "SKILL.md"),
	} {
		b, err := os.ReadFile(p)
		require.NoError(t, err, "global skill written to %s", p)
		assert.Equal(t, defaults.SkillMD, string(b))
	}
	entries, err := os.ReadDir(repo)
	require.NoError(t, err)
	assert.Empty(t, entries, "--global never touches the repo")
	assert.Contains(t, buf.String(), filepath.Join(home, ".agents", "skills", "standup", "SKILL.md"))
}

func TestSkillInstallUsage(t *testing.T) {
	unsetCliEnv(t, "STANDUP_CONFIG_DIR")
	t.Chdir(t.TempDir())
	root := New(func() (Deps, error) { return Deps{}, nil })
	root.SetArgs([]string{"skill"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage: standup skill install")
}

func unsetCliEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		old, ok := os.LookupEnv(k)
		require.NoError(t, os.Unsetenv(k))
		if ok {
			t.Cleanup(func() {
				if err := os.Setenv(k, old); err != nil {
					t.Errorf("restore %s: %v", k, err)
				}
			})
		}
	}
}
