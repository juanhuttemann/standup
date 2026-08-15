package agent

import (
	"context"
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

func TestExtractTasks(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{"plain json", `{"tasks":["fix login","write tests"]}`, []string{"fix login", "write tests"}, false},
		{"fenced", "```json\n{\"tasks\":[\"a\"]}\n```", []string{"a"}, false},
		{"text around", "Here you go:\n{\"tasks\":[\"a\",\"b\"]}\nhope that helps", []string{"a", "b"}, false},
		{"missing", "no json here at all", nil, true},
		{"empty array", `{"tasks":[]}`, nil, true},
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
			return `{"tasks":["Fixed login bug","Deployed the API"]}`, nil
		},
		st: st,
	}
	got, err := a.AddTasks(context.Background(), "raw")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "Fixed login bug", got[0].Text)

	tasks, err := st.List()
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, "Fixed login bug", tasks[0].Text)
	assert.Equal(t, "todo", tasks[0].Status)
	assert.Equal(t, "Deployed the API", tasks[1].Text)
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
		genTpl:   template.Must(template.New("generate").Parse(genTplText)),
	}
	sec := report.Section{
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

func TestNewBadGenerateTemplate(t *testing.T) {
	cfg := config.Config{
		BaseURL: "http://x/v1", Model: "m",
		EditorInstructions: "e", ReporterInstructions: "r",
		GenerateInputTemplate: "{{",
	}
	_, err := New(cfg, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "template")
}
