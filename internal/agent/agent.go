package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
}

var _ Assistant = (*impl)(nil)

func New(cfg config.Config, st *store.Store) (Assistant, error) {
	genTpl, err := template.New("generate").Parse(cfg.GenerateInputTemplate)
	if err != nil {
		return nil, fmt.Errorf("agent: generate template: %w", err)
	}
	client := openai.NewClient(option.WithBaseURL(cfg.BaseURL))
	newRun := func(name, instructions string) runFunc {
		a := openaiprovider.NewChatCompletionsAgent(client, openaiprovider.AgentConfig{
			Model:        cfg.Model,
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
	}, nil
}

func (a *impl) AddTasks(ctx context.Context, rawText string) ([]store.Task, error) {
	out, err := a.editor(ctx, rawText)
	if err != nil {
		return nil, err
	}
	texts, err := extractTasks(out)
	if err != nil {
		return nil, err
	}
	var added []store.Task
	for _, text := range texts {
		t, err := a.st.Add(text)
		if err != nil {
			return added, fmt.Errorf("agent: store task %q: %w", text, err)
		}
		added = append(added, t)
	}
	return added, nil
}

func (a *impl) Generate(ctx context.Context, sec report.Section) (string, error) {
	var prompt strings.Builder
	if err := a.genTpl.Execute(&prompt, sec); err != nil {
		return "", fmt.Errorf("agent: generate template: %w", err)
	}
	return a.reporter(ctx, prompt.String())
}

func extractTasks(s string) ([]string, error) {
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
	return nil, errors.New("agent: no tasks found in editor output")
}
