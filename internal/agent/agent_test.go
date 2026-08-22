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

const tplText = `{{range .Days}}## {{.Heading}}
{{range .Groups}}### {{.Label}}
{{range .Tasks}}- {{.Text}} ({{.Timestamp.Format "15:04"}})
{{end}}{{end}}{{end}}{{if .Blockers}}## Blockers
{{range .Blockers}}- {{.Text}} ({{.Timestamp.Format "15:04"}})
{{end}}{{end}}`

func mustTpl(s string) *template.Template {
	return template.Must(template.New("t").Parse(s))
}

// day builds a one-group day section for the tests that only care about the
// wording that reaches the report.
func day(heading, label string, tasks ...store.Task) report.Day {
	return report.Day{Heading: heading, Groups: []report.Group{{Label: label, Status: tasks[0].Status, Tasks: tasks}}}
}

func at(h, min int, text, status string) store.Task {
	return store.Task{Text: text, Status: status, Timestamp: time.Date(2026, 8, 15, h, min, 0, 0, time.UTC)}
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
		`{"operations":[{"kind":"edit","id":"361640ae-ca64-41a2-8096-be9eb3669c01","status":"todo"}],"message":null}`,
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

// The direct planner answers first: delegating to the specialists on every
// prompt spent five model calls on work a single pass resolves.
func TestImplPlanUsesTheDirectPlannerFirst(t *testing.T) {
	var directPrompt string
	delegated := false
	a := &impl{
		planner: func(context.Context, string, func(string)) (string, error) {
			delegated = true
			return `{"operations":[{"kind":"create","text":"delegated"}]}`, nil
		},
		plannerDirect: func(_ context.Context, prompt string) (string, error) {
			directPrompt = prompt
			return `{"operations":[{"kind":"create","text":"ship"}]}`, nil
		},
	}
	ops, err := a.Plan(context.Background(), "add ship", nil, time.Now())
	require.NoError(t, err)
	require.Len(t, ops, 1)
	assert.Equal(t, store.OperationCreate, ops[0].Kind)
	assert.Equal(t, "ship", ops[0].Text)
	assert.Contains(t, directPrompt, `"prompt":"add ship"`)
	assert.False(t, delegated, "a resolved single pass never reaches the specialists")
}

// A refusal or an off-contract answer is exactly where the specialists earn
// their cost, so the coordinator still runs then.
func TestImplPlanDelegatesWhenTheDirectPlannerCannotResolve(t *testing.T) {
	for _, directOut := range []string{"not json", `{"operations":[],"message":"which caching task?"}`} {
		var progress []string
		a := &impl{
			planner: func(_ context.Context, _ string, report func(string)) (string, error) {
				return `{"operations":[{"kind":"create","text":"delegated"}]}`, nil
			},
			plannerDirect: func(context.Context, string) (string, error) { return directOut, nil },
		}
		ops, err := a.PlanWithProgress(context.Background(), "p", nil, time.Now(), func(m string) {
			progress = append(progress, m)
		})
		require.NoError(t, err, directOut)
		require.Len(t, ops, 1)
		assert.Equal(t, "delegated", ops[0].Text)
		assert.Contains(t, progress, "delegating to specialists")
	}
}

func TestImplPlanReportsBothOutputsWhenNeitherPathParses(t *testing.T) {
	a := &impl{
		planner:       func(context.Context, string, func(string)) (string, error) { return "also not json", nil },
		plannerDirect: func(context.Context, string) (string, error) { return "not json", nil },
	}
	_, err := a.Plan(context.Background(), "p", nil, time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "direct output")
	assert.Contains(t, err.Error(), "delegated output")
}

// The curator's job is editorial: 28 commit subjects are not a standup, and
// merging them is the one part of the pipeline a model is actually good at.
func TestImplGenerateCuratesViaCurator(t *testing.T) {
	var gotPrompt string
	a := &impl{
		curator: func(_ context.Context, prompt string) (string, error) {
			gotPrompt = prompt
			return `{"entries":[{"text":"Released v0.1.0 through v0.3.0","sources":[1,2,3]},{"text":"Ship the release","sources":[4]}]}`, nil
		},
		tpl: mustTpl(tplText),
	}
	sec := report.Section{Days: []report.Day{
		day("Yesterday", "Done",
			at(9, 15, "release v0.1.0", "done"),
			at(9, 30, "release v0.2.0", "done"),
			at(9, 45, "release v0.3.0", "done")),
		day("Today", "Next", at(8, 5, "ship release", "todo")),
	}}
	gen, err := a.Generate(context.Background(), sec)
	require.NoError(t, err)
	assert.Empty(t, gen.Fallback)
	assert.NotContains(t, gotPrompt, "merge related", "agent instructions must not be duplicated in user input")
	assert.Contains(t, gotPrompt, "## Yesterday\n### Done\n1. release v0.1.0")
	assert.Contains(t, gotPrompt, "4. ship release", "numbering runs across sections")
	assert.Contains(t, gen.Text, "- Released v0.1.0 through v0.3.0 (09:15)",
		"three entries became one, timed by the earliest of them")
	assert.Contains(t, gen.Text, "- Ship the release (08:05)")
	assert.NotContains(t, gen.Text, "release v0.2.0", "merged entries are gone from the report")
}

func TestImplGenerateRejectsCurationThatLosesOrMovesWork(t *testing.T) {
	tests := []struct {
		name, out, reason string
	}{
		{"dropped entry", `{"entries":[{"text":"a","sources":[1]}]}`, "was dropped"},
		{"reused entry", `{"entries":[{"text":"a","sources":[1,1]}]}`, "used twice"},
		{"invented entry", `{"entries":[{"text":"a","sources":[1]},{"text":"b","sources":[9]}]}`, "does not exist"},
		{"crossed sections", `{"entries":[{"text":"a","sources":[1,2]}]}`, "mixed two sections"},
		{"empty text", `{"entries":[{"text":" ","sources":[1]},{"text":"b","sources":[2]}]}`, "came back empty"},
		{"no sources", `{"entries":[{"text":"a","sources":[]},{"text":"b","sources":[2]}]}`, "cited no input line"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &impl{
				curator: func(context.Context, string) (string, error) { return tt.out, nil },
				tpl:     mustTpl(tplText),
			}
			sec := report.Section{Days: []report.Day{
				day("Yesterday", "Done", at(9, 15, "fix login bug", "done")),
				day("Today", "Done", at(8, 5, "ship release", "done")),
			}}
			gen, err := a.Generate(context.Background(), sec)
			require.NoError(t, err, "a refused curation still produces a report")
			assert.Contains(t, gen.Fallback, tt.reason)
			assert.Contains(t, gen.Text, "- fix login bug (09:15)", "the stored texts are the fallback")
			assert.Contains(t, gen.Text, "- ship release (08:05)")
		})
	}
}

// A merged entry keeps the branch only when every line it covers shared it:
// attributing three branches' work to one of them is a fabricated fact.
func TestCurationKeepsOnlyTheSharedBranch(t *testing.T) {
	a := &impl{
		curator: func(context.Context, string) (string, error) {
			return `{"entries":[{"text":"Reworked the parser","sources":[1,2]}]}`, nil
		},
		tpl: mustTpl(`{{range .Days}}{{range .Groups}}{{range .Tasks}}{{.Text}}|{{.Branch}}{{end}}{{end}}{{end}}`),
	}
	same := day("Today", "Done", at(9, 0, "a", "done"), at(9, 5, "b", "done"))
	same.Groups[0].Tasks[0].Branch, same.Groups[0].Tasks[1].Branch = "feat", "feat"
	gen, err := a.Generate(context.Background(), report.Section{Days: []report.Day{same}})
	require.NoError(t, err)
	assert.Equal(t, "Reworked the parser|feat", gen.Text)

	mixed := day("Today", "Done", at(9, 0, "a", "done"), at(9, 5, "b", "done"))
	mixed.Groups[0].Tasks[0].Branch, mixed.Groups[0].Tasks[1].Branch = "feat", "main"
	gen, err = a.Generate(context.Background(), report.Section{Days: []report.Day{mixed}})
	require.NoError(t, err)
	assert.Equal(t, "Reworked the parser|", gen.Text)
}

func TestCurationDropsTagsTheCuratorInvented(t *testing.T) {
	a := &impl{
		curator: func(context.Context, string) (string, error) {
			return `{"entries":[{"text":"Fixed the cache #checkout #invented","sources":[1]}]}`, nil
		},
		tpl: mustTpl(`{{range .Days}}{{range .Groups}}{{range .Tasks}}{{.Text}}{{end}}{{end}}{{end}}`),
	}
	sec := report.Section{Days: []report.Day{day("Today", "Done", at(9, 0, "fixed cache #checkout", "done"))}}
	gen, err := a.Generate(context.Background(), sec)
	require.NoError(t, err)
	assert.Equal(t, "Fixed the cache #checkout invented", gen.Text)
}

func TestImplGenerateLanguageInPrompt(t *testing.T) {
	var gotPrompt string
	a := &impl{
		curator: func(ctx context.Context, prompt string) (string, error) { gotPrompt = prompt; return "no json", nil },
		lang:    "German",
		tpl:     mustTpl(tplText),
	}
	sec := report.Section{Days: []report.Day{day("Today", "Next", at(9, 0, "x", "todo"))}}
	_, err := a.Generate(context.Background(), sec)
	require.NoError(t, err)
	assert.Contains(t, gotPrompt, "German")
}

func TestImplGenerateFallsBackDeterministically(t *testing.T) {
	tests := []struct {
		name    string
		curator func(ctx context.Context, prompt string) (string, error)
	}{
		{"curator error", func(ctx context.Context, prompt string) (string, error) { return "", assert.AnError }},
		{"not json", func(ctx context.Context, prompt string) (string, error) { return "sure thing!", nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &impl{curator: tt.curator, tpl: mustTpl(tplText)}
			sec := report.Section{Days: []report.Day{
				day("Yesterday", "Done", at(9, 15, "fix login bug", "done")),
				day("Today", "Done", at(8, 5, "ship release", "done")),
			}}
			gen, err := a.Generate(context.Background(), sec)
			require.NoError(t, err, "fallback keeps generate working")
			assert.NotEmpty(t, gen.Fallback, "a silent fallback ships a raw work log as if the model wrote it")
			assert.Contains(t, gen.Text, "- fix login bug (09:15)")
			assert.Contains(t, gen.Text, "- ship release (08:05)")
		})
	}
}

func TestImplGenerateRendersEveryDayHeading(t *testing.T) {
	a := &impl{
		curator: func(ctx context.Context, prompt string) (string, error) { return "", assert.AnError },
		tpl:     mustTpl(tplText),
	}
	sec := report.Section{Days: []report.Day{
		day("Thu 2026-08-13", "Done", at(9, 0, "old", "done")),
		{Heading: "Fri 2026-08-14"},
		day("Sat 2026-08-15", "Next", at(9, 0, "new", "todo")),
	}}
	gen, err := a.Generate(context.Background(), sec)
	require.NoError(t, err)
	assert.Contains(t, gen.Text, "## Thu 2026-08-13")
	assert.Contains(t, gen.Text, "### Done")
	assert.Contains(t, gen.Text, "### Next")
	assert.Contains(t, gen.Text, "## Sat 2026-08-15")
}

func TestExtractEntries(t *testing.T) {
	got, err := extractEntries(`{"entries":[{"text":"a","sources":[1]},{"text":"b","sources":[2,3]}]}`)
	require.NoError(t, err)
	assert.Equal(t, []curatedEntry{{Text: "a", Sources: []int{1}}, {Text: "b", Sources: []int{2, 3}}}, got)

	got, err = extractEntries("sure:\n```json\n{\"entries\":[{\"text\":\"x\",\"sources\":[1]}]}\n```")
	require.NoError(t, err)
	assert.Equal(t, []curatedEntry{{Text: "x", Sources: []int{1}}}, got)

	_, err = extractEntries(`{"entries":[]}`)
	assert.Error(t, err)
	_, err = extractEntries("no json")
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
	script, err := a.Script(context.Background(), "## Yesterday\n### Done\n- fix login bug")
	require.NoError(t, err)
	assert.NotContains(t, gotPrompt, "speak plainly", "agent instructions must not be duplicated in user input")
	assert.Contains(t, gotPrompt, "## Yesterday\n### Done\n- fix login bug", "the rendered report is the speaker input")
	assert.Equal(t, "Yesterday: fix login bug.", script, "invented release work falls back to the report")
}

func TestImplScriptFallsBackWhenSpeakerIsUngrounded(t *testing.T) {
	a := &impl{speaker: func(context.Context, string) (string, error) {
		return "User Safety: safe", nil
	}}
	reportText := "## Today\n### Done\n- Review deployment #ops (10:00)\n- Fixed the cache (10:30)\n- Merged the parser branch (11:00)\n### Next\n- Write API docs (09:00)"
	script, err := a.Script(context.Background(), reportText)
	require.NoError(t, err)
	assert.Equal(t, "Today: Review deployment ops. Also, Fixed the cache. Then, Merged the parser branch. Next up: Write API docs.", script,
		"one anchor per section, varied connectives after it, and planned work is announced as planned")
	assert.NotContains(t, script, "#", "a listener hears markup read aloud as \"hashtag\"")
}

func TestImplScriptFallsBackOnInventedAdvice(t *testing.T) {
	a := &impl{speaker: func(context.Context, string) (string, error) {
		return "I fixed login and should make sure deployment works.", nil
	}}
	reportText := "## Today\n### Done\n- Fixed login (09:00)"
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
	l := &local{tpl: mustTpl(tplText)}
	sec := report.Section{
		Days: []report.Day{
			day("Yesterday", "Done", at(9, 15, "fix login bug", "done")),
			{Heading: "Today"},
		},
		Blockers: []store.Task{at(8, 5, "ship release", "blocked")},
	}
	gen, err := l.Generate(context.Background(), sec)
	require.NoError(t, err)
	assert.Equal(t, "## Yesterday\n### Done\n- fix login bug (09:15)\n## Today\n## Blockers\n- ship release (08:05)\n", gen.Text)
	assert.Empty(t, gen.Fallback, "offline is not a fallback: no model was going to phrase these")
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

	sec := report.Section{Days: []report.Day{{Heading: "Yesterday"}, day("Today", "Done", multi)}}
	gen, err := ass.Generate(context.Background(), sec)
	out := gen.Text
	require.NoError(t, err)
	assert.Contains(t, out, "- Feat: big thing (12:00)")
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
	sec := report.Section{Days: []report.Day{day("Today", "Done", task)}}
	gen, err := ass.Generate(context.Background(), sec)
	out := gen.Text
	require.NoError(t, err)
	assert.Contains(t, out, "- Feat: x [main] (12:00)", "report rows attribute the branch when recorded")

	task.Branch = ""
	sec = report.Section{Days: []report.Day{day("Today", "Done", task)}}
	gen, err = ass.Generate(context.Background(), sec)
	out = gen.Text
	require.NoError(t, err)
	assert.Contains(t, out, "- Feat: x (12:00)", "no branch, no empty brackets")
}

func TestNewOfflineReturnsLocal(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	cfg := config.Config{
		Offline:               true,
		EditorInstructions:    "e",
		CuratorInstructions:   "c",
		GenerateInputTemplate: tplText,
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
		CuratorInstructions:   "c",
		GenerateInputTemplate: tplText,
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
		"planner":        cfg.PlannerInstructions,
		"planner direct": cfg.PlannerDirectInstructions,
		"creator":        cfg.CreatorInstructions,
		"updater":        cfg.UpdaterInstructions,
		"deleter":        cfg.DeleterInstructions,
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
		CuratorInstructions:   "c",
		GenerateInputTemplate: "{{",
	}
	_, err := New(cfg, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "template")
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

func TestCommittedTemplateSkipsEmptySections(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	require.NoError(t, err)
	ass, err := Local(committedCfg(t), st)
	require.NoError(t, err)

	sec := report.Section{Days: []report.Day{
		{Heading: "Thu 2026-08-13"},
		{Heading: "Yesterday"},
		day("Today", "In progress", at(8, 5, "ship release", "in-progress")),
	}}
	gen, err := ass.Generate(context.Background(), sec)
	out := gen.Text
	require.NoError(t, err)
	assert.Contains(t, out, "## Today")
	assert.Contains(t, out, "### In progress")
	assert.Contains(t, out, "- Ship release (08:05)", "the render normalizes the entry's register")
	assert.NotContains(t, out, "## Yesterday", "empty days are not rendered")
	assert.NotContains(t, out, "## Thu 2026-08-13")
	assert.NotContains(t, out, "## Blockers", "no blockers, no Blockers section")
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
	return report.Section{Days: []report.Day{{Heading: "Yesterday"}, day("Today", "Done", task)}}
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
		{name: "dropped work", reply: `{"entries":[{"text":"a","sources":[]}]}`, wantReason: "cited no input line"},
		{name: "not json", reply: "sorry, here is your report", wantReason: "no JSON entries"},
		{name: "empty entry", reply: `{"entries":[{"text":"  ","sources":[1]}]}`, wantReason: "came back empty"},
		{name: "call failed", replyErr: assert.AnError, wantReason: "the model call failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &impl{
				curator: func(context.Context, string) (string, error) { return tt.reply, tt.replyErr },
				tpl:     mustTpl(tplText),
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
		curator: func(context.Context, string) (string, error) {
			return `{"entries":[{"text":"Shipped the release","sources":[1]}]}`, nil
		},
		tpl: mustTpl(tplText),
	}
	gen, err := a.Generate(context.Background(), oneTaskSection(store.Task{Text: "ship release", Status: "done", Timestamp: time.Now()}))
	require.NoError(t, err)
	assert.Empty(t, gen.Fallback)
}

// A commit body is stored in full but only its subject is reported, so that
// is what the curator is asked to condense — 39 imported commits used to
// bury the JSON contract under tens of kilobytes of input.
func TestGenerateSendsOnlySubjectLinesToTheCurator(t *testing.T) {
	var gotPrompt string
	a := &impl{
		curator: func(_ context.Context, prompt string) (string, error) {
			gotPrompt = prompt
			return `{"entries":[{"text":"Added sync","sources":[1]}]}`, nil
		},
		tpl: mustTpl(tplText),
	}
	task := store.Task{
		Text:      "add sync: merge tasks with a PocketBase server\n\nMerging is deterministic and model-free: tasks union by id.",
		Status:    "done",
		Timestamp: time.Now(),
	}
	_, err := a.Generate(context.Background(), oneTaskSection(task))
	require.NoError(t, err)
	assert.Contains(t, gotPrompt, "1. add sync: merge tasks with a PocketBase server")
	assert.NotContains(t, gotPrompt, "union by id")
}

// #word is this app's own tag syntax (`list --tag`): a curator minting one
// makes reports display a tag the task does not carry.
func TestGenerateDropsInventedTags(t *testing.T) {
	a := &impl{
		curator: func(context.Context, string) (string, error) {
			return `{"entries":[{"text":"Fixed login redirect bug #1 for #auth","sources":[1]}]}`, nil
		},
		tpl: mustTpl(tplText),
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
	reportText := "## Today\n### Done\n- Updated the README (09:00)"
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
// "- - fixed the bug" and travels on into the spoken brief.
func TestGenerateStripsEchoedListMarkers(t *testing.T) {
	a := &impl{
		curator: func(context.Context, string) (string, error) {
			return `{"entries":[{"text":"- Fixed the login bug","sources":[1]}]}`, nil
		},
		tpl: mustTpl(tplText),
	}
	gen, err := a.Generate(context.Background(), oneTaskSection(store.Task{Text: "fix login", Status: "done", Timestamp: time.Now()}))
	require.NoError(t, err)
	assert.Contains(t, gen.Text, "- Fixed the login bug")
	assert.NotContains(t, gen.Text, "- - ")
}

func TestSpokenFallbackEndsEachSentenceOnce(t *testing.T) {
	script := spokenFallback("## Today\n### Done\n- Fixed the bug number two. (09:00)\n- Shipped it (10:00)")
	assert.Equal(t, "Today: Fixed the bug number two. Also, Shipped it.", script)
}

// A day's own heading anchors its finished work, but planned work is
// announced as planned: a listener heard five finished items where the report
// had four done and one todo.
func TestSpokenFallbackAnchorsEachGroup(t *testing.T) {
	script := spokenFallback("## Yesterday\n### Done\n- Fixed the bug (09:00)\n### Next\n- Write API docs (10:00)\n## Blockers\n- Waiting on devops (11:00)")
	assert.Equal(t, "Yesterday: Fixed the bug. Next up: Write API docs. Blockers: Waiting on devops.", script)
}

// A brief that joins two entries into one sentence is what a person says;
// the strict one-sentence-per-bullet rule rejected every such brief and the
// deterministic fallback — a template — was what users actually heard.
// Covering fewer sentences is fine, inventing more is not.
func TestScriptAcceptsABriefThatCombinesEntries(t *testing.T) {
	reportText := "## Today\n### Done\n- Fixed the login redirect (09:00)\n- Reviewed the billing PR (10:00)\n### Next\n- Write API docs (11:00)"
	combined := "I fixed the login redirect and reviewed the billing PR. Next I'll write API docs."
	a := &impl{speaker: func(context.Context, string) (string, error) { return combined, nil }}
	script, err := a.Script(context.Background(), reportText)
	require.NoError(t, err)
	assert.Equal(t, combined, script, "two sentences for three entries is a person talking")
}

func TestScriptStillRejectsMoreSentencesThanEntries(t *testing.T) {
	reportText := "## Today\n### Done\n- Fixed the login redirect (09:00)"
	padded := "I fixed the login redirect. It took most of the morning. The team was pleased."
	a := &impl{speaker: func(context.Context, string) (string, error) { return padded, nil }}
	script, err := a.Script(context.Background(), reportText)
	require.NoError(t, err)
	assert.Equal(t, "Today: Fixed the login redirect.", script, "extra sentences are invented detail")
}

// An operation that parses but cannot be applied is an invalid plan, not a
// store error: "batch operation 1: empty task text" tells the user nothing
// they can act on, and the specialists resolve exactly these cases.
func TestExtractOperationsRejectsUnapplicableOperations(t *testing.T) {
	now := time.Now()
	for name, plan := range map[string]string{
		"edit without text":   `{"operations":[{"kind":"edit","id":"abc","text":""}]}`,
		"edit without id":     `{"operations":[{"kind":"edit","text":"x"}]}`,
		"create without text": `{"operations":[{"kind":"create","text":"  "}]}`,
		"status without id":   `{"operations":[{"kind":"status","status":"done"}]}`,
		"invalid status":      `{"operations":[{"kind":"status","id":"abc","status":"finished"}]}`,
		"delete without id":   `{"operations":[{"kind":"delete"}]}`,
		"unknown kind":        `{"operations":[{"kind":"archive","id":"abc"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := extractOperations(plan, now)
			assert.ErrorIs(t, err, errInvalidOperationPlan)
		})
	}
}

// The direct planner's sloppy plan must reach the specialists rather than the
// store.
func TestImplPlanDelegatesWhenTheDirectPlanCannotBeApplied(t *testing.T) {
	a := &impl{
		planner: func(context.Context, string, func(string)) (string, error) {
			return `{"operations":[{"kind":"status","id":"abc","status":"blocked"}]}`, nil
		},
		plannerDirect: func(context.Context, string) (string, error) {
			return `{"operations":[{"kind":"edit","id":"abc","text":""}]}`, nil
		},
	}
	ops, err := a.Plan(context.Background(), "mark the login one as blocked", nil, time.Now())
	require.NoError(t, err)
	require.Len(t, ops, 1)
	assert.Equal(t, store.OperationStatus, ops[0].Kind)
	assert.Equal(t, "blocked", ops[0].Status)
}
