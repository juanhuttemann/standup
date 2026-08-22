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
	"standup/internal/sync"
	standupupdate "standup/internal/update"
)

type fakeAssistant struct {
	added            []string
	addResult        []store.Task
	addErr           error
	genCalls         int
	genSec           *report.Section
	genSecs          []report.Section
	genOut           string
	genFallback      string
	genErr           error
	scriptCalls      int
	scriptRep        string
	scriptOut        string
	scriptErr        error
	synthCalls       int
	synthScript      string
	synthAudio       []byte
	synthErr         error
	planCalls        int
	planPrompt       string
	planTasks        []store.Task
	planNow          time.Time
	planResult       []store.BatchOperation
	planErr          error
	planProgress     []string
	planHold         time.Duration
	planVerboseCalls int
}

func (f *fakeAssistant) Plan(ctx context.Context, prompt string, tasks []store.Task, now time.Time) ([]store.BatchOperation, error) {
	if f.planHold > 0 {
		select {
		case <-time.After(f.planHold):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.planCalls++
	f.planPrompt = prompt
	f.planTasks = append([]store.Task(nil), tasks...)
	f.planNow = now
	return f.planResult, f.planErr
}

// PlanWithProgress is the minimal opt-in API needed by prompt --verbose. The
// regular Assistant interface remains unchanged; CLI can type-assert this
// capability only when verbose output was requested.
func (f *fakeAssistant) PlanWithProgress(ctx context.Context, prompt string, tasks []store.Task, now time.Time, progress func(string)) ([]store.BatchOperation, error) {
	f.planVerboseCalls++
	f.planPrompt = prompt
	f.planTasks = append([]store.Task(nil), tasks...)
	f.planNow = now
	for _, message := range f.planProgress {
		progress(message)
	}
	return f.planResult, f.planErr
}

func (f *fakeAssistant) AddTasks(ctx context.Context, rawText string) ([]store.Task, error) {
	f.added = append(f.added, rawText)
	if f.addErr != nil {
		return nil, f.addErr
	}
	return f.addResult, nil
}

func (f *fakeAssistant) Generate(ctx context.Context, sec report.Section) (agent.Generated, error) {
	f.genCalls++
	*f.genSec = sec
	f.genSecs = append(f.genSecs, sec)
	return agent.Generated{Text: f.genOut, Fallback: f.genFallback}, f.genErr
}

func (f *fakeAssistant) Script(ctx context.Context, report string) (string, error) {
	f.scriptCalls++
	f.scriptRep = report
	return f.scriptOut, f.scriptErr
}

func (f *fakeAssistant) Synthesize(ctx context.Context, script string) ([]byte, error) {
	f.synthCalls++
	f.synthScript = script
	return f.synthAudio, f.synthErr
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

func TestSpeak(t *testing.T) {
	assistant := &fakeAssistant{
		genOut:     "## Yesterday\n- did stuff",
		scriptOut:  "Yesterday I did stuff.",
		synthAudio: []byte("MP3BYTES"),
	}
	st, root, buf := newHarness(t, assistant)
	st.Now = today(7, 0)
	_, err := st.Add("did stuff")
	require.NoError(t, err)
	st.Now = today(8, 0)
	out := filepath.Join(t.TempDir(), "standup.mp3")
	root.SetArgs([]string{"speak", "-o", out})
	require.NoError(t, root.Execute())
	assert.Equal(t, 1, assistant.scriptCalls)
	assert.Contains(t, assistant.scriptRep, "did stuff", "the rendered report feeds the speaker")
	assert.Equal(t, 1, assistant.synthCalls)
	assert.Equal(t, "Yesterday I did stuff.", assistant.synthScript, "TTS narrates the printed script")
	audio, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, []byte("MP3BYTES"), audio, "Go writes the audio bytes deterministically")
	assert.Contains(t, buf.String(), "Yesterday I did stuff.", "the script is printed")
	assert.Contains(t, buf.String(), out, "the written path is echoed")
}

func TestSpeakPreviewSkipsSynthesis(t *testing.T) {
	assistant := &fakeAssistant{genOut: "## Today\n- x", scriptOut: "Today I did x."}
	st, root, buf := newHarness(t, assistant)
	st.Now = today(8, 0)
	_, err := st.Add("x")
	require.NoError(t, err)
	t.Chdir(t.TempDir())
	root.SetArgs([]string{"speak"})
	require.NoError(t, root.Execute())
	assert.Equal(t, 1, assistant.scriptCalls)
	assert.Equal(t, 0, assistant.synthCalls, "no -o: preview only, no speech endpoint call")
	assert.Contains(t, buf.String(), "Today I did x.")
	_, statErr := os.Stat("standup.mp3")
	assert.ErrorIs(t, statErr, os.ErrNotExist, "preview writes no file")
}

func TestSpeakEmpty(t *testing.T) {
	assistant := &fakeAssistant{}
	st, root, buf := newHarness(t, assistant)
	st.Now = today(8, 0)
	root.SetArgs([]string{"speak", "-o", filepath.Join(t.TempDir(), "out.mp3")})
	require.NoError(t, root.Execute())
	assert.Equal(t, 0, assistant.scriptCalls, "no report, no speaker call")
	assert.Contains(t, buf.String(), "nothing to report")
}

func TestSpeakScriptErrorSkipsSynthesis(t *testing.T) {
	assistant := &fakeAssistant{genOut: "## Today\n- x", scriptErr: assert.AnError}
	st, root, _ := newHarness(t, assistant)
	st.Now = today(8, 0)
	_, err := st.Add("x")
	require.NoError(t, err)
	out := filepath.Join(t.TempDir(), "out.mp3")
	root.SetArgs([]string{"speak", "-o", out})
	err = root.Execute()
	assert.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, 0, assistant.synthCalls)
	_, statErr := os.Stat(out)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "a failed script leaves no partial file")
}

func TestSpeakSynthesizeErrorWritesNothing(t *testing.T) {
	assistant := &fakeAssistant{genOut: "## Today\n- x", scriptOut: "x", synthErr: assert.AnError}
	st, root, _ := newHarness(t, assistant)
	st.Now = today(8, 0)
	_, err := st.Add("x")
	require.NoError(t, err)
	out := filepath.Join(t.TempDir(), "out.mp3")
	root.SetArgs([]string{"speak", "-o", out})
	err = root.Execute()
	assert.ErrorIs(t, err, assert.AnError)
	assert.Contains(t, err.Error(), "script was printed above")
	_, statErr := os.Stat(out)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "a failed speech call leaves no partial file")
}

func TestSpeakSingleDateFlag(t *testing.T) {
	assistant := &fakeAssistant{genOut: "x", scriptOut: "x", synthAudio: []byte("A")}
	st, root, _ := newHarness(t, assistant)
	st.Now = func() time.Time { return time.Date(2026, 8, 14, 8, 0, 0, 0, time.Local) }
	_, err := st.Add("x")
	require.NoError(t, err)
	st.Now = today(8, 0)
	root.SetArgs([]string{"speak", "--from", "2026-08-14"})
	require.NoError(t, root.Execute())
	require.Len(t, assistant.genSec.Days, 1)
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
	root.SetArgs([]string{"rm", "--force", added.ID})
	require.NoError(t, root.Execute())
	tasks, err := st.List()
	require.NoError(t, err)
	assert.Empty(t, tasks)
	assert.Contains(t, buf.String(), "- removed: ship it")
}

func TestRmRequiresForceAndShowsTarget(t *testing.T) {
	st, root, _ := newHarness(t, &fakeAssistant{})
	added, err := st.Add("keep this task")
	require.NoError(t, err)
	root.SetArgs([]string{"rm", added.ID[:8]})
	err = root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keep this task")
	assert.Contains(t, err.Error(), "--force")
	tasks, listErr := st.List()
	require.NoError(t, listErr)
	assert.Len(t, tasks, 1)
}

func TestRmEchoFoldsMultilineTaskText(t *testing.T) {
	st, root, buf := newHarness(t, &fakeAssistant{})
	added, err := st.AddAt("feat: big thing\n\nbody line", "done", time.Date(2026, 8, 15, 12, 0, 0, 0, time.Local))
	require.NoError(t, err)
	root.SetArgs([]string{"rm", "--force", added.ID})
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

func TestTruecolorSupport(t *testing.T) {
	assert.False(t, truecolorSupported("windows", true, false), "promptui's Windows readline cannot parse truecolor escapes")
	assert.False(t, truecolorSupported("linux", false, false))
	assert.False(t, truecolorSupported("linux", true, true))
	assert.True(t, truecolorSupported("linux", true, false))
}

func TestGenerateDaysArg(t *testing.T) {
	assistant := &fakeAssistant{genOut: "## Today"}
	st, root, _ := newHarness(t, assistant)
	seedDays(t, st)
	st.Now = today(9, 0)
	root.SetArgs([]string{"generate", "3"})
	require.NoError(t, root.Execute())
	require.Len(t, assistant.genSec.Days, 3)
	assert.Equal(t, []string{"Thu 2026-08-13", "Fri 2026-08-14", "Sat 2026-08-15"},
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
	assert.Equal(t, []string{"Yesterday", "Today"},
		[]string{assistant.genSec.Days[0].Heading, assistant.genSec.Days[1].Heading})
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
	for _, tk := range assistant.genSec.Days[1].Tasks() {
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
	assert.Equal(t, "wrote "+path+"\n", buf.String(),
		"a minute of model calls must not end in silence: the path is echoed like every other mutation")
	assert.NotContains(t, buf.String(), "did stuff", "the report itself goes to the file, not stdout")
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

func TestGenerateObsidianPublishesConfiguredNote(t *testing.T) {
	assistant := &fakeAssistant{genOut: "## Today\n- did stuff"}
	st, _, buf := newHarness(t, assistant)
	st.Now = today(8, 0)
	_, err := st.Add("fix bug")
	require.NoError(t, err)
	vault := t.TempDir()
	root := New(func() (Deps, error) {
		return Deps{
			Assistant: func() (agent.Assistant, error) { return assistant, nil },
			Raw:       assistant,
			Store:     st,
			Config: config.Config{
				MeetingTime:   "09:30",
				ObsidianVault: vault,
				ObsidianNote:  "Standups/{date}.md",
			},
		}, nil
	})
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"generate", "--obsidian"})
	require.NoError(t, root.Execute())
	b, err := os.ReadFile(filepath.Join(vault, "Standups", "2026-08-15.md"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "<!-- standup:start -->\n## Today\n- did stuff\n<!-- standup:end -->")
	assert.Contains(t, buf.String(), "wrote ")
}

func TestGenerateObsidianRequiresVault(t *testing.T) {
	assistant := &fakeAssistant{genOut: "x"}
	st, root, _ := newHarness(t, assistant)
	st.Now = today(8, 0)
	_, err := st.Add("fix bug")
	require.NoError(t, err)
	root.SetArgs([]string{"generate", "--obsidian"})
	err = root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "obsidian.vault")
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
}

func TestGenerateSingleExplicitDate(t *testing.T) {
	for _, args := range [][]string{
		{"generate", "--from", "2026-08-13"},
		{"generate", "--to", "2026-08-13"},
	} {
		assistant := &fakeAssistant{genOut: "x"}
		st, root, _ := newHarness(t, assistant)
		seedDays(t, st)
		root.SetArgs(args)
		require.NoError(t, root.Execute())
		require.Len(t, assistant.genSec.Days, 1)
		assert.Equal(t, "Thu 2026-08-13", assistant.genSec.Days[0].Heading)
	}
}

func TestGenerateFromToUsage(t *testing.T) {
	for _, args := range [][]string{
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
	assert.Equal(t, []string{"friday task"}, taskTextsOf(assistant.genSec.Days[0].Tasks()))
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
	assert.NotContains(t, root.ErrOrStderr().(*bytes.Buffer).String(), "webhook: failed",
		"a delivery failure is reported once, as the command's error")
}

func TestDeliverReportAttemptsEverySinkAndReportsResults(t *testing.T) {
	oldPost, oldCopy := postWebhook, copyToClipboard
	postWebhook = func(string, string) error { return errors.New("boom") }
	copyToClipboard = func(string) error { return nil }
	t.Cleanup(func() { postWebhook, copyToClipboard = oldPost, oldCopy })

	root := New(func() (Deps, error) { return Deps{}, nil })
	buf := &bytes.Buffer{}
	gen, _, err := root.Find([]string{"generate"})
	require.NoError(t, err)
	gen.SetErr(buf)
	require.NoError(t, gen.Flags().Set("webhook", "https://example.invalid"))
	require.NoError(t, gen.Flags().Set("clip", "true"))
	err = deliverReport(gen, config.Config{}, "report")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom", "every sink is attempted and the failure is returned")
	assert.Contains(t, buf.String(), "clipboard: delivered")
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

func TestMailRejectsHeaderNewlines(t *testing.T) {
	cfg := config.Config{SMTPHost: "smtp.example.com", SMTPPort: 587, MailFrom: "standup@example.com"}
	err := mailReport(cfg, "team@example.com\r\nBcc: attacker@example.com", "report")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "newline")
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
	assert.NoError(t, reachable(live.URL, "TEST_BASE_URL"))
	assert.Error(t, reachable(srv.URL, "TEST_BASE_URL"), "closed endpoint is unreachable")
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
	t.Setenv("OPENAI_API_KEY", "")
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
	assert.Contains(t, buf.String(), "OPENAI_API_KEY not set (optional")
}

func TestDoctorChecksAnthropicEnv(t *testing.T) {
	pipeStdin(t, "")
	st, _, _ := newHarness(t, &fakeAssistant{})
	oldIdent := gitIdentity
	gitIdentity = func(string) (string, error) { return "me@example.com", nil }
	t.Cleanup(func() { gitIdentity = oldIdent })
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_MODEL", "")
	buf := &bytes.Buffer{}
	root := New(func() (Deps, error) {
		return Deps{
			Assistant: func() (agent.Assistant, error) { return &fakeAssistant{}, nil },
			Raw:       &fakeAssistant{},
			Store:     st,
			Config: config.Config{
				MeetingTime: "09:30", DataFile: filepath.Join(t.TempDir(), "tasks.jsonl"), Provider: "anthropic",
			},
		}, nil
	})
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"doctor"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, buf.String(), "fail env ANTHROPIC_BASE_URL")
	assert.Contains(t, buf.String(), "fail env ANTHROPIC_API_KEY")
	assert.Contains(t, buf.String(), "fail env ANTHROPIC_MODEL")
	assert.NotContains(t, buf.String(), "OPENAI_BASE_URL")
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
			{Hash: "h1", Body: "alice work", Author: "alice@example.com", Name: "Alice Dev", When: time.Date(2026, 8, 14, 10, 0, 0, 0, time.Local)},
			{Hash: "h2", Body: "bob work", Author: "bob@example.com", When: time.Date(2026, 8, 14, 11, 0, 0, 0, time.Local)},
		}, nil
	}
	t.Cleanup(func() { gitLogAll = old })
	root.SetArgs([]string{"commits", "--all-authors"})
	require.NoError(t, root.Execute())
	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, "Alice Dev", tasks[0].Author, "team headings read as names, not email addresses")
	assert.Equal(t, "bob@example.com", tasks[1].Author, "a commit with no author name falls back to the email")
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
	assistant := &fakeAssistant{genOut: "## Today\n- [done] work (10:00)"}
	st, root, buf := newHarness(t, assistant)
	st.Now = today(8, 0)
	oldName := gitName
	gitName = func(string) (string, error) { return "Juan", nil }
	t.Cleanup(func() { gitName = oldName })
	for _, seed := range []struct{ text, author string }{
		{"alice work", "Alice Dev"},
		{"bob work", "Bob Ops"},
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
	assert.Contains(t, buf.String(), "## Alice Dev")
	assert.Contains(t, buf.String(), "## Bob Ops")
	assert.Contains(t, buf.String(), "## Juan", "the reader must be able to tell whose the first block is")
	assert.Contains(t, buf.String(), "### Today", "day headings nest under their author")
	assert.NotContains(t, buf.String(), "\n## Today", "a day heading never sits beside an author heading")
	for _, sec := range assistant.genSecs {
		for _, day := range sec.Days {
			tasks := day.Tasks()
			for i := 1; i < len(tasks); i++ {
				assert.Equal(t, tasks[0].Author, tasks[i].Author, "every section is single-author")
			}
		}
	}
}

func TestGenerateTeamWithoutAuthorsNamesTheCurrentUser(t *testing.T) {
	assistant := &fakeAssistant{genOut: "## Today\n- [done] work (10:00)"}
	st, root, buf := newHarness(t, assistant)
	st.Now = today(8, 0)
	oldName, oldIdent := gitName, gitIdentity
	gitName = func(string) (string, error) { return "", errors.New("unset") }
	gitIdentity = func(string) (string, error) { return "me@example.com", nil }
	t.Cleanup(func() { gitName, gitIdentity = oldName, oldIdent })
	_, err := st.Add("plain task")
	require.NoError(t, err)
	root.SetArgs([]string{"generate", "--team"})
	require.NoError(t, root.Execute())
	require.Len(t, assistant.genSecs, 1, "no recorded authors: one section")
	assert.Contains(t, buf.String(), "## me@example.com", "the email is the fallback when user.name is unset")
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
	st, root, _ := newHarness(t, &fakeAssistant{})
	st.Now = today(8, 0)
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
	oldInteractive := editorInteractive
	editorInteractive = func() bool { return true }
	t.Cleanup(func() { editorInteractive = oldInteractive })
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
	oldInteractive := editorInteractive
	editorInteractive = func() bool { return true }
	t.Cleanup(func() { editorInteractive = oldInteractive })
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

func TestEditWithoutTextRefusesNonInteractiveEditor(t *testing.T) {
	oldInteractive := editorInteractive
	editorInteractive = func() bool { return false }
	t.Cleanup(func() { editorInteractive = oldInteractive })
	assistant := &fakeAssistant{}
	st, root, _ := newHarness(t, assistant)
	added, err := st.Add("keep me")
	require.NoError(t, err)
	root.SetArgs([]string{"edit", added.ID})
	err = root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-interactive")
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
	root.SetArgs([]string{"rm", "--force", added.ID})
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

func TestPromptFlagPlansAgainstStoreSnapshot(t *testing.T) {
	assistant := &fakeAssistant{}
	st, root, _ := newHarness(t, assistant)
	now := time.Date(2026, 8, 17, 10, 15, 0, 0, time.Local)
	st.Now = func() time.Time { return now }
	existing, err := st.Add("yesterday's task")
	require.NoError(t, err)

	root.SetArgs([]string{"-p", "mark yesterday's task done"})
	require.NoError(t, root.Execute())

	assert.Equal(t, 1, assistant.planCalls)
	assert.Equal(t, "mark yesterday's task done", assistant.planPrompt)
	assert.Equal(t, now, assistant.planNow)
	require.Len(t, assistant.planTasks, 1)
	assert.Equal(t, existing.ID, assistant.planTasks[0].ID)
}

func TestPromptLongFlagReadsStdin(t *testing.T) {
	assistant := &fakeAssistant{}
	_, root, _ := newHarness(t, assistant)
	root.SetIn(strings.NewReader("add first task\nthen add second\n"))
	root.SetArgs([]string{"--prompt", "-"})

	require.NoError(t, root.Execute())
	assert.Equal(t, 1, assistant.planCalls)
	assert.Equal(t, "add first task\nthen add second", assistant.planPrompt)
}

func TestPromptAppliesMixedOperationsAndPrintsChanges(t *testing.T) {
	assistant := &fakeAssistant{}
	st, root, buf := newHarness(t, assistant)
	editTask, err := st.Add("old text")
	require.NoError(t, err)
	deleteTask, err := st.Add("remove me")
	require.NoError(t, err)
	assistant.planResult = []store.BatchOperation{
		{Kind: store.OperationCreate, Text: "new task", Status: "todo"},
		{Kind: store.OperationEdit, ID: editTask.ID, Text: "new text"},
		{Kind: store.OperationStatus, ID: editTask.ID, Status: "done"},
		{Kind: store.OperationDelete, ID: deleteTask.ID},
	}

	root.SetArgs([]string{"--prompt", "apply several changes", "--yes"})
	require.NoError(t, root.Execute())

	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	byText := make(map[string]store.Task, len(tasks))
	for _, task := range tasks {
		byText[task.Text] = task
	}
	assert.Equal(t, "done", byText["new text"].Status)
	assert.Equal(t, "todo", byText["new task"].Status)
	for _, want := range []string{"new task", "new text", "done", "remove me"} {
		assert.Contains(t, buf.String(), want)
	}
}

func TestPromptDefaultHidesPlannerToolCalls(t *testing.T) {
	assistant := &fakeAssistant{
		planProgress: []string{
			"tool create: interpreting new task",
			"tool create: proposed 1 operation",
		},
		planResult: []store.BatchOperation{{Kind: store.OperationCreate, Text: "clean server disks", Status: "todo"}},
	}
	_, root, buf := newHarness(t, assistant)
	root.SetArgs([]string{"-p", "add cleanup server disks"})

	require.NoError(t, root.Execute())
	assert.Equal(t, 1, assistant.planCalls)
	assert.Zero(t, assistant.planVerboseCalls)
	assert.NotContains(t, buf.String(), "tool create")
	assert.Contains(t, buf.String(), "created")
}

func TestPromptVerboseShowsPlannerToolCalls(t *testing.T) {
	assistant := &fakeAssistant{
		planProgress: []string{
			"tool create: interpreting new task",
			"tool create: proposed 1 operation",
		},
		planResult: []store.BatchOperation{{Kind: store.OperationCreate, Text: "clean server disks", Status: "todo"}},
	}
	_, root, buf := newHarness(t, assistant)
	root.SetArgs([]string{"-p", "add cleanup server disks", "--verbose"})

	require.NoError(t, root.Execute())
	assert.Zero(t, assistant.planCalls)
	assert.Equal(t, 1, assistant.planVerboseCalls)
	assert.Contains(t, buf.String(), "tool create: interpreting new task")
	assert.Contains(t, buf.String(), "tool create: proposed 1 operation")
	assert.Contains(t, buf.String(), "created")
}

func TestPromptVerboseSeparatesProgressFromChanges(t *testing.T) {
	assistant := &fakeAssistant{
		planProgress: []string{"tool creator"},
		planResult:   []store.BatchOperation{{Kind: store.OperationCreate, Text: "clean server disks", Status: "todo"}},
	}
	_, root, _ := newHarness(t, assistant)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"-p", "add cleanup server disks", "--verbose"})

	require.NoError(t, root.Execute())
	assert.Equal(t, "tool creator\n", stderr.String())
	assert.NotContains(t, stdout.String(), "tool creator")
	assert.Contains(t, stdout.String(), "created")
}

func TestRootActionFlagsAreMutuallyExclusive(t *testing.T) {
	for _, args := range [][]string{
		{"-p", "add one", "-a", "add two"},
		{"-p", "add one", "-l"},
		{"-a", "add one", "-g"},
	} {
		assistant := &fakeAssistant{}
		_, root, _ := newHarness(t, assistant)
		root.SetArgs(args)

		err := root.Execute()
		require.ErrorContains(t, err, "only one of")
		assert.Zero(t, assistant.planCalls)
	}
}

func TestPromptRejectsPositionalArgs(t *testing.T) {
	assistant := &fakeAssistant{}
	_, root, _ := newHarness(t, assistant)
	root.SetArgs([]string{"-p", "add one", "ignored"})

	err := root.Execute()
	require.ErrorContains(t, err, "does not accept positional arguments")
	assert.Zero(t, assistant.planCalls)
}

func TestVerboseRequiresPrompt(t *testing.T) {
	assistant := &fakeAssistant{}
	_, root, _ := newHarness(t, assistant)
	root.SetArgs([]string{"--verbose"})

	err := root.Execute()
	require.ErrorContains(t, err, "--verbose requires --prompt")
}

func TestRootGenerateFlagStillAcceptsDays(t *testing.T) {
	assistant := &fakeAssistant{}
	st, root, _ := newHarness(t, assistant)
	_, err := st.Add("today")
	require.NoError(t, err)
	root.SetArgs([]string{"-g", "3"})

	require.NoError(t, root.Execute())
}

func TestPromptPlanErrorLeavesStoreUnchanged(t *testing.T) {
	assistant := &fakeAssistant{planErr: errors.New("invalid agent plan")}
	st, root, _ := newHarness(t, assistant)
	existing, err := st.Add("keep me")
	require.NoError(t, err)

	root.SetArgs([]string{"-p", "do everything"})
	err = root.Execute()
	require.ErrorContains(t, err, "invalid agent plan")
	tasks, listErr := st.List()
	require.NoError(t, listErr)
	require.Len(t, tasks, 1)
	assert.Equal(t, existing.ID, tasks[0].ID)
	assert.Equal(t, existing.Text, tasks[0].Text)
	assert.Equal(t, existing.Status, tasks[0].Status)
}

func TestPromptInvalidBatchLeavesStoreUnchanged(t *testing.T) {
	assistant := &fakeAssistant{}
	st, root, _ := newHarness(t, assistant)
	existing, err := st.Add("keep me")
	require.NoError(t, err)
	assistant.planResult = []store.BatchOperation{
		{Kind: store.OperationEdit, ID: existing.ID, Text: "must roll back"},
		{Kind: store.OperationDelete, ID: "invented-id"},
	}

	root.SetArgs([]string{"-p", "bad plan", "--yes"})
	require.Error(t, root.Execute())
	tasks, listErr := st.List()
	require.NoError(t, listErr)
	require.Len(t, tasks, 1)
	assert.Equal(t, existing.ID, tasks[0].ID)
	assert.Equal(t, existing.Text, tasks[0].Text)
	assert.Equal(t, existing.Status, tasks[0].Status)
}

func TestPromptEmptyStdinFailsBeforeAssistant(t *testing.T) {
	assistant := &fakeAssistant{}
	_, root, _ := newHarness(t, assistant)
	root.SetIn(strings.NewReader(" \n\t"))
	root.SetArgs([]string{"-p", "-"})

	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, 0, assistant.planCalls)
}

// rawAss fails if the model path is taken; the deterministic assistant must
// be used instead.
type rawAss struct{ called *bool }

func (r *rawAss) AddTasks(ctx context.Context, rawText string) ([]store.Task, error) {
	*r.called = true
	return nil, errors.New("model must not be called with --raw")
}

func (r *rawAss) Generate(ctx context.Context, sec report.Section) (agent.Generated, error) {
	return agent.Generated{}, nil
}

func (r *rawAss) Script(ctx context.Context, report string) (string, error) {
	return "", nil
}

func (r *rawAss) Synthesize(ctx context.Context, script string) ([]byte, error) {
	return nil, nil
}

func (r *rawAss) Plan(ctx context.Context, prompt string, tasks []store.Task, now time.Time) ([]store.BatchOperation, error) {
	*r.called = true
	return nil, errors.New("model must not be called through raw assistant")
}

var _ agent.Assistant = (*rawAss)(nil)

func TestColorReport(t *testing.T) {
	p := painter{on: true}
	in := "## Today\n### Done\n- a (09:00)\n### In progress\n- b (10:00)\n### Next\n- c (11:00)\n## Blockers\n- d (12:00)\n"
	on := colorReport(in, p)
	assert.Contains(t, on, "### "+p.wrap(statusColor("done"), "Done"))
	assert.Contains(t, on, "### "+p.wrap(statusColor("in-progress"), "In progress"))
	assert.Contains(t, on, "### "+p.wrap(statusColor("todo"), "Next"))
	assert.Contains(t, on, "## "+p.wrap(statusColor("blocked"), "Blockers"))
	assert.Contains(t, on, "## Today", "a day heading is not a status")
	assert.Contains(t, on, "- a (09:00)", "only the headings are painted")
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

func TestAddRawDashReadsCommandStdin(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	raw, err := agent.Local(config.Config{GenerateInputTemplate: "x"}, st)
	require.NoError(t, err)
	root := New(func() (Deps, error) { return Deps{Raw: raw, Store: st}, nil })
	root.SetIn(strings.NewReader("first\n\nsecond"))
	root.SetArgs([]string{"add", "--raw", "-"})
	require.NoError(t, root.Execute())
	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, "first", tasks[0].Text)
	assert.Equal(t, "second", tasks[1].Text)
}

func TestAddWarnsOnExactDuplicateWithoutBlocking(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	raw, err := agent.Local(config.Config{
		GenerateInputTemplate: "x",
	}, st)
	require.NoError(t, err)
	buf := &bytes.Buffer{}
	root := New(func() (Deps, error) {
		return Deps{Raw: raw, Store: st, Config: config.Config{MeetingTime: "09:30"}}, nil
	})
	root.SetOut(buf)
	root.SetErr(buf)

	root.SetArgs([]string{"add", "--raw", "same task"})
	require.NoError(t, root.Execute())
	root.SetArgs([]string{"add", "--raw", "same task"})
	require.NoError(t, root.Execute(), "duplicates warn but remain allowed")

	tasks, err := st.List()
	require.NoError(t, err)
	assert.Len(t, tasks, 2)
	assert.Contains(t, buf.String(), "warning: exact duplicate task")
}

func TestReadOnlyCommandsSkipAssistant(t *testing.T) {
	old := gitLog
	gitLog = func(dir string, since time.Time) ([]git.Commit, error) { return nil, nil }
	t.Cleanup(func() { gitLog = old })

	for name, args := range map[string][]string{
		"list":    {"list"},
		"done":    {"done", "%s"},
		"rm":      {"rm", "--force", "%s"},
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
	for _, args := range [][]string{{"--help"}, {"--version"}, {"version"}, {}, {"help"}} {
		root := New(func() (Deps, error) { return Deps{}, errors.New("config must not load") })
		root.Version = "test"
		buf := &bytes.Buffer{}
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs(args)
		require.NoError(t, root.Execute(), "args %v must not touch config", args)
	}
}

func TestVersionCommandAddsContext(t *testing.T) {
	root := New(func() (Deps, error) { return Deps{}, errors.New("must not load") })
	root.Version = "1.2.3"
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetArgs([]string{"version"})
	require.NoError(t, root.Execute())
	assert.Equal(t, "standup version 1.2.3\n", buf.String())
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

func TestConfigSetNeverLoadsDeps(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STANDUP_CONFIG_DIR", dir)
	root := New(func() (Deps, error) { return Deps{}, errors.New("deps must not load") })
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetArgs([]string{"config", "set", "offline", "true"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "offline=true")
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "offline: true")
}

func TestConfigSetProviderWritesDotEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STANDUP_CONFIG_DIR", dir)
	root := New(func() (Deps, error) { return Deps{}, errors.New("deps must not load") })
	root.SetArgs([]string{"config", "set", "OPENAI_BASE_URL", "http://localhost:8080/v1"})
	require.NoError(t, root.Execute())
	b, err := os.ReadFile(filepath.Join(dir, ".env"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "OPENAI_BASE_URL=http://localhost:8080/v1")
}

func TestConfigEditOpensConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STANDUP_CONFIG_DIR", dir)
	script := filepath.Join(t.TempDir(), "fake-editor.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf 'offline: true\n' > \"$1\"\n"), 0o755))
	t.Setenv("EDITOR", script)
	root := New(func() (Deps, error) { return Deps{}, errors.New("deps must not load") })
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"config", "edit"})
	require.NoError(t, root.Execute())
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "offline: true")
	assert.Contains(t, buf.String(), "opening "+filepath.Join(dir, "config.yaml"))
	assert.Contains(t, buf.String(), script)
}

func TestConfigEditRestoresInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STANDUP_CONFIG_DIR", dir)
	path, err := config.EnsureConfig()
	require.NoError(t, err)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	script := filepath.Join(t.TempDir(), "bad-editor.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf ': bad' > \"$1\"\n"), 0o755))
	t.Setenv("EDITOR", script)
	root := New(func() (Deps, error) { return Deps{}, errors.New("deps must not load") })
	root.SetArgs([]string{"config", "edit"})
	err = root.Execute()
	require.Error(t, err)
	after, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, before, after)
}

func TestUpdateAvailable(t *testing.T) {
	old := selfUpdate
	selfUpdate = func(context.Context, string, bool) (standupupdate.Result, error) {
		return standupupdate.Result{Current: "v0.5.0", Latest: "v0.6.0", State: standupupdate.UpgradeAvailable}, nil
	}
	t.Cleanup(func() { selfUpdate = old })
	_, root, buf := newHarness(t, &fakeAssistant{})
	root.Version = "0.5.0"
	root.SetArgs([]string{"update", "--check"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "update available: v0.6.0 (current: v0.5.0)")
}

func TestUpdateUpToDate(t *testing.T) {
	old := selfUpdate
	selfUpdate = func(context.Context, string, bool) (standupupdate.Result, error) {
		return standupupdate.Result{Current: "v0.5.0", Latest: "v0.5.0", State: standupupdate.UpToDate}, nil
	}
	t.Cleanup(func() { selfUpdate = old })
	_, root, buf := newHarness(t, &fakeAssistant{})
	root.Version = "0.5.0"
	root.SetArgs([]string{"update"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "up to date (v0.5.0)")
}

func TestUpdateNetworkErrorFails(t *testing.T) {
	old := selfUpdate
	selfUpdate = func(context.Context, string, bool) (standupupdate.Result, error) {
		return standupupdate.Result{}, errors.New("no network")
	}
	t.Cleanup(func() { selfUpdate = old })
	_, root, _ := newHarness(t, &fakeAssistant{})
	root.Version = "0.5.0"
	root.SetArgs([]string{"update"})
	assert.Error(t, root.Execute())
}

func TestUpdateInstallsByDefault(t *testing.T) {
	old := selfUpdate
	selfUpdate = func(_ context.Context, version string, check bool) (standupupdate.Result, error) {
		assert.Equal(t, "0.5.0", version)
		assert.False(t, check)
		return standupupdate.Result{Current: "v0.5.0", Latest: "v0.6.0", State: standupupdate.UpgradeAvailable, Updated: true}, nil
	}
	t.Cleanup(func() { selfUpdate = old })
	_, root, buf := newHarness(t, &fakeAssistant{})
	root.Version = "0.5.0"
	root.SetArgs([]string{"update"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "updated v0.5.0 -> v0.6.0")
}

func TestUpdateDevelopmentBuildFailsBeforeNetwork(t *testing.T) {
	old := selfUpdate
	selfUpdate = func(context.Context, string, bool) (standupupdate.Result, error) {
		t.Fatal("development build must not check the network")
		return standupupdate.Result{}, nil
	}
	t.Cleanup(func() { selfUpdate = old })
	_, root, _ := newHarness(t, &fakeAssistant{})
	root.Version = "dev"
	root.SetArgs([]string{"update", "--check"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "development build")
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

func TestSkillInstallReplacesWindowsSymlinkPlaceholders(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	for _, root := range []string{".agents", ".claude"} {
		dir := filepath.Join(root, "skills")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "standup"), []byte("../../config/skill"), 0o644))
	}
	cmd := New(func() (Deps, error) { return Deps{}, errors.New("deps must not load") })
	cmd.SetArgs([]string{"skill", "install"})
	require.NoError(t, cmd.Execute())
	for _, root := range []string{".agents", ".claude"} {
		b, err := os.ReadFile(filepath.Join(root, "skills", "standup", "SKILL.md"))
		require.NoError(t, err)
		assert.Equal(t, defaults.SkillMD, string(b))
	}
}

func TestSkillInstallPreflightsConflictsBeforeWriting(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	require.NoError(t, os.MkdirAll(filepath.Join(".claude", "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(".claude", "skills", "standup"), []byte("user data"), 0o644))
	cmd := New(func() (Deps, error) { return Deps{}, nil })
	cmd.SetArgs([]string{"skill", "install"})
	require.Error(t, cmd.Execute())
	_, err := os.Stat(filepath.Join(".agents", "skills", "standup", "SKILL.md"))
	assert.ErrorIs(t, err, os.ErrNotExist)
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

// --- sync ---------------------------------------------------------------

// syncHarness builds a root command with sync configured and syncRun faked,
// so CLI tests never need a PocketBase server.
func syncHarness(t *testing.T, cfg config.Config, res sync.Result, runErr error) (*cobra.Command, *bytes.Buffer, *[]string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	var calls []string
	old := syncRun
	syncRun = func(s *store.Store, srv sync.Server) (sync.Result, error) {
		calls = append(calls, strings.Join([]string{srv.URL, srv.Collection, srv.Email, srv.Password}, "|"))
		assert.Same(t, st, s, "sync gets the CLI's store")
		return res, runErr
	}
	t.Cleanup(func() { syncRun = old })
	buf := &bytes.Buffer{}
	root := New(func() (Deps, error) { return Deps{Store: st, Config: cfg}, nil })
	root.SetOut(buf)
	root.SetErr(buf)
	return root, buf, &calls
}

func TestSyncCommand(t *testing.T) {
	cfg := config.Config{SyncURL: "https://pb.example.com", SyncCollection: "my_tasks",
		SyncEmail: "admin@example.com", SyncPassword: "s3cret"}
	res := sync.Result{Push: []store.Task{{ID: "a"}, {ID: "b"}}, Pulled: 3}
	root, buf, calls := syncHarness(t, cfg, res, nil)
	root.SetArgs([]string{"sync"})
	require.NoError(t, root.Execute())

	assert.Equal(t, []string{"https://pb.example.com|my_tasks|admin@example.com|s3cret"}, *calls,
		"url, collection and credentials all come from the loaded config")
	assert.Contains(t, buf.String(), "2 pushed")
	assert.Contains(t, buf.String(), "3 pulled")
}

func TestSyncReportsResolvedDuplicates(t *testing.T) {
	cfg := config.Config{SyncURL: "https://pb.example.com", SyncCollection: "my_tasks"}
	root, buf, _ := syncHarness(t, cfg, sync.Result{Resolved: 2}, nil)
	root.SetArgs([]string{"sync"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "2 duplicates resolved")
}

func TestSyncQuietWhenNothingResolved(t *testing.T) {
	cfg := config.Config{SyncURL: "https://pb.example.com", SyncCollection: "my_tasks"}
	root, buf, _ := syncHarness(t, cfg, sync.Result{}, nil)
	root.SetArgs([]string{"sync"})
	require.NoError(t, root.Execute())
	assert.NotContains(t, buf.String(), "duplicate", "no duplicate noise on a clean sync")
}

func TestSyncNotConfigured(t *testing.T) {
	root, _, calls := syncHarness(t, config.Config{SyncCollection: "my_tasks"}, sync.Result{}, nil)
	root.SetArgs([]string{"sync"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sync.url", "the error names the setting to fill in")
	assert.Empty(t, *calls, "no server is contacted when sync is not configured")
}

func TestSyncPropagatesError(t *testing.T) {
	cfg := config.Config{SyncURL: "https://pb.example.com", SyncCollection: "my_tasks"}
	root, _, _ := syncHarness(t, cfg, sync.Result{}, errors.New("pocketbase is down"))
	root.SetArgs([]string{"sync"})
	assert.ErrorContains(t, root.Execute(), "pocketbase is down")
}

func TestSyncRegistered(t *testing.T) {
	root := New(func() (Deps, error) { return Deps{}, nil })
	var names []string
	for _, c := range root.Commands() {
		names = append(names, c.Name())
	}
	assert.Contains(t, names, "sync")
}

// A doctor that only checks presence and reachability reported all-green for
// a dead API key, and the very next command failed.
func TestDoctorFailsWhenTheModelCallFails(t *testing.T) {
	pipeStdin(t, "")
	st, _, _ := newHarness(t, &fakeAssistant{})
	oldIdent := gitIdentity
	gitIdentity = func(string) (string, error) { return "me@example.com", nil }
	oldReachable := reachable
	reachable = func(string, string) error { return nil }
	oldCheck := modelCheck
	modelCheck = func(context.Context, config.Config) error {
		return errors.New("endpoint rejected the credentials — check OPENAI_API_KEY: 401 Unauthorized")
	}
	t.Cleanup(func() { gitIdentity, reachable, modelCheck = oldIdent, oldReachable, oldCheck })
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("OPENAI_MODEL", "test-model")

	buf := &bytes.Buffer{}
	root := New(func() (Deps, error) {
		return Deps{
			Assistant: func() (agent.Assistant, error) { return &fakeAssistant{}, nil },
			Raw:       &fakeAssistant{},
			Store:     st,
			Config:    config.Config{MeetingTime: "09:30", DataFile: st.Path},
		}, nil
	})
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"doctor"})
	require.Error(t, root.Execute())
	assert.Contains(t, buf.String(), "ok   endpoint reachable")
	assert.Contains(t, buf.String(), "fail model answers")
	assert.Contains(t, buf.String(), "OPENAI_API_KEY")
}

func TestDoctorPassesWhenTheModelAnswers(t *testing.T) {
	pipeStdin(t, "")
	st, _, _ := newHarness(t, &fakeAssistant{})
	oldIdent := gitIdentity
	gitIdentity = func(string) (string, error) { return "me@example.com", nil }
	oldReachable := reachable
	reachable = func(string, string) error { return nil }
	oldCheck := modelCheck
	called := 0
	modelCheck = func(context.Context, config.Config) error { called++; return nil }
	t.Cleanup(func() { gitIdentity, reachable, modelCheck = oldIdent, oldReachable, oldCheck })
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("OPENAI_MODEL", "test-model")

	buf := &bytes.Buffer{}
	root := New(func() (Deps, error) {
		return Deps{
			Assistant: func() (agent.Assistant, error) { return &fakeAssistant{}, nil },
			Raw:       &fakeAssistant{},
			Store:     st,
			Config:    config.Config{MeetingTime: "09:30", DataFile: st.Path},
		}, nil
	})
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"doctor"})
	require.NoError(t, root.Execute())
	assert.Equal(t, 1, called, "doctor proves the setup with a real model call")
	assert.Contains(t, buf.String(), "ok   model answers")
}

// "delete all of my tasks" wiped the store with no confirmation and no undo,
// while `rm` refuses a single task without --force.
func TestPromptRefusesDeletesWithoutConfirmation(t *testing.T) {
	assistant := &fakeAssistant{}
	st, root, buf := newHarness(t, assistant)
	one, err := st.Add("keep me")
	require.NoError(t, err)
	two, err := st.Add("keep me too")
	require.NoError(t, err)
	assistant.planResult = []store.BatchOperation{
		{Kind: store.OperationDelete, ID: one.ID},
		{Kind: store.OperationDelete, ID: two.ID},
	}

	root.SetArgs([]string{"-p", "delete all of my tasks"})
	err = root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--yes")
	assert.Contains(t, buf.String(), "will delete")
	assert.Contains(t, buf.String(), "keep me too")
	tasks, listErr := st.List()
	require.NoError(t, listErr)
	assert.Len(t, tasks, 2, "a refused plan writes nothing")
}

func TestPromptAppliesDeletesWithYes(t *testing.T) {
	assistant := &fakeAssistant{}
	st, root, _ := newHarness(t, assistant)
	one, err := st.Add("remove me")
	require.NoError(t, err)
	assistant.planResult = []store.BatchOperation{{Kind: store.OperationDelete, ID: one.ID}}

	root.SetArgs([]string{"-p", "delete that task", "--yes"})
	require.NoError(t, root.Execute())
	tasks, err := st.List()
	require.NoError(t, err)
	assert.Empty(t, tasks)
}

func TestPromptWithoutDeletesNeedsNoConfirmation(t *testing.T) {
	assistant := &fakeAssistant{
		planResult: []store.BatchOperation{{Kind: store.OperationCreate, Text: "new task", Status: "todo"}},
	}
	st, root, _ := newHarness(t, assistant)

	root.SetArgs([]string{"-p", "add a task"})
	require.NoError(t, root.Execute())
	tasks, err := st.List()
	require.NoError(t, err)
	assert.Len(t, tasks, 1)
}

func TestClipboardCommandPerPlatform(t *testing.T) {
	found := func(want string) func(string) (string, error) {
		return func(name string) (string, error) {
			if name == want {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		}
	}
	name, args, err := clipboardCommand("darwin", found("nothing"))
	require.NoError(t, err)
	assert.Equal(t, "pbcopy", name)
	assert.Empty(t, args)

	name, args, err = clipboardCommand("linux", found("xclip"))
	require.NoError(t, err)
	assert.Equal(t, "xclip", name)
	assert.Equal(t, []string{"-selection", "clipboard"}, args)

	name, _, err = clipboardCommand("linux", found("wl-copy"))
	require.NoError(t, err)
	assert.Equal(t, "wl-copy", name)

	_, _, err = clipboardCommand("linux", found("nothing"))
	assert.ErrorContains(t, err, "no clipboard command found")
}

// wl-copy owns the clipboard from a forked child that inherits the parent's
// output pipes: collecting its output waited for that child, so
// `generate --clip` copied the report and then hung forever printing nothing.
func TestWriteClipboardDoesNotWaitForAForkedHelper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh is the stand-in for a forking clipboard helper")
	}
	done := make(chan error, 1)
	go func() { done <- writeClipboard("sh", []string{"-c", "cat >/dev/null; sleep 30 &"}, "report") }()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("writeClipboard waited for the helper's forked child")
	}
}

func TestWriteClipboardReportsAFailingCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh is the stand-in for a failing clipboard helper")
	}
	err := writeClipboard("sh", []string{"-c", "exit 3"}, "report")
	assert.ErrorContains(t, err, "clipboard sh")
}

// `commits` stores a commit's whole message: one 1700-character task rendered
// as a single row destroyed the column layout of the whole listing.
func TestListTruncatesLongTaskRows(t *testing.T) {
	pipeStdin(t, "")
	st, root, buf := newHarness(t, &fakeAssistant{})
	st.Now = today(8, 0)
	body := "add sync: merge tasks with a PocketBase server across machines " + strings.Repeat("body text ", 200)
	_, err := st.Add(body)
	require.NoError(t, err)

	root.SetArgs([]string{"list", "--days", "1"})
	require.NoError(t, root.Execute())
	row := strings.TrimRight(buf.String(), "\n")
	assert.Less(t, len([]rune(row)), 140, "the row fits a terminal")
	assert.Contains(t, row, "add sync: merge tasks with a PocketBase server")
	assert.True(t, strings.HasSuffix(row, "…"), "truncation is visible")

	tasks, err := st.List()
	require.NoError(t, err)
	assert.Equal(t, body, tasks[0].Text, "the store keeps the full text")
}

// The rephrase fallback used to be invisible: the user shipped raw task text
// to their team believing the model wrote it.
func TestGenerateNotesTheVerbatimFallback(t *testing.T) {
	assistant := &fakeAssistant{genOut: "## Today\n- [done] raw text", genFallback: "the model answered off-contract (12 entries for 39 tasks)"}
	st, root, buf := newHarness(t, assistant)
	st.Now = today(8, 0)
	_, err := st.Add("ship")
	require.NoError(t, err)

	root.SetArgs([]string{"generate"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "note: the model answered off-contract (12 entries for 39 tasks); using the task texts verbatim")
}

func TestGenerateStaysQuietWhenTheModelAnswers(t *testing.T) {
	assistant := &fakeAssistant{genOut: "## Today\n- [done] shipped it"}
	st, root, buf := newHarness(t, assistant)
	st.Now = today(8, 0)
	_, err := st.Add("ship")
	require.NoError(t, err)

	root.SetArgs([]string{"generate"})
	require.NoError(t, root.Execute())
	assert.NotContains(t, buf.String(), "note:")
}

// A read-only diagnostic left an empty tasks.jsonl behind on machines that
// had never run standup.
func TestDoctorDoesNotCreateTheDataFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh", "tasks.jsonl")
	require.NoError(t, checkWritable(path))
	assert.NoFileExists(t, path, "probing writability must not create the store")
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Empty(t, entries, "and must leave no probe behind either")

	require.NoError(t, os.WriteFile(path, []byte(""), 0o644))
	assert.NoError(t, checkWritable(path), "an existing store is still checked")
	require.NoError(t, os.Chmod(path, 0o444))
	assert.Error(t, checkWritable(path), "an unwritable store is reported")
}

// model_call_timeout bounds one call and the coordinator makes several, so a
// -p run had no overall bound at all and could sit silent indefinitely.
func TestPromptGivesUpOnItsWallClockBudget(t *testing.T) {
	assistant := &fakeAssistant{planHold: 2 * time.Second}
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	buf := &bytes.Buffer{}
	root := New(func() (Deps, error) {
		return Deps{
			Assistant: func() (agent.Assistant, error) { return assistant, nil },
			Raw:       assistant,
			Store:     st,
			Config:    config.Config{MeetingTime: "09:30", ModelCallTimeout: 20 * time.Millisecond},
		}, nil
	})
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"-p", "do something slow"})

	start := time.Now()
	err = root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model_call_timeout")
	assert.Less(t, time.Since(start), time.Second, "the budget bounds the whole command")
	tasks, listErr := st.List()
	require.NoError(t, listErr)
	assert.Empty(t, tasks)
}

func TestPromptBudgetIsAMultipleOfTheCallTimeout(t *testing.T) {
	assert.Equal(t, 6*time.Minute, promptBudget(time.Minute))
	assert.Equal(t, 6*time.Minute, promptBudget(0), "an unset timeout falls back to the default call bound")
}

// The verbose plan reported `status <id> blocked -> blocked` as a change, and
// applying it would restamp the record for no reason.
func TestPromptDropsNoOpOperations(t *testing.T) {
	assistant := &fakeAssistant{}
	st, root, buf := newHarness(t, assistant)
	blocked, err := st.Add("triaged the flaky CI job")
	require.NoError(t, err)
	_, err = st.SetStatus(blocked.ID, "blocked")
	require.NoError(t, err)
	assistant.planResult = []store.BatchOperation{
		{Kind: store.OperationStatus, ID: blocked.ID, Status: "blocked"},
		{Kind: store.OperationEdit, ID: blocked.ID, Text: "triaged the flaky CI job"},
	}

	root.SetArgs([]string{"-p", "block the flaky CI work"})
	require.NoError(t, root.Execute())
	assert.Equal(t, "no changes\n", buf.String())
}

// 0 violated the stated rule and was silently reinterpreted as "today",
// while -5 was rejected.
func TestListRejectsNonPositiveDays(t *testing.T) {
	pipeStdin(t, "")
	st, root, _ := newHarness(t, &fakeAssistant{})
	st.Now = today(8, 0)
	_, err := st.Add("ship")
	require.NoError(t, err)
	for _, days := range []string{"0", "-5"} {
		root.SetArgs([]string{"list", "--days", days})
		err := root.Execute()
		require.Error(t, err, "--days %s", days)
		assert.Contains(t, err.Error(), "N >= 1")
	}
}

// The first positional is documented as [days]: a typo there sent the user
// looking for a directory.
func TestCommitsRejectsATypedDayCount(t *testing.T) {
	st, root, _ := newHarness(t, &fakeAssistant{})
	st.Now = today(8, 0)
	root.SetArgs([]string{"commits", "1o"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither a day count nor a directory")
}

// `store:` and `agent:` are package names the user has no concept of; the
// messages behind them are good and must survive intact.
func TestErrorsDoNotLeakPackageNames(t *testing.T) {
	pipeStdin(t, "")
	st, root, _ := newHarness(t, &fakeAssistant{})
	st.Now = today(8, 0)
	_, err := st.Add("ship")
	require.NoError(t, err)

	root.SetArgs([]string{"status", "zzzzzz", "nonsense"})
	err = root.Execute()
	require.Error(t, err)
	assert.Equal(t, `no task with id "zzzzzz"`, err.Error())

	tasks, err := st.List()
	require.NoError(t, err)
	root.SetArgs([]string{"status", tasks[0].ID, "nonsense"})
	err = root.Execute()
	require.Error(t, err)
	assert.Equal(t, `invalid status "nonsense" (valid: todo, in-progress, blocked, done)`, err.Error())
}

// Installing twice printed the same success both times; an edited SKILL.md
// was replaced with no diff, no prompt and no warning.
func TestSkillInstallWarnsWhenReplacingAnEditedSkill(t *testing.T) {
	unsetCliEnv(t, "STANDUP_CONFIG_DIR")
	repo := t.TempDir()
	t.Chdir(repo)
	root := New(func() (Deps, error) { return Deps{}, errors.New("skill install never loads deps") })
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"skill", "install"})
	require.NoError(t, root.Execute())
	assert.NotContains(t, buf.String(), "warning", "a fresh install has nothing to warn about")

	edited := filepath.Join(repo, ".claude", "skills", "standup", "SKILL.md")
	require.NoError(t, os.WriteFile(edited, []byte("my own instructions\n"), 0o644))
	buf.Reset()
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "warning: replaced an edited "+filepath.Join(".claude", "skills", "standup", "SKILL.md"))
	assert.NotContains(t, strings.SplitN(buf.String(), "warning:", 2)[0], ".claude",
		"the untouched root installs without a warning")

	buf.Reset()
	require.NoError(t, root.Execute())
	assert.NotContains(t, buf.String(), "warning", "reinstalling the embedded copy is not an edit")
}

// TestListAlignsColumns: the status column was unpadded, so every piped or
// pasted listing came out ragged. The TUI path already aligned.
func TestListAlignsColumns(t *testing.T) {
	st, root, buf := newHarness(t, &fakeAssistant{})
	st.Now = today(9, 0)
	for _, text := range []string{"fixed the login redirect thing", "stuck waiting on devops", "write API docs"} {
		_, err := st.Add(text)
		require.NoError(t, err)
	}
	root.SetArgs([]string{"list", "--days", "1"})
	require.NoError(t, root.Execute())

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, 3)
	want := strings.Index(lines[0], "09:00")
	for _, line := range lines {
		assert.Equal(t, want, strings.Index(line, "09:00"), "every row starts its time column at the same offset: %q", line)
	}
}

// TestListTruncatesAtAWordBoundary: commit-imported rows were cut mid-token.
func TestListTruncatesAtAWordBoundary(t *testing.T) {
	st, root, buf := newHarness(t, &fakeAssistant{})
	st.Now = today(9, 0)
	_, err := st.Add(strings.Repeat("refactoring ", 20) + "finished")
	require.NoError(t, err)
	root.SetArgs([]string{"list", "--days", "1"})
	require.NoError(t, root.Execute())

	out := strings.TrimRight(buf.String(), "\n")
	require.Contains(t, out, "…")
	shown := strings.TrimSuffix(out[strings.Index(out, "refactoring"):], "…")
	assert.True(t, strings.HasSuffix(shown, "refactoring"), "the row ends on a whole word, got %q", shown)
}

// TestAddRawEchoesTheWholeTask: the echo exists to confirm what was stored,
// and it was the one place truncation defeated the purpose.
func TestAddRawEchoesTheWholeTask(t *testing.T) {
	long := "ugh spent like 4 hrs today just banging my head against that stupid caching thing in the checkout flow, turns out redis was evicting keys cuz maxmemory was set way too low, anyway fixed it i think #checkout"
	_, root, buf := newHarness(t, &fakeAssistant{addResult: []store.Task{{ID: "x", Text: long, Status: "done"}}})
	root.SetArgs([]string{"add", "--raw", long})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), long)
	assert.NotContains(t, buf.String(), "…")
}

// TestAmbiguousIDListsTheCandidates: printing the rows turns a retry into a
// copy-paste.
func TestAmbiguousIDListsTheCandidates(t *testing.T) {
	st, root, _ := newHarness(t, &fakeAssistant{})
	st.Now = today(9, 0)
	var ids []string
	for _, text := range []string{"first task", "second task"} {
		task, err := st.Add(text)
		require.NoError(t, err)
		ids = append(ids, task.ID)
	}
	// Force a shared prefix by rewriting the store's ids.
	require.NoError(t, retagIDs(st, ids, []string{"aaaa1111-0000-0000-0000-000000000001", "aaaa2222-0000-0000-0000-000000000002"}))

	root.SetArgs([]string{"done", "aaaa"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `ambiguous id "aaaa"`)
	assert.Contains(t, err.Error(), "first task", "the candidate rows are the answer to the question")
	assert.Contains(t, err.Error(), "second task")
	assert.Contains(t, err.Error(), "aaaa1111")
}

// retagIDs rewrites the store's ids in place so a test can force a shared
// prefix; uuids never collide on their own.
func retagIDs(st *store.Store, from, to []string) error {
	b, err := os.ReadFile(st.Path)
	if err != nil {
		return err
	}
	out := string(b)
	for i := range from {
		out = strings.ReplaceAll(out, from[i], to[i])
	}
	return os.WriteFile(st.Path, []byte(out), 0o644)
}

// TestAddWithoutAProviderStillCaptures: on a clean install the first command
// anyone types was `add`, and it exited 1 with nothing stored.
func TestAddWithoutAProviderStillCaptures(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	raw, err := agent.Local(config.Config{GenerateInputTemplate: "x"}, st)
	require.NoError(t, err)
	buf := &bytes.Buffer{}
	root := New(func() (Deps, error) {
		return Deps{
			Assistant: func() (agent.Assistant, error) {
				return nil, &agent.ProviderUnconfiguredError{Missing: []string{"OPENAI_BASE_URL", "OPENAI_MODEL"}}
			},
			Raw: raw, Store: st, Config: config.Config{MeetingTime: "09:30"},
		}, nil
	})
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"add", "fixed a bug"})
	require.NoError(t, root.Execute(), "the note is captured, not dropped")

	stored, err := st.List()
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, "fixed a bug", stored[0].Text)
	assert.Contains(t, buf.String(), "OPENAI_BASE_URL", "the note names what is missing")
	assert.Contains(t, buf.String(), "doctor", "and where to fix it")
}

// Windows keeps a running executable locked, so an update cannot delete its
// own backup. That used to fail the command with "Access is denied" after the
// new binary was already in place, telling users to distrust an update that
// had worked.
func TestUpdateReportsALeftoverBackupWithoutFailing(t *testing.T) {
	old := selfUpdate
	selfUpdate = func(context.Context, string, bool) (standupupdate.Result, error) {
		return standupupdate.Result{
			Current: "v0.17.0", Latest: "v0.18.0", Updated: true,
			LeftoverBackup: `C:\Users\j64\AppData\Local\standup\standup.exe.old-4340`,
		}, nil
	}
	t.Cleanup(func() { selfUpdate = old })

	_, root, buf := newHarness(t, &fakeAssistant{})
	root.Version = "0.17.0"
	root.SetArgs([]string{"update"})
	require.NoError(t, root.Execute(), "a backup that cannot be deleted is not a failed update")
	assert.Contains(t, buf.String(), "updated v0.17.0 -> v0.18.0")
	assert.Contains(t, buf.String(), "note the previous version is still open")
	assert.Contains(t, buf.String(), `standup.exe.old-4340`)
}
