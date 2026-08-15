package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/template"

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
	editor   runFunc
	reporter runFunc
	st       *store.Store
	genTpl   *template.Template
	daysTpl  *template.Template
}

var _ Assistant = (*impl)(nil)

// local is the offline assistant: no model endpoint, deterministic behavior.
type local struct {
	st      *store.Store
	genTpl  *template.Template
	daysTpl *template.Template
}

var _ Assistant = (*local)(nil)

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
	client := openai.NewClient(option.WithBaseURL(os.Getenv("OPENAI_BASE_URL")))
	newRun := func(name, instructions string) runFunc {
		a := openaiprovider.NewChatCompletionsAgent(client, openaiprovider.AgentConfig{
			Model:        os.Getenv("OPENAI_MODEL"),
			Instructions: instructions,
			Config:       agent.Config{Name: name},
		})
		return func(ctx context.Context, prompt string) (string, error) {
			out, err := a.RunText(ctx, prompt).Collect()
			if err != nil {
				return "", err
			}
			return out.String(), nil
		}
	}
	return &impl{
		editor:   newRun("editor", cfg.EditorInstructions),
		reporter: newRun("reporter", cfg.ReporterInstructions),
		st:       st,
		genTpl:   genTpl,
		daysTpl:  daysTpl,
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

func parseTemplates(cfg config.Config) (genTpl, daysTpl *template.Template, err error) {
	genTpl, err = template.New("generate").Parse(cfg.GenerateInputTemplate)
	if err != nil {
		return nil, nil, fmt.Errorf("agent: generate template: %w", err)
	}
	daysTpl, err = template.New("generate-days").Parse(cfg.DaysTemplate)
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

func (a *impl) Generate(ctx context.Context, sec report.Section) (string, error) {
	var prompt strings.Builder
	if err := tplFor(sec, a.genTpl, a.daysTpl).Execute(&prompt, sec); err != nil {
		return "", fmt.Errorf("agent: generate template: %w", err)
	}
	return a.reporter(ctx, prompt.String())
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
// range template for any other day count.
func tplFor(sec report.Section, genTpl, daysTpl *template.Template) *template.Template {
	if len(sec.Days) == 2 {
		return genTpl
	}
	return daysTpl
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
