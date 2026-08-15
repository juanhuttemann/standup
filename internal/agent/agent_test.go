package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"text/template"
	"time"

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
		want    []extracted
		wantErr bool
	}{
		{"plain json", `{"tasks":["fix login","write tests"]}`, []extracted{{text: "fix login"}, {text: "write tests"}}, false},
		{"objects with status", `{"tasks":[{"task":"waiting on infra","status":"blocked"},{"task":"ship it","status":"done"}]}`, []extracted{{text: "waiting on infra", status: "blocked"}, {text: "ship it", status: "done"}}, false},
		{"mixed forms", `{"tasks":["plain",{"task":"obj","status":"blocked"}]}`, []extracted{{text: "plain"}, {text: "obj", status: "blocked"}}, false},
		{"fenced", "```json\n{\"tasks\":[\"a\"]}\n```", []extracted{{text: "a"}}, false},
		{"text around", "Here you go:\n{\"tasks\":[\"a\",\"b\"]}\nhope that helps", []extracted{{text: "a"}, {text: "b"}}, false},
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
	assert.Equal(t, "todo", got[0].Status)
	assert.Equal(t, "blocked", got[1].Status, "editor-provided status travels to the store")

	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, "blocked", tasks[1].Status)
}

func TestImplAddTasksEditorError(t *testing.T) {
	a := &impl{
		editor: func(ctx context.Context, prompt string) (string, error) { return "", assert.AnError },
	}
	_, err := a.AddTasks(context.Background(), "raw")
	assert.ErrorIs(t, err, assert.AnError)
}

func TestImplGenerateWiring(t *testing.T) {
	var gotPrompt string
	a := &impl{
		reporter: func(ctx context.Context, prompt string) (string, error) { gotPrompt = prompt; return "markdown", nil },
		genTpl:   mustTpl(genTplText),
		daysTpl:  mustTpl(daysTplText),
	}
	sec := report.Section{
		Days: []report.Day{
			{Heading: "Yesterday"},
			{Heading: "Today"},
		},
		Yesterday: []store.Task{{Text: "fix login bug", Status: "done", Timestamp: time.Date(2026, 8, 14, 9, 15, 0, 0, time.UTC)}},
		Today:     []store.Task{{Text: "ship release", Status: "done", Timestamp: time.Date(2026, 8, 15, 8, 5, 0, 0, time.UTC)}},
	}
	out, err := a.Generate(context.Background(), sec)
	require.NoError(t, err)
	assert.Equal(t, "markdown", out)
	assert.Contains(t, gotPrompt, "## Yesterday")
	assert.Contains(t, gotPrompt, "[done] fix login bug (09:15)")
	assert.Contains(t, gotPrompt, "## Today")
	assert.Contains(t, gotPrompt, "[done] ship release (08:05)")
}

func TestImplGenerateRangeUsesDaysTemplate(t *testing.T) {
	var gotPrompt string
	a := &impl{
		reporter: func(ctx context.Context, prompt string) (string, error) { gotPrompt = prompt; return "markdown", nil },
		genTpl:   mustTpl(genTplText),
		daysTpl:  mustTpl(daysTplText),
	}
	sec := report.Section{Days: []report.Day{
		{Heading: "Thu 2026-08-13", Tasks: []store.Task{{Text: "old", Status: "done", Timestamp: time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)}}},
		{Heading: "Yesterday"},
		{Heading: "Today"},
	}}
	out, err := a.Generate(context.Background(), sec)
	require.NoError(t, err)
	assert.Equal(t, "markdown", out)
	assert.Contains(t, gotPrompt, "## Thu 2026-08-13")
	assert.Contains(t, gotPrompt, "[done] old (09:00)")
	assert.NotContains(t, gotPrompt, "## Yesterday\n\n## Today\n\n", "two-section template not used for ranges")
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
	out, err := l.Generate(context.Background(), sec)
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
	out, err := l.Generate(context.Background(), sec)
	require.NoError(t, err)
	assert.Equal(t, "## Thu 2026-08-13\n## Yesterday\n## Today\n- [todo] ship (08:00)\n", out)
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
	assert.Contains(t, err.Error(), "offline: true")

	t.Setenv("OPENAI_BASE_URL", "http://x/v1")
	_, err = New(cfg, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OPENAI_MODEL")
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
	out, err := ass.Generate(context.Background(), sec)
	require.NoError(t, err)
	assert.Contains(t, out, "## Today")
	assert.Contains(t, out, "ship release")
	assert.NotContains(t, out, "## Yesterday", "empty sections are not rendered")
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
	out, err := ass.Generate(context.Background(), sec)
	require.NoError(t, err)
	assert.Contains(t, out, "## Yesterday")
	assert.Contains(t, out, "## Today")
	assert.NotContains(t, out, "## Thu 2026-08-13", "empty days are not rendered")
}
