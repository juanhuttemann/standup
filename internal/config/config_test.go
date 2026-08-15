package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const agentYAML = `
editor_instructions: |
  Edit things.

reporter_instructions: |
  Report things.

generate_input_template: |
  {{range .Yesterday}}- {{.Text}}
  {{range .Today}}- {{.Text}}

generate_input_template_days: |
  {{range .Days}}## {{.Heading}}
  {{end}}
`

func write(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func unsetenv(t *testing.T, keys ...string) {
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

func TestLoadYAMLAndPlaceholders(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "meeting_time: \"10:15\"\ndata_file: \"~/tasks.jsonl\"\n")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("OPENAI_BASE_URL", "http://x/v1")
	t.Setenv("OPENAI_MODEL", "some-model")
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "10:15", cfg.MeetingTime)
	assert.Equal(t, filepath.Join(home, "tasks.jsonl"), cfg.DataFile)
	assert.Equal(t, "http://x/v1", cfg.BaseURL)
	assert.Equal(t, "some-model", cfg.Model)
	assert.Equal(t, "Edit things.", cfg.EditorInstructions)
	assert.Equal(t, "Report things.", cfg.ReporterInstructions)
	assert.Contains(t, cfg.GenerateInputTemplate, "{{range .Yesterday}}")
}

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("OPENAI_BASE_URL", "http://x/v1")
	t.Setenv("OPENAI_MODEL", "some-model")
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "09:30", cfg.MeetingTime)
	assert.Equal(t, filepath.Join(home, ".standup", "tasks.jsonl"), cfg.DataFile)
}

func TestEnvOverridesYAML(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "meeting_time: \"09:30\"\ndata_file: \"~/.standup/tasks.jsonl\"\n")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("OPENAI_BASE_URL", "http://x/v1")
	t.Setenv("OPENAI_MODEL", "some-model")
	t.Setenv("STANDUP_MEETING_TIME", "22:00")
	t.Setenv("STANDUP_DATA_FILE", "/tmp/direct.jsonl")

	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "22:00", cfg.MeetingTime)
	assert.Equal(t, "/tmp/direct.jsonl", cfg.DataFile)
}

func TestDotEnvPrecedence(t *testing.T) {
	unsetenv(t, "OPENAI_BASE_URL", "OPENAI_MODEL")
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, ".env"), "OPENAI_BASE_URL=http://x/v1\nOPENAI_MODEL=some-model\nSTANDUP_MEETING_TIME=12:00\nSTANDUP_DATA_FILE=/tmp/fromdotenv.jsonl\n")
	t.Chdir(cwd)
	t.Setenv("STANDUP_MEETING_TIME", "13:00")

	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "meeting_time: \"09:30\"\ndata_file: \"~/.standup/tasks.jsonl\"\n")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)

	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "13:00", cfg.MeetingTime)
	assert.Equal(t, "/tmp/fromdotenv.jsonl", cfg.DataFile)
	assert.Equal(t, "http://x/v1", cfg.BaseURL)
	assert.Equal(t, "some-model", cfg.Model)
}

func TestMissingBaseURL(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	unsetenv(t, "OPENAI_BASE_URL")
	t.Setenv("OPENAI_MODEL", "some-model")

	_, err := Load(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OPENAI_BASE_URL")
}

func TestMissingModel(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("OPENAI_BASE_URL", "http://x/v1")
	unsetenv(t, "OPENAI_MODEL")

	_, err := Load(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OPENAI_MODEL")
}

func TestMissingConfigYAML(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("OPENAI_BASE_URL", "http://x/v1")
	t.Setenv("OPENAI_MODEL", "some-model")

	_, err := Load(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config.yaml")
}

func TestMissingAgentYAML(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "")
	t.Setenv("OPENAI_BASE_URL", "http://x/v1")
	t.Setenv("OPENAI_MODEL", "some-model")

	_, err := Load(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent.yaml")
}

func TestMissingPromptKey(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "")
	write(t, filepath.Join(dir, "agent.yaml"), "editor_instructions: |\n  Edit things.\n")
	t.Setenv("OPENAI_BASE_URL", "http://x/v1")
	t.Setenv("OPENAI_MODEL", "some-model")

	_, err := Load(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reporter_instructions")
}

func TestMissingTemplateKey(t *testing.T) {
	base := "editor_instructions: |\n  Edit things.\nreporter_instructions: |\n  Report things.\ngenerate_input_template: |\n  {{range .Yesterday}}- {{.Text}}\n"
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "")
	write(t, filepath.Join(dir, "agent.yaml"), base)
	t.Setenv("OPENAI_BASE_URL", "http://x/v1")
	t.Setenv("OPENAI_MODEL", "some-model")

	_, err := Load(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "generate_input_template_days")
}

func TestOfflineSkipsProviderEnv(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "offline: true\n")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	unsetenv(t, "OPENAI_BASE_URL", "OPENAI_MODEL")

	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.True(t, cfg.Offline)
	assert.Empty(t, cfg.BaseURL)
	assert.Empty(t, cfg.Model)
	assert.Contains(t, cfg.DaysTemplate, "{{range .Days}}")
}

func TestOfflineEnvOverridesYAML(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "offline: false\n")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	unsetenv(t, "OPENAI_BASE_URL", "OPENAI_MODEL")
	t.Setenv("STANDUP_OFFLINE", "true")

	cfg, err := Load(dir)
	require.NoError(t, err)
	assert.True(t, cfg.Offline)
}
