package agent

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"

	frameworkagent "github.com/microsoft/agent-framework-go/agent"
	"github.com/microsoft/agent-framework-go/message"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"standup/internal/config"
	"standup/internal/report"
	"standup/internal/store"
)

const genTplText = `## Yesterday
{{range .Yesterday}}- [{{.Status}}] {{.Text}} ({{.Timestamp.Format "15:04"}})
{{end}}
## Today
{{range .Today}}- [{{.Status}}] {{.Text}} ({{.Timestamp.Format "15:04"}})
{{end}}`

const daysTplText = `{{range .Days}}## {{.Heading}}
{{range .Tasks}}- [{{.Status}}] {{.Text}} ({{.Timestamp.Format "15:04"}})
{{end}}{{end}}`

func mustTpl(s string) *template.Template {
	return template.Must(template.New("t").Parse(s))
}

func TestExtractTasks(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{"plain json", `{"tasks":["fix login","write tests"]}`, []string{"fix login", "write tests"}, false},
		{"objects drop the status", `{"tasks":[{"task":"waiting on infra","status":"blocked"},{"task":"ship it","status":"done"}]}`, []string{"waiting on infra", "ship it"}, false},
		{"mixed forms", `{"tasks":["plain",{"task":"obj","status":"blocked"}]}`, []string{"plain", "obj"}, false},
		{"fenced", "```json\n{\"tasks\":[\"a\"]}\n```", []string{"a"}, false},
		{"text around", "Here you go:\n{\"tasks\":[\"a\",\"b\"]}\nhope that helps", []string{"a", "b"}, false},
		{"missing", "no json here at all", nil, true},
		{"empty array", `{"tasks":[]}`, nil, true},
		{"object without task field", `{"tasks":[{"status":"blocked"}]}`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractTasks(tt.in)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractOperations(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.FixedZone("PY", -4*60*60))
	out, err := extractOperations(`{"operations":[{"kind":"create","text":"did work","status":"done","when":"yesterday"},{"kind":"status","id":"full-id","status":"done"},{"kind":"delete","id":"old-id"}]}`, now)
	require.NoError(t, err)
	require.Len(t, out, 3)
	assert.Equal(t, store.OperationCreate, out[0].Kind)
	assert.Equal(t, "did work", out[0].Text)
	assert.Equal(t, "2026-08-16T10:00:00-04:00", out[0].Timestamp.Format(time.RFC3339))
	assert.Equal(t, "full-id", out[1].ID)
	assert.Equal(t, store.OperationDelete, out[2].Kind)

	for _, invalid := range []string{
		`{"operations":[]}`,
		`{"operations":[{"kind":"create","text":"x","when":"some day"}]}`,
		`{"operations":[{"kind":"create","text":"x","extra":true}]}`,
		"not json",
	} {
		_, err := extractOperations(invalid, now)
		assert.Error(t, err)
	}
}

func TestExtractOperationsExplainsEmptyPlan(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)

	t.Run("planner reason", func(t *testing.T) {
		reason := "target missing\n" + strings.Repeat("x", 500)
		_, err := extractOperations(`{"operations":[],"message":"`+strings.ReplaceAll(reason, "\n", `\n`)+`"}`, now)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no applicable changes")
		assert.Contains(t, err.Error(), "target missing")
		assert.NotContains(t, err.Error(), "\n")
		assert.LessOrEqual(t, len(err.Error()), 256, "planner-provided reasons must be bounded")
	})

	t.Run("no planner reason", func(t *testing.T) {
		_, err := extractOperations(`{"operations":[]}`, now)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no applicable changes")
		assert.Contains(t, err.Error(), "missing")
		assert.Contains(t, err.Error(), "ambiguous")
	})
}

func TestExtractOperationsDistinguishesInvalidPlan(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)

	for _, output := range []string{
		"not json",
		`{"operations":[{"kind":"create","text":"x","extra":true}]}`,
	} {
		t.Run(output, func(t *testing.T) {
			_, err := extractOperations(output, now)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid operation plan")
		})
	}
}

func TestSafeDiagnosticIsBoundedAndRemovesControls(t *testing.T) {
	got := safeDiagnostic("bad\x1b[31m\n" + strings.Repeat("x", 300))
	assert.NotContains(t, got, "\x1b")
	assert.LessOrEqual(t, len([]rune(got)), 161)
}

func TestImplPlanIncludesBoundedSnapshot(t *testing.T) {
	var got string
	a := &impl{
		planner: func(ctx context.Context, prompt string, _ func(string)) (string, error) {
			got = prompt
			return `{"operations":[{"kind":"status","id":"full-id","status":"done"}]}`, nil
		},
	}
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.FixedZone("PY", -4*60*60))
	ops, err := a.Plan(context.Background(), "mark yesterday done", []store.Task{{ID: "full-id", Text: "work", Status: "todo", Timestamp: now.AddDate(0, 0, -1)}}, now)
	require.NoError(t, err)
	require.Len(t, ops, 1)
	assert.Equal(t, "full-id", ops[0].ID)
	assert.NotContains(t, got, "coordinate", "agent instructions must not be duplicated in user input")
	assert.Contains(t, got, `"prompt":"mark yesterday done"`)
	assert.Contains(t, got, `"id":"full-id"`)
	assert.Contains(t, got, `"relative_date":"yesterday"`)
	assert.Contains(t, got, `"now":"2026-08-17T10:00:00-04:00"`)
}

func TestImplAddTasksWritesStore(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	st.Now = func() time.Time { return time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC) }
	a := &impl{
		editor: func(ctx context.Context, prompt string) (string, error) {
			return `{"tasks":[{"task":"Fixed login bug"},{"task":"Waiting on infra","status":"blocked"}]}`, nil
		},
		st: st,
	}
	got, err := a.AddTasks(context.Background(), "raw")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "Fixed login bug", got[0].Text)
	assert.Equal(t, "done", got[0].Status, "past-tense work is done, derived in Go")
	assert.Equal(t, "blocked", got[1].Status, "the text says it is blocked")

	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, "blocked", tasks[1].Status)
}

// The model is not allowed to judge progress: it invented `blocked` for
// routine work, and an invented blocker reaches the team's Blockers section.
func TestImplAddTasksIgnoresModelSuppliedStatus(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	a := &impl{
		editor: func(context.Context, string) (string, error) {
			return `{"tasks":[{"task":"triaged the flaky CI job","status":"blocked"},{"task":"update the readme","status":"in-progress"}]}`, nil
		},
		st: st,
	}
	got, err := a.AddTasks(context.Background(), "raw")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "done", got[0].Status)
	assert.Equal(t, "todo", got[1].Status)
}

func TestImplAddTasksRestoresSingleTaskTags(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	a := &impl{
		st: st,
		editor: func(context.Context, string) (string, error) {
			return `{"tasks":[{"task":"Fix login","status":"todo"}]}`, nil
		},
	}
	got, err := a.AddTasks(context.Background(), "fix login #backend")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "Fix login #backend", got[0].Text)
}

func TestImplAddTasksEditorError(t *testing.T) {
	a := &impl{
		editor: func(ctx context.Context, prompt string) (string, error) { return "", assert.AnError },
	}
	_, err := a.AddTasks(context.Background(), "raw")
	assert.ErrorIs(t, err, assert.AnError)
}

func TestImplAddTasksIsAtomicOnInvalidLaterTask(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	a := &impl{editor: func(context.Context, string) (string, error) {
		return `{"tasks":[{"task":"valid"},"   "]}`, nil
	}, st: st}
	_, err = a.AddTasks(context.Background(), "raw")
	require.Error(t, err)
	tasks, listErr := st.List()
	require.NoError(t, listErr)
	assert.Empty(t, tasks)
}

func TestImplPlanFallsBackWithoutTools(t *testing.T) {
	var fallbackPrompt string
	a := &impl{
		planner: func(context.Context, string, func(string)) (string, error) { return "not json", nil },
		plannerFallback: func(_ context.Context, prompt string) (string, error) {
			fallbackPrompt = prompt
			return `{"operations":[{"kind":"create","text":"ship"}]}`, nil
		},
	}
	ops, err := a.Plan(context.Background(), "add ship", nil, time.Now())
	require.NoError(t, err)
	require.Len(t, ops, 1)
	assert.Equal(t, store.OperationCreate, ops[0].Kind)
	assert.Contains(t, fallbackPrompt, `"prompt":"add ship"`)
}

func TestImplGenerateRephrasesViaReporter(t *testing.T) {
	var gotPrompt string
	a := &impl{
		reporter: func(ctx context.Context, prompt string) (string, error) {
			gotPrompt = prompt
			return `{"tasks": ["Fixed login bug", "Ship the release"]}`, nil
		},
		genTpl:  mustTpl(genTplText),
		daysTpl: mustTpl(daysTplText),
	}
	sec := report.Section{
		Days: []report.Day{
			{Heading: "Yesterday", Tasks: []store.Task{{Text: "fix login bug", Status: "done", Timestamp: time.Date(2026, 8, 14, 9, 15, 0, 0, time.UTC)}}},
			{Heading: "Today", Tasks: []store.Task{{Text: "ship release", Status: "done", Timestamp: time.Date(2026, 8, 15, 8, 5, 0, 0, time.UTC)}}},
		},
		Yesterday: []store.Task{{Text: "fix login bug", Status: "done", Timestamp: time.Date(2026, 8, 14, 9, 15, 0, 0, time.UTC)}},
		Today:     []store.Task{{Text: "ship release", Status: "done", Timestamp: time.Date(2026, 8, 15, 8, 5, 0, 0, time.UTC)}},
	}
	gen, err := a.Generate(context.Background(), sec)
	out := gen.Text
	require.NoError(t, err)
	assert.NotContains(t, gotPrompt, "rephrase", "agent instructions must not be duplicated in user input")
	assert.Contains(t, gotPrompt, "- fix login bug")
	assert.Contains(t, gotPrompt, "- ship release")
	assert.Contains(t, out, "## Yesterday")
	assert.Contains(t, out, "- [done] Fixed login bug (09:15)", "formatting is deterministic, only phrasing comes from the model")
	assert.Contains(t, out, "- [done] Ship the release (08:05)")
	assert.NotContains(t, out, "fix login bug", "original wording replaced")
}

func TestImplGenerateLanguageInPrompt(t *testing.T) {
	var gotPrompt string
	a := &impl{
		reporter: func(ctx context.Context, prompt string) (string, error) { gotPrompt = prompt; return "no json", nil },
		lang:     "German",
		genTpl:   mustTpl(genTplText),
		daysTpl:  mustTpl(daysTplText),
	}
	sec := report.Section{
		Days:  []report.Day{{Heading: "Yesterday"}, {Heading: "Today", Tasks: []store.Task{{Text: "x", Status: "todo", Timestamp: time.Now()}}}},
		Today: []store.Task{{Text: "x", Status: "todo", Timestamp: time.Now()}},
	}
	_, err := a.Generate(context.Background(), sec)
	require.NoError(t, err)
	assert.Contains(t, gotPrompt, "German")
}

func TestImplGenerateFallsBackDeterministically(t *testing.T) {
	tests := []struct {
		name     string
		reporter func(ctx context.Context, prompt string) (string, error)
	}{
		{"reporter error", func(ctx context.Context, prompt string) (string, error) { return "", assert.AnError }},
		{"not json", func(ctx context.Context, prompt string) (string, error) { return "sure thing!", nil }},
		{"count mismatch", func(ctx context.Context, prompt string) (string, error) { return `{"tasks":["only one"]}`, nil }},
		{"empty entry", func(ctx context.Context, prompt string) (string, error) { return `{"tasks":["fixed","  "]}`, nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &impl{reporter: tt.reporter, genTpl: mustTpl(genTplText), daysTpl: mustTpl(daysTplText)}
			sec := report.Section{
				Days: []report.Day{
					{Heading: "Yesterday", Tasks: []store.Task{{Text: "fix login bug", Status: "done", Timestamp: time.Date(2026, 8, 14, 9, 15, 0, 0, time.UTC)}}},
					{Heading: "Today", Tasks: []store.Task{{Text: "ship release", Status: "done", Timestamp: time.Date(2026, 8, 15, 8, 5, 0, 0, time.UTC)}}},
				},
				Yesterday: []store.Task{{Text: "fix login bug", Status: "done", Timestamp: time.Date(2026, 8, 14, 9, 15, 0, 0, time.UTC)}},
				Today:     []store.Task{{Text: "ship release", Status: "done", Timestamp: time.Date(2026, 8, 15, 8, 5, 0, 0, time.UTC)}},
			}
			gen, err := a.Generate(context.Background(), sec)
			out := gen.Text
			require.NoError(t, err, "fallback keeps generate working")
			assert.Contains(t, out, "- [done] fix login bug (09:15)")
			assert.Contains(t, out, "- [done] ship release (08:05)")
		})
	}
}

func TestImplGenerateRangeUsesDaysTemplate(t *testing.T) {
	a := &impl{
		reporter: func(ctx context.Context, prompt string) (string, error) { return "", assert.AnError },
		genTpl:   mustTpl(genTplText),
		daysTpl:  mustTpl(daysTplText),
	}
	sec := report.Section{Days: []report.Day{
		{Heading: "Thu 2026-08-13", Tasks: []store.Task{{Text: "old", Status: "done", Timestamp: time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)}}},
		{Heading: "Yesterday"},
		{Heading: "Today"},
	}}
	gen, err := a.Generate(context.Background(), sec)
	out := gen.Text
	require.NoError(t, err)
	assert.Contains(t, out, "## Thu 2026-08-13")
	assert.Contains(t, out, "- [done] old (09:00)")
	assert.NotContains(t, out, "## Yesterday\n\n## Today\n", "two-section template not used for ranges")
}

func TestImplGenerateExplicitTwoDayWindowUsesDaysTemplate(t *testing.T) {
	a := &impl{
		reporter: func(ctx context.Context, prompt string) (string, error) { return "", assert.AnError },
		genTpl:   mustTpl(genTplText),
		daysTpl:  mustTpl(daysTplText),
	}
	sec := report.Section{Days: []report.Day{
		{Heading: "Mon 2026-08-10", Tasks: []store.Task{{Text: "old", Status: "done", Timestamp: time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)}}},
		{Heading: "Tue 2026-08-11", Tasks: []store.Task{{Text: "new", Status: "done", Timestamp: time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)}}},
	}}
	gen, err := a.Generate(context.Background(), sec)
	out := gen.Text
	require.NoError(t, err)
	assert.Contains(t, out, "## Mon 2026-08-10", "no Yesterday/Today aliases: dated headings")
	assert.Contains(t, out, "## Tue 2026-08-11")
}

func TestExtractStrings(t *testing.T) {
	got, err := extractStrings(`{"tasks":["a","b"]}`)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, got)

	got, err = extractStrings("sure:\n```json\n{\"tasks\":[\"x\"]}\n```")
	require.NoError(t, err)
	assert.Equal(t, []string{"x"}, got)

	_, err = extractStrings(`{"tasks":[]}`)
	assert.Error(t, err)
	_, err = extractStrings("no json")
	assert.Error(t, err)
}

func TestImplScriptReturnsTrimmedScript(t *testing.T) {
	var gotPrompt string
	a := &impl{
		speaker: func(ctx context.Context, prompt string) (string, error) {
			gotPrompt = prompt
			return "  Yesterday I fixed the login bug. Today I ship the release. \n", nil
		},
	}
	script, err := a.Script(context.Background(), "## Yesterday\n- [done] fix login bug")
	require.NoError(t, err)
	assert.NotContains(t, gotPrompt, "speak plainly", "agent instructions must not be duplicated in user input")
	assert.Contains(t, gotPrompt, "## Yesterday\n- [done] fix login bug", "the rendered report is the speaker input")
	assert.Equal(t, "Yesterday: fix login bug.", script, "invented release work falls back to the report")
}

func TestImplScriptFallsBackWhenSpeakerIsUngrounded(t *testing.T) {
	a := &impl{speaker: func(context.Context, string) (string, error) {
		return "User Safety: safe", nil
	}}
	reportText := "## Today\n- [todo] Fix login redirect #backend (09:00)\n- [done] Review deployment #ops (10:00)"
	script, err := a.Script(context.Background(), reportText)
	require.NoError(t, err)
	assert.Equal(t, "Today: Fix login redirect #backend. Also, Review deployment #ops.", script,
		"one anchor per section: repeating it before every item reads like a template")
}

func TestImplScriptFallsBackOnInventedAdvice(t *testing.T) {
	a := &impl{speaker: func(context.Context, string) (string, error) {
		return "I fixed login and should make sure deployment works.", nil
	}}
	reportText := "## Today\n- [done] Fixed login (09:00)"
	script, err := a.Script(context.Background(), reportText)
	require.NoError(t, err)
	assert.Equal(t, "Today: Fixed login.", script)
}

func TestImplScriptLanguageInPrompt(t *testing.T) {
	var gotPrompt string
	a := &impl{
		speaker: func(ctx context.Context, prompt string) (string, error) { gotPrompt = prompt; return "x", nil },
		lang:    "German",
	}
	_, err := a.Script(context.Background(), "report")
	require.NoError(t, err)
	assert.Contains(t, gotPrompt, "German")
}

func TestImplScriptEmptyIsAnError(t *testing.T) {
	a := &impl{
		speaker: func(ctx context.Context, prompt string) (string, error) { return "  \n ", nil },
	}
	_, err := a.Script(context.Background(), "report")
	assert.Error(t, err, "an empty script must not be narrated")
}

func TestImplScriptErrorPropagates(t *testing.T) {
	a := &impl{
		speaker: func(ctx context.Context, prompt string) (string, error) { return "", assert.AnError },
	}
	_, err := a.Script(context.Background(), "report")
	assert.ErrorIs(t, err, assert.AnError)
}

func TestImplSynthesizeNarratesTheScript(t *testing.T) {
	var gotInput string
	a := &impl{
		tts: func(ctx context.Context, input string) ([]byte, error) {
			gotInput = input
			return []byte("AUDIO"), nil
		},
	}
	audio, err := a.Synthesize(context.Background(), "the script")
	require.NoError(t, err)
	assert.Equal(t, "the script", gotInput, "TTS narrates exactly the script")
	assert.Equal(t, []byte("AUDIO"), audio)
}

func TestImplSynthesizeTooLongFailsClosed(t *testing.T) {
	called := false
	a := &impl{
		tts: func(ctx context.Context, input string) ([]byte, error) { called = true; return nil, nil },
	}
	_, err := a.Synthesize(context.Background(), strings.Repeat("x", 4097))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "4096", "the API input limit is named in the error")
	assert.False(t, called, "over-limit scripts never reach the speech endpoint")
}

func TestLocalSpeakUnavailable(t *testing.T) {
	l := &local{}
	_, err := l.Script(context.Background(), "report")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "offline")
	_, err = l.Synthesize(context.Background(), "script")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "offline")
}

func TestNewTTSRequiresSpeechEnv(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_SPEECH_MODEL", "")
	t.Setenv("OPENAI_SPEECH_VOICE", "")
	tts := newTTS(openai.NewClient(option.WithBaseURL("http://x/v1")))
	_, err := tts(context.Background(), "hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OPENAI_BASE_URL")
	assert.Contains(t, err.Error(), "OPENAI_SPEECH_MODEL")
	assert.Contains(t, err.Error(), "OPENAI_SPEECH_VOICE")
}

func TestNewTTSStreamsScriptAndReturnsAudio(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		gotBody, err = io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.Equal(t, "/chat/completions", r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"audio\":{\"id\":\"a1\",\"data\":\"TVAz\",\"transcript\":\"hello \"}}}]}\n\n",
			"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"audio\":{\"data\":\"QllURVM=\",\"transcript\":\"standup\"}},\"finish_reason\":\"stop\"}]}\n\n",
			"data: [DONE]\n\n",
		}
		for _, c := range chunks {
			_, err := w.Write([]byte(c))
			assert.NoError(t, err)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_SPEECH_MODEL", "test-tts")
	t.Setenv("OPENAI_SPEECH_VOICE", "test-voice")

	tts := newTTS(openai.NewClient(option.WithBaseURL(srv.URL)))
	audio, err := tts(context.Background(), "hello standup")
	require.NoError(t, err)
	want := wavWrap([]byte("MP3BYTES"))
	assert.Equal(t, want, audio, "base64 audio chunks are joined, decoded and wav-wrapped")
	body := string(gotBody)
	assert.Contains(t, body, `"stream":true`, "audio output requires streaming")
	assert.Contains(t, body, `"modalities":["text","audio"]`)
	assert.Contains(t, body, `"format":"pcm16"`, "endpoints stream raw pcm16 only")
	assert.Contains(t, body, `"model":"test-tts"`)
	assert.Contains(t, body, `"voice":"test-voice"`)
	assert.Contains(t, body, `"role":"system"`)
	assert.Contains(t, body, "Read the supplied script verbatim")
	assert.Contains(t, body, "SCRIPT TO READ VERBATIM")
	assert.Contains(t, body, "hello standup")
}

func TestNewTTSRejectsAudioThatAnswersInsteadOfNarrating(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, err := w.Write([]byte("data: {\"choices\":[{\"delta\":{\"audio\":{\"data\":\"QUJDRA==\",\"transcript\":\"Here is my answer\"}},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
		assert.NoError(t, err)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_SPEECH_MODEL", "test-tts")
	t.Setenv("OPENAI_SPEECH_VOICE", "test-voice")
	tts := newTTS(openai.NewClient(option.WithBaseURL(srv.URL)))
	_, err := tts(context.Background(), "Today I fixed login.")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not narrate")
}

func TestWavWrap(t *testing.T) {
	pcm := []byte{1, 2, 3, 4, 5}
	out := wavWrap(pcm)
	require.Len(t, out, 44+len(pcm))
	assert.Equal(t, "RIFF", string(out[0:4]))
	assert.Equal(t, uint32(36+len(pcm)), binary.LittleEndian.Uint32(out[4:]))
	assert.Equal(t, "WAVE", string(out[8:12]))
	assert.Equal(t, "fmt ", string(out[12:16]))
	assert.Equal(t, uint16(1), binary.LittleEndian.Uint16(out[20:]), "PCM format tag")
	assert.Equal(t, uint16(1), binary.LittleEndian.Uint16(out[22:]), "mono")
	assert.Equal(t, uint32(24000), binary.LittleEndian.Uint32(out[24:]), "speech pcm16 rate")
	assert.Equal(t, uint32(48000), binary.LittleEndian.Uint32(out[28:]), "byte rate")
	assert.Equal(t, uint16(16), binary.LittleEndian.Uint16(out[34:]), "16-bit samples")
	assert.Equal(t, "data", string(out[36:40]))
	assert.Equal(t, uint32(len(pcm)), binary.LittleEndian.Uint32(out[40:]))
	assert.Equal(t, pcm, out[44:], "payload preserved verbatim")
}

func TestNewTTSFailsClosedOnMissingAudio(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, err := w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"no audio here\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
		assert.NoError(t, err)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_SPEECH_MODEL", "test-tts")
	t.Setenv("OPENAI_SPEECH_VOICE", "test-voice")

	tts := newTTS(openai.NewClient(option.WithBaseURL(srv.URL)))
	_, err := tts(context.Background(), "hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no audio", "a text-only reply is a speech failure, not an empty file")
}

func TestLocalAddTasksSplitsParagraphs(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	st.Now = func() time.Time { return time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC) }
	l := &local{st: st}

	got, err := l.AddTasks(context.Background(), "first task\nsecond line of first\n\nsecond task\n\n\nthird task")
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "first task\nsecond line of first", got[0].Text, "paragraphs persist verbatim")
	assert.Equal(t, "second task", got[1].Text)
	assert.Equal(t, "third task", got[2].Text)
	assert.Equal(t, "todo", got[0].Status)
}

func TestLocalGenerateRendersTemplate(t *testing.T) {
	l := &local{genTpl: mustTpl(genTplText), daysTpl: mustTpl(daysTplText)}
	sec := report.Section{
		Days: []report.Day{
			{Heading: "Yesterday"},
			{Heading: "Today"},
		},
		Yesterday: []store.Task{{Text: "fix login bug", Status: "done", Timestamp: time.Date(2026, 8, 14, 9, 15, 0, 0, time.UTC)}},
		Today:     []store.Task{{Text: "ship release", Status: "blocked", Timestamp: time.Date(2026, 8, 15, 8, 5, 0, 0, time.UTC)}},
	}
	gen, err := l.Generate(context.Background(), sec)
	out := gen.Text
	require.NoError(t, err)
	want := "## Yesterday\n- [done] fix login bug (09:15)\n\n## Today\n- [blocked] ship release (08:05)\n"
	assert.Equal(t, want, out)
}

func TestLocalGenerateRangeRendersDaysTemplate(t *testing.T) {
	l := &local{genTpl: mustTpl(genTplText), daysTpl: mustTpl(daysTplText)}
	sec := report.Section{Days: []report.Day{
		{Heading: "Thu 2026-08-13"},
		{Heading: "Yesterday"},
		{Heading: "Today", Tasks: []store.Task{{Text: "ship", Status: "todo", Timestamp: time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)}}},
	}}
	gen, err := l.Generate(context.Background(), sec)
	out := gen.Text
	require.NoError(t, err)
	assert.Equal(t, "## Thu 2026-08-13\n## Yesterday\n## Today\n- [todo] ship (08:00)\n", out)
}

// An imported commit stores the whole message; a 1700-character body
// rendered as one bullet is not a report entry, so the committed templates
// keep the subject line and leave the body in the store.
func TestGenerateKeepsOnlyTheTaskSubjectLine(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	ass, err := Local(committedCfg(t), st)
	require.NoError(t, err)
	multi := store.Task{
		Text:      "feat: big thing\n\nline one of body\nline two of body",
		Status:    "done",
		Timestamp: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC),
	}

	// Default window (two-section template) and the days template must both
	// render one bullet holding the subject line only.
	sec := report.Section{
		Days:  []report.Day{{Heading: "Yesterday"}, {Heading: "Today", Tasks: []store.Task{multi}}},
		Today: []store.Task{multi},
	}
	gen, err := ass.Generate(context.Background(), sec)
	out := gen.Text
	require.NoError(t, err)
	assert.Contains(t, out, "- [done] feat: big thing (12:00)")
	assert.NotContains(t, out, "line one of body")

	sec = report.Section{Days: []report.Day{{Heading: "Today", Tasks: []store.Task{multi}}}}
	gen, err = ass.Generate(context.Background(), sec)
	out = gen.Text
	require.NoError(t, err)
	assert.Contains(t, out, "- [done] feat: big thing (12:00)")
	assert.NotContains(t, out, "line one of body")
}
func TestGenerateRendersBranchWhenRecorded(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	ass, err := Local(committedCfg(t), st)
	require.NoError(t, err)
	task := store.Task{
		Text:      "feat: x",
		Status:    "done",
		Branch:    "main",
		Timestamp: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC),
	}
	sec := report.Section{Days: []report.Day{{Heading: "Today", Tasks: []store.Task{task}}}}
	gen, err := ass.Generate(context.Background(), sec)
	out := gen.Text
	require.NoError(t, err)
	assert.Contains(t, out, "- [done] feat: x [main] (12:00)", "report rows attribute the branch when recorded")

	task.Branch = ""
	sec = report.Section{Days: []report.Day{{Heading: "Today", Tasks: []store.Task{task}}}}
	gen, err = ass.Generate(context.Background(), sec)
	out = gen.Text
	require.NoError(t, err)
	assert.Contains(t, out, "- [done] feat: x (12:00)", "no branch, no empty brackets")
}

func TestNewOfflineReturnsLocal(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	cfg := config.Config{
		Offline:               true,
		EditorInstructions:    "e",
		ReporterInstructions:  "r",
		GenerateInputTemplate: genTplText,
		DaysTemplate:          daysTplText,
	}
	ass, err := New(cfg, st)
	require.NoError(t, err)
	assert.IsType(t, &local{}, ass)

	got, err := ass.AddTasks(context.Background(), "one\n\ntwo")
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestNewRequiresProviderEnv(t *testing.T) {
	cfg := config.Config{
		EditorInstructions:    "e",
		ReporterInstructions:  "r",
		GenerateInputTemplate: genTplText,
		DaysTemplate:          daysTplText,
	}
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_MODEL", "")

	_, err := New(cfg, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OPENAI_BASE_URL")
	assert.Contains(t, err.Error(), "standup config set offline true")

	t.Setenv("OPENAI_BASE_URL", "http://x/v1")
	_, err = New(cfg, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OPENAI_MODEL")
}

func TestNewRequiresAnthropicProviderEnv(t *testing.T) {
	cfg := committedCfg(t)
	cfg.Provider = "anthropic"
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_MODEL", "")

	_, err := New(cfg, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ANTHROPIC_BASE_URL")
	assert.Contains(t, err.Error(), "ANTHROPIC_API_KEY")
	assert.Contains(t, err.Error(), "ANTHROPIC_MODEL")
	assert.NotContains(t, err.Error(), "OPENAI_BASE_URL")
}

func TestNewRejectsUnknownProvider(t *testing.T) {
	cfg := committedCfg(t)
	cfg.Provider = "mystery"

	_, err := New(cfg, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported provider")
}

func TestAnthropicAddTasksUsesMessagesAPI(t *testing.T) {
	var gotPath, gotKey, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","model":"test-model","content":[{"type":"text","text":"{\"tasks\":[{\"task\":\"fixed bug\",\"status\":\"todo\"}]}"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "must-not-shadow-explicit-api-key")
	t.Setenv("ANTHROPIC_MODEL", "test-model")
	cfg := committedCfg(t)
	cfg.Provider = "anthropic"
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)

	ass, err := New(cfg, st)
	require.NoError(t, err)
	tasks, err := ass.AddTasks(context.Background(), "fix bug")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "fixed bug", tasks[0].Text)
	assert.Equal(t, "/v1/messages", gotPath)
	assert.Equal(t, "test-key", gotKey)
	assert.Empty(t, gotAuth, "unrelated SDK environment defaults must not add a second auth method")
}

func TestNewPreflightsProviderEndpoint(t *testing.T) {
	t.Run("reachable regardless of HTTP status", func(t *testing.T) {
		called := make(chan struct{}, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called <- struct{}{}
			http.Error(w, "model route requires POST", http.StatusMethodNotAllowed)
		}))
		t.Cleanup(srv.Close)
		t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
		t.Setenv("OPENAI_MODEL", "test")

		_, err := New(committedCfg(t), nil)
		require.NoError(t, err)
		select {
		case <-called:
		case <-time.After(time.Second):
			t.Fatal("New did not check endpoint connectivity")
		}
	})

	t.Run("unreachable reports endpoint unavailable", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		secret := "query-secret-must-not-appear"
		endpoint := "http://" + listener.Addr().String() + "/v1?api-key=" + secret
		require.NoError(t, listener.Close())
		t.Setenv("OPENAI_BASE_URL", endpoint)
		t.Setenv("OPENAI_MODEL", "test")
		start := time.Now()
		_, err = New(committedCfg(t), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "endpoint unavailable")
		assert.NotContains(t, err.Error(), secret)
		assert.Less(t, time.Since(start), time.Second, "connection refusal should fail immediately")
	})

	t.Run("invalid URL does not disclose credentials", func(t *testing.T) {
		secret := "invalid-url-secret-must-not-appear"
		t.Setenv("OPENAI_BASE_URL", "http://example.invalid/v1?api-key="+secret+"\n")
		t.Setenv("OPENAI_MODEL", "test")

		_, err := New(committedCfg(t), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "OPENAI_BASE_URL is invalid")
		assert.NotContains(t, err.Error(), secret)
	})

	t.Run("silent endpoint times out quickly", func(t *testing.T) {
		done := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			<-done
		}))
		t.Cleanup(srv.Close)
		t.Cleanup(func() { close(done) })
		t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
		t.Setenv("OPENAI_MODEL", "test")

		start := time.Now()
		_, err := New(committedCfg(t), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "endpoint unavailable")
		assert.Contains(t, err.Error(), "timed out")
		assert.Less(t, time.Since(start), 3*time.Second, "preflight should not use the model-call timeout")
	})
}

func TestReportToolCallsAllowsOnlySpecialists(t *testing.T) {
	seen := make(map[string]bool)
	var got []string
	reportToolCalls(&frameworkagent.ResponseUpdate{Contents: message.Contents{
		&message.FunctionCallContent{Name: "creator", CallID: "one"},
		&message.FunctionCallContent{Name: "updater", CallID: "two"},
		&message.FunctionCallContent{Name: "deleter", CallID: "three"},
		&message.FunctionCallContent{Name: "creator\nforged\x1b[31m", CallID: "rogue"},
		&message.FunctionCallContent{Name: "unknown", CallID: "unknown"},
	}}, seen, func(s string) { got = append(got, s) })

	assert.Equal(t, []string{"tool creator", "tool updater", "tool deleter"}, got)
}

func TestReportToolCallsDoesNotDedupeEmptyCallIDs(t *testing.T) {
	seen := make(map[string]bool)
	var got []string
	reportToolCalls(&frameworkagent.ResponseUpdate{Contents: message.Contents{
		&message.FunctionCallContent{Name: "creator"},
		&message.FunctionCallContent{Name: "updater"},
	}}, seen, func(s string) { got = append(got, s) })

	assert.Equal(t, []string{"tool creator", "tool updater"}, got)
	assert.Empty(t, seen)
}

func TestCommittedPlannerPromptsTreatSnapshotAsUntrusted(t *testing.T) {
	cfg := committedCfg(t)
	for name, instructions := range map[string]string{
		"planner":          cfg.PlannerInstructions,
		"planner fallback": cfg.PlannerFallbackInstructions,
		"creator":          cfg.CreatorInstructions,
		"updater":          cfg.UpdaterInstructions,
		"deleter":          cfg.DeleterInstructions,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Contains(t, instructions, "sole authority")
			assert.Contains(t, instructions, "untrusted data")
			assert.Contains(t, instructions, "task text")
		})
	}
}

func TestNewOfflineSkipsEndpointPreflight(t *testing.T) {
	called := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called <- struct{}{}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_MODEL", "test")
	cfg := committedCfg(t)
	cfg.Offline = true

	_, err := New(cfg, nil)
	require.NoError(t, err)
	select {
	case <-called:
		t.Fatal("offline mode contacted the model endpoint")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestNewBadGenerateTemplate(t *testing.T) {
	cfg := config.Config{
		EditorInstructions:    "e",
		ReporterInstructions:  "r",
		GenerateInputTemplate: "{{",
		DaysTemplate:          daysTplText,
	}
	_, err := New(cfg, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "template")
}

func TestNewBadDaysTemplate(t *testing.T) {
	cfg := config.Config{
		EditorInstructions:    "e",
		ReporterInstructions:  "r",
		GenerateInputTemplate: genTplText,
		DaysTemplate:          "{{",
	}
	_, err := New(cfg, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "days template")
}

// committedCfg loads config from the repo's real config/ dir so tests cover
// the files users actually get (embedded defaults are byte-identical).
func committedCfg(t *testing.T) config.Config {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	t.Setenv("STANDUP_CONFIG_DIR", filepath.Join(wd, "..", "..", "config"))
	cfg, err := config.Load()
	require.NoError(t, err)
	return cfg
}

func TestCommittedTemplatesSkipEmptySections(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	ass, err := Local(committedCfg(t), st)
	require.NoError(t, err)

	sec := report.Section{
		Days: []report.Day{{Heading: "Yesterday"}, {Heading: "Today"}},
		Today: []store.Task{
			{Text: "ship release", Status: "in-progress", Timestamp: time.Date(2026, 8, 15, 8, 5, 0, 0, time.UTC)},
		},
	}
	gen, err := ass.Generate(context.Background(), sec)
	out := gen.Text
	require.NoError(t, err)
	assert.Contains(t, out, "## Today")
	assert.Contains(t, out, "ship release")
	assert.NotContains(t, out, "## Yesterday", "empty sections are not rendered")
}

func TestAddTasksTimesOutOnSilentEndpoint(t *testing.T) {
	// done unblocks the handler at cleanup so srv.Close does not wait out
	// the sleep (cleanups run LIFO: close(done) fires before srv.Close).
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		select {
		case <-time.After(10 * time.Second): // silent but established: worse than a black hole
		case <-done:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(done) })
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_MODEL", "test")

	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	cfg := committedCfg(t)
	cfg.ModelCallTimeout = 100 * time.Millisecond
	ass, err := New(cfg, st)
	require.NoError(t, err)

	start := time.Now()
	_, err = ass.AddTasks(context.Background(), "fix bug")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "the client timeout bounds the call, not the SDK default")
	tasks, err := st.List()
	require.NoError(t, err)
	assert.Empty(t, tasks, "timeout must not leave partial writes")
}

func TestCommittedDaysTemplateSkipsEmptyDays(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	ass, err := Local(committedCfg(t), st)
	require.NoError(t, err)

	sec := report.Section{Days: []report.Day{
		{Heading: "Thu 2026-08-13"},
		{Heading: "Yesterday", Tasks: []store.Task{
			{Text: "fix bug", Status: "done", Timestamp: time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)},
		}},
		{Heading: "Today", Tasks: []store.Task{
			{Text: "ship", Status: "todo", Timestamp: time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)},
		}},
	}}
	gen, err := ass.Generate(context.Background(), sec)
	out := gen.Text
	require.NoError(t, err)
	assert.Contains(t, out, "## Yesterday")
	assert.Contains(t, out, "## Today")
	assert.NotContains(t, out, "## Thu 2026-08-13", "empty days are not rendered")
}

// openAIStub answers the preflight HEAD and every chat completion with the
// given status and body: a doctor that only checks presence and reachability
// passes setups that fail on the very next command.
func openAIStub(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, err := w.Write([]byte(body))
		assert.NoError(t, err)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_MODEL", "test-model")
	return srv.URL
}

const okCompletion = `{"id":"c","object":"chat.completion","created":0,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`

func TestCheckPassesWhenTheModelAnswers(t *testing.T) {
	openAIStub(t, http.StatusOK, okCompletion)
	require.NoError(t, Check(context.Background(), committedCfg(t)))
}

func TestCheckNamesTheApiKeyOnRejectedCredentials(t *testing.T) {
	openAIStub(t, http.StatusUnauthorized, `{"error":{"message":"User not found.","type":"authentication_error"}}`)
	err := Check(context.Background(), committedCfg(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OPENAI_API_KEY")
	assert.NotContains(t, err.Error(), "OPENAI_BASE_URL", "the base URL is demonstrably fine")
}

func TestCheckNamesTheModelOnRejectedModel(t *testing.T) {
	openAIStub(t, http.StatusBadRequest, `{"error":{"message":"not/a-real-model is not a valid model ID","code":400}}`)
	err := Check(context.Background(), committedCfg(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OPENAI_MODEL")
}

func TestCheckReportsMissingProviderEnv(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_MODEL", "")
	err := Check(context.Background(), committedCfg(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OPENAI_BASE_URL")
}

// The same misdirection reaches ordinary commands: a 401 blamed the base URL
// and the network, the two things that were working.
func TestAddTasksNamesTheApiKeyOnRejectedCredentials(t *testing.T) {
	openAIStub(t, http.StatusUnauthorized, `{"error":{"message":"User not found."}}`)
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	ass, err := New(committedCfg(t), st)
	require.NoError(t, err)
	_, err = ass.AddTasks(context.Background(), "fix bug")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OPENAI_API_KEY")
}

// oneTaskSection is a minimal default-window section holding one task.
func oneTaskSection(task store.Task) report.Section {
	return report.Section{
		Days:  []report.Day{{Heading: "Yesterday"}, {Heading: "Today", Tasks: []store.Task{task}}},
		Today: []store.Task{task},
	}
}

// The fallback fires precisely when the input is large and messy — exactly
// when verbatim task text is least usable — and the user shipped a raw commit
// dump believing the model wrote it.
func TestGenerateNamesTheVerbatimFallback(t *testing.T) {
	sec := oneTaskSection(store.Task{Text: "ship release", Status: "done", Timestamp: time.Date(2026, 8, 15, 8, 5, 0, 0, time.UTC)})
	tests := []struct {
		name, reply, wantReason string
		replyErr                error
	}{
		{name: "count mismatch", reply: `{"tasks":["a","b"]}`, wantReason: "2 entries for 1 tasks"},
		{name: "not json", reply: "sorry, here is your report", wantReason: "no JSON entries"},
		{name: "empty entry", reply: `{"tasks":["  "]}`, wantReason: "came back empty"},
		{name: "call failed", replyErr: assert.AnError, wantReason: "the model call failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &impl{
				reporter: func(context.Context, string) (string, error) { return tt.reply, tt.replyErr },
				genTpl:   mustTpl(genTplText),
				daysTpl:  mustTpl(daysTplText),
			}
			gen, err := a.Generate(context.Background(), sec)
			require.NoError(t, err, "the report is still rendered")
			assert.Contains(t, gen.Text, "ship release")
			assert.Contains(t, gen.Fallback, tt.wantReason)
		})
	}
}

func TestGenerateReportsNoFallbackWhenTheModelAnswers(t *testing.T) {
	a := &impl{
		reporter: func(context.Context, string) (string, error) { return `{"tasks":["Shipped the release"]}`, nil },
		genTpl:   mustTpl(genTplText),
		daysTpl:  mustTpl(daysTplText),
	}
	gen, err := a.Generate(context.Background(), oneTaskSection(store.Task{Text: "ship release", Status: "done", Timestamp: time.Now()}))
	require.NoError(t, err)
	assert.Empty(t, gen.Fallback)
}

// A commit body is stored in full but only its subject is reported, so that
// is what the reporter is asked to rephrase — 39 imported commits used to
// bury the JSON contract under tens of kilobytes of input.
func TestGenerateSendsOnlySubjectLinesToTheReporter(t *testing.T) {
	var gotPrompt string
	a := &impl{
		reporter: func(_ context.Context, prompt string) (string, error) {
			gotPrompt = prompt
			return `{"tasks":["Added sync"]}`, nil
		},
		genTpl:  mustTpl(genTplText),
		daysTpl: mustTpl(daysTplText),
	}
	task := store.Task{
		Text:      "add sync: merge tasks with a PocketBase server\n\nMerging is deterministic and model-free: tasks union by id.",
		Status:    "done",
		Timestamp: time.Now(),
	}
	_, err := a.Generate(context.Background(), oneTaskSection(task))
	require.NoError(t, err)
	assert.Contains(t, gotPrompt, "- add sync: merge tasks with a PocketBase server")
	assert.NotContains(t, gotPrompt, "union by id")
}

// #word is this app's own tag syntax (`list --tag`): a rephraser minting one
// makes reports display a tag the task does not carry.
func TestGenerateDropsInventedTags(t *testing.T) {
	a := &impl{
		reporter: func(context.Context, string) (string, error) {
			return `{"tasks":["Fixed login redirect bug #1 for #auth"]}`, nil
		},
		genTpl:  mustTpl(genTplText),
		daysTpl: mustTpl(daysTplText),
	}
	task := store.Task{Text: "fixd teh login redirct bug numbr 1 lol #auth", Status: "done", Timestamp: time.Now()}
	gen, err := a.Generate(context.Background(), oneTaskSection(task))
	require.NoError(t, err)
	assert.Contains(t, gen.Text, "bug 1", "the invented #1 keeps its word and loses the tag")
	assert.NotContains(t, gen.Text, "#1")
	assert.Contains(t, gen.Text, "#auth", "a tag the task carries survives")
}

// The day split is decided in Go and carried in the report's headings. A
// brief that moves today's work to yesterday is a factual error about the
// user's own work, spoken aloud in a meeting.
func TestImplScriptRejectsADayTheReportDoesNotName(t *testing.T) {
	reportText := "## Today\n- [done] Updated the README (09:00)"
	for _, spoken := range []string{
		"Yesterday I updated the README.",
		"On Monday I updated the README.",
		"Tomorrow I update the README.",
	} {
		t.Run(spoken, func(t *testing.T) {
			a := &impl{speaker: func(context.Context, string) (string, error) { return spoken, nil }}
			script, err := a.Script(context.Background(), reportText)
			require.NoError(t, err)
			assert.Equal(t, "Today: Updated the README.", script)
		})
	}
}

func TestImplScriptKeepsADayTheReportNames(t *testing.T) {
	a := &impl{speaker: func(context.Context, string) (string, error) {
		return "Yesterday I updated the README.", nil
	}}
	script, err := a.Script(context.Background(), "## Yesterday\n- [done] Updated the README (09:00)")
	require.NoError(t, err)
	assert.Equal(t, "Yesterday I updated the README.", script)
}

func TestImplScriptAcceptsAWeekdayHeading(t *testing.T) {
	a := &impl{speaker: func(context.Context, string) (string, error) {
		return "On Monday I updated the README.", nil
	}}
	script, err := a.Script(context.Background(), "## Mon 2026-08-17\n- [done] Updated the README (09:00)")
	require.NoError(t, err)
	assert.Equal(t, "On Monday I updated the README.", script)
}

// Cutting mid-word removed exactly the part that was about to tell the user
// what they could have matched.
func TestNoApplicableChangesClipsOnAWordBoundary(t *testing.T) {
	long := "Could not find any task matching 'kubernetes migration' in the task list. The existing tasks are about login redirect, release PR, documentation, deployment, integration"
	err := noApplicableChanges(long)
	require.Error(t, err)
	msg := err.Error()
	assert.True(t, strings.HasSuffix(msg, "…"))
	assert.False(t, strings.HasSuffix(msg, "in…"), "no mid-word cut")
	trimmed := strings.TrimSuffix(msg, "…")
	assert.False(t, strings.HasSuffix(trimmed, " "))
	assert.True(t, strings.HasSuffix(trimmed, "deployment,") || strings.HasSuffix(trimmed, "deployment"), "clipped at the last full word: "+msg)
}

// The editor minted "#1" out of "numbr 1 lol"; a stored tag the user never
// wrote is worse than a rephrased one, because `list --tag` finds it.
func TestImplAddTasksDropsInventedTags(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	a := &impl{
		editor: func(context.Context, string) (string, error) {
			return `{"tasks":["Fixed login redirect bug #1","Reviewed the #auth service"]}`, nil
		},
		st: st,
	}
	got, err := a.AddTasks(context.Background(), "fixd teh login redirct bug numbr 1 lol and reviewd the #auth service")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "Fixed login redirect bug 1", got[0].Text)
	assert.Equal(t, "Reviewed the #auth service", got[1].Text, "a tag from the note survives")
}

// Go renders the bullets: an entry that carries its own turns into
// "- [done] - fixed the bug" and travels on into the spoken brief.
func TestGenerateStripsEchoedListMarkers(t *testing.T) {
	a := &impl{
		reporter: func(context.Context, string) (string, error) {
			return `{"tasks":["- Fixed the login bug"]}`, nil
		},
		genTpl:  mustTpl(genTplText),
		daysTpl: mustTpl(daysTplText),
	}
	gen, err := a.Generate(context.Background(), oneTaskSection(store.Task{Text: "fix login", Status: "done", Timestamp: time.Now()}))
	require.NoError(t, err)
	assert.Contains(t, gen.Text, "- [done] Fixed the login bug")
	assert.NotContains(t, gen.Text, "] - ")
}

func TestSpokenFallbackEndsEachSentenceOnce(t *testing.T) {
	script := spokenFallback("## Today\n- [done] Fixed the bug number two. (09:00)\n- [todo] Ship it (10:00)")
	assert.Equal(t, "Today: Fixed the bug number two. Also, Ship it.", script)
}
