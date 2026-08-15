package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/microsoft/agent-framework-go/agent"
	"github.com/microsoft/agent-framework-go/provider/openaiprovider"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"standup/internal/config"
	"standup/internal/report"
	"standup/internal/store"
)

type Assistant interface {
	AddTasks(ctx context.Context, rawText string) ([]store.Task, error)
	Generate(ctx context.Context, sec report.Section) (string, error)
}

type runFunc func(ctx context.Context, prompt string) (string, error)

type impl struct {
	editor       runFunc
	reporter     runFunc
	instructions string // reporter prompt
	lang         string // optional output language
	st           *store.Store
	genTpl       *template.Template
	daysTpl      *template.Template
}

var _ Assistant = (*impl)(nil)

// local is the offline assistant: no model endpoint, deterministic behavior.
type local struct {
	st      *store.Store
	genTpl  *template.Template
	daysTpl *template.Template
}

var _ Assistant = (*local)(nil)

// modelTimeout bounds every model call; without it a silent endpoint rides
// the SDK default (~10 min). Var, not config: nobody asked to tune it.
var modelTimeout = 60 * time.Second

func New(cfg config.Config, st *store.Store) (Assistant, error) {
	genTpl, daysTpl, err := parseTemplates(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Offline {
		return &local{st: st, genTpl: genTpl, daysTpl: daysTpl}, nil
	}
	var missing []string
	for _, key := range []string{"OPENAI_BASE_URL", "OPENAI_MODEL"} {
		if os.Getenv(key) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment variables: %s (or set offline: true)", strings.Join(missing, ", "))
	}
	client := openai.NewClient(
		option.WithBaseURL(os.Getenv("OPENAI_BASE_URL")),
		option.WithHTTPClient(&http.Client{Timeout: modelTimeout}),
	)
	newRun := func(name, instructions string) runFunc {
		a := openaiprovider.NewChatCompletionsAgent(client, openaiprovider.AgentConfig{
			Model:        os.Getenv("OPENAI_MODEL"),
			Instructions: instructions,
			Config:       agent.Config{Name: name},
		})
		return func(ctx context.Context, prompt string) (string, error) {
			out, err := a.RunText(ctx, prompt).Collect()
			if err != nil {
				return "", fmt.Errorf("endpoint call failed — check OPENAI_BASE_URL and network: %w", err)
			}
			return out.String(), nil
		}
	}
	return &impl{
		editor:       newRun("editor", cfg.EditorInstructions),
		reporter:     newRun("reporter", cfg.ReporterInstructions),
		instructions: cfg.ReporterInstructions,
		lang:         cfg.Language,
		st:           st,
		genTpl:       genTpl,
		daysTpl:      daysTpl,
	}, nil
}

// Local returns the deterministic assistant: no model endpoint, paragraph
// splitting on add, direct template render on generate.
func Local(cfg config.Config, st *store.Store) (Assistant, error) {
	genTpl, daysTpl, err := parseTemplates(cfg)
	if err != nil {
		return nil, err
	}
	return &local{st: st, genTpl: genTpl, daysTpl: daysTpl}, nil
}

// tplFuncs are the funcs available to the generate templates. fold collapses
// a task text to one row: multi-line entries (commit bodies) must not break
// the bullet layout.
var tplFuncs = template.FuncMap{"fold": foldText}

func foldText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func parseTemplates(cfg config.Config) (genTpl, daysTpl *template.Template, err error) {
	genTpl, err = template.New("generate").Funcs(tplFuncs).Parse(cfg.GenerateInputTemplate)
	if err != nil {
		return nil, nil, fmt.Errorf("agent: generate template: %w", err)
	}
	daysTpl, err = template.New("generate-days").Funcs(tplFuncs).Parse(cfg.DaysTemplate)
	if err != nil {
		return nil, nil, fmt.Errorf("agent: days template: %w", err)
	}
	return genTpl, daysTpl, nil
}

func (a *impl) AddTasks(ctx context.Context, rawText string) ([]store.Task, error) {
	out, err := a.editor(ctx, rawText)
	if err != nil {
		return nil, err
	}
	parsed, err := extractTasks(out)
	if err != nil {
		return nil, err
	}
	return persist(a.st, parsed)
}

// Generate renders the report deterministically in Go; online mode first
// rephrases the task texts through the reporter (formatting never depends on
// the model). Any rephrase failure falls back to the original texts.
func (a *impl) Generate(ctx context.Context, sec report.Section) (string, error) {
	texts := taskTexts(sec)
	if len(texts) > 0 {
		if repl, ok := a.rephrase(ctx, texts); ok {
			applyTexts(&sec, repl)
		}
	}
	var b strings.Builder
	if err := tplFor(sec, a.genTpl, a.daysTpl).Execute(&b, sec); err != nil {
		return "", fmt.Errorf("agent: generate template: %w", err)
	}
	return b.String(), nil
}

// rephrase exchanges the task texts for rewritten ones, one per input, via
// the reporter agent's JSON contract.
func (a *impl) rephrase(ctx context.Context, texts []string) ([]string, bool) {
	var prompt strings.Builder
	prompt.WriteString(a.instructions)
	if a.lang != "" {
		fmt.Fprintf(&prompt, " Write the entries in %s.", a.lang)
	}
	prompt.WriteString("\nTasks:")
	for _, t := range texts {
		prompt.WriteString("\n- " + t)
	}
	out, err := a.reporter(ctx, prompt.String())
	if err != nil {
		return nil, false
	}
	repl, err := extractStrings(out)
	if err != nil || len(repl) != len(texts) {
		return nil, false
	}
	for _, r := range repl {
		if strings.TrimSpace(r) == "" {
			return nil, false
		}
	}
	return repl, true
}

func (l *local) AddTasks(_ context.Context, rawText string) ([]store.Task, error) {
	var parsed []extracted
	for _, p := range splitParagraphs(rawText) {
		parsed = append(parsed, extracted{text: p})
	}
	return persist(l.st, parsed)
}

func (l *local) Generate(_ context.Context, sec report.Section) (string, error) {
	var b strings.Builder
	if err := tplFor(sec, l.genTpl, l.daysTpl).Execute(&b, sec); err != nil {
		return "", fmt.Errorf("agent: generate template: %w", err)
	}
	return b.String(), nil
}

// tplFor picks the two-section template for the default window and the
// range template for any other layout (aliases are only set for the default
// window, including empty ones skipped by both templates).
func tplFor(sec report.Section, genTpl, daysTpl *template.Template) *template.Template {
	if len(sec.Days) == 2 && (sec.Yesterday != nil || sec.Today != nil) {
		return genTpl
	}
	return daysTpl
}

// taskTexts flattens the section's tasks in render order: days first, then
// blockers.
func taskTexts(sec report.Section) []string {
	var out []string
	for _, d := range sec.Days {
		for _, t := range d.Tasks {
			out = append(out, t.Text)
		}
	}
	for _, t := range sec.Blockers {
		out = append(out, t.Text)
	}
	return out
}

// applyTexts writes rephrased texts back in the same order taskTexts read
// them.
func applyTexts(sec *report.Section, repl []string) {
	i := 0
	for di := range sec.Days {
		for ti := range sec.Days[di].Tasks {
			if i < len(repl) {
				sec.Days[di].Tasks[ti].Text = repl[i]
				i++
			}
		}
	}
	for bi := range sec.Blockers {
		if i < len(repl) {
			sec.Blockers[bi].Text = repl[i]
			i++
		}
	}
	if len(sec.Days) == 2 {
		if sec.Yesterday != nil {
			sec.Yesterday = sec.Days[0].Tasks
		}
		if sec.Today != nil {
			sec.Today = sec.Days[1].Tasks
		}
	}
}

// extractStrings finds the reporter's JSON reply: {"tasks": ["...", ...]}.
func extractStrings(s string) ([]string, error) {
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		var v struct {
			Tasks []string `json:"tasks"`
		}
		if err := json.NewDecoder(strings.NewReader(s[i:])).Decode(&v); err == nil && len(v.Tasks) > 0 {
			return v.Tasks, nil
		}
	}
	return nil, errors.New("agent: no tasks found in reporter output")
}

func persist(st *store.Store, parsed []extracted) ([]store.Task, error) {
	var added []store.Task
	for _, p := range parsed {
		t, err := st.AddWithStatus(p.text, p.status)
		if err != nil {
			return added, fmt.Errorf("agent: store task %q: %w", p.text, err)
		}
		added = append(added, t)
	}
	return added, nil
}

type extracted struct {
	text   string
	status string
}

// extractTasks finds the editor's JSON reply. Entries may be plain strings
// or {"task": "...", "status": "..."} objects; missing status means todo.
func extractTasks(s string) ([]extracted, error) {
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		var v struct {
			Tasks []json.RawMessage `json:"tasks"`
		}
		if err := json.NewDecoder(strings.NewReader(s[i:])).Decode(&v); err == nil && len(v.Tasks) > 0 {
			var out []extracted
			ok := true
			for _, raw := range v.Tasks {
				var e extracted
				if len(raw) > 0 && raw[0] == '"' {
					if err := json.Unmarshal(raw, &e.text); err != nil {
						ok = false
						break
					}
				} else {
					var o struct {
						Task   string `json:"task"`
						Status string `json:"status"`
					}
					if err := json.Unmarshal(raw, &o); err != nil || strings.TrimSpace(o.Task) == "" {
						ok = false
						break
					}
					e.text, e.status = o.Task, o.Status
				}
				out = append(out, e)
			}
			if ok {
				return out, nil
			}
		}
	}
	return nil, errors.New("agent: no tasks found in editor output")
}

// splitParagraphs splits text on blank lines: one paragraph, one task.
func splitParagraphs(text string) []string {
	var out []string
	var cur []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			if len(cur) > 0 {
				out = append(out, strings.Join(cur, "\n"))
				cur = nil
			}
			continue
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, "\n"))
	}
	return out
}
