package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	defaults "standup/config"
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

// isolateDirs pins the whole resolution chain: no STANDUP_CONFIG_DIR, an
// empty cwd (no ./config), and an empty XDG_CONFIG_HOME (no user dir), so
// tests only see the dirs they create.
func isolateDirs(t *testing.T) string {
	t.Helper()
	unsetenv(t, "STANDUP_CONFIG_DIR")
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Chdir(t.TempDir())
	return xdg
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

// cleanStandupEnv removes STANDUP_* variables that godotenv loaded into the
// process environment during a test (t.Setenv cannot track those).
func cleanStandupEnv(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		for _, k := range []string{"STANDUP_MEETING_TIME", "STANDUP_DATA_FILE", "STANDUP_OFFLINE"} {
			if err := os.Unsetenv(k); err != nil {
				t.Errorf("unset %s: %v", k, err)
			}
		}
	})
}

func TestLoadYAMLAndPlaceholders(t *testing.T) {
	isolateDirs(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "meeting_time: \"10:15\"\ndata_file: \"~/tasks.jsonl\"\n")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("STANDUP_CONFIG_DIR", dir)
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "10:15", cfg.MeetingTime)
	assert.Equal(t, filepath.Join(home, "tasks.jsonl"), cfg.DataFile)
	assert.Equal(t, "Edit things.", cfg.EditorInstructions)
	assert.Equal(t, "Report things.", cfg.ReporterInstructions)
	assert.Contains(t, cfg.GenerateInputTemplate, "{{range .Yesterday}}")
}

func TestLoadDefaults(t *testing.T) {
	isolateDirs(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("STANDUP_CONFIG_DIR", dir)
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "09:30", cfg.MeetingTime)
	assert.Equal(t, filepath.Join(home, ".standup", "tasks.jsonl"), cfg.DataFile)
}

func TestEmbeddedFallbackWhenNoConfigAnywhere(t *testing.T) {
	isolateDirs(t)

	cfg, err := Load()
	require.NoError(t, err, "fresh install with zero config files must work")
	assert.Equal(t, "09:30", cfg.MeetingTime)
	assert.Contains(t, cfg.DataFile, ".standup")
	assert.Equal(t, defaults.ConfigYAML != "", true, "config.yaml is embedded")
	assert.Contains(t, cfg.EditorInstructions, "standup", "embedded agent.yaml supplies prompts")
	assert.Contains(t, cfg.DaysTemplate, "{{range .Days}}")
}

func TestEmbeddedFallbackPerFile(t *testing.T) {
	isolateDirs(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "meeting_time: \"11:11\"\n")
	t.Setenv("STANDUP_CONFIG_DIR", dir)

	cfg, err := Load()
	require.NoError(t, err, "missing agent.yaml falls back to embedded defaults")
	assert.Equal(t, "11:11", cfg.MeetingTime)
	assert.Contains(t, cfg.EditorInstructions, "standup")
}

func TestLocalConfigDirWinsOverUserDir(t *testing.T) {
	xdg := isolateDirs(t)
	write(t, filepath.Join(xdg, "standup", "config.yaml"), "meeting_time: \"22:00\"\n")
	cwd := t.TempDir()
	t.Chdir(cwd)
	write(t, filepath.Join(cwd, "config", "config.yaml"), "meeting_time: \"10:10\"\n")
	write(t, filepath.Join(cwd, "config", "agent.yaml"), agentYAML)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "10:10", cfg.MeetingTime, "./config wins over the user config dir")
}

func TestUserConfigDirFallback(t *testing.T) {
	xdg := isolateDirs(t)
	write(t, filepath.Join(xdg, "standup", "config.yaml"), "meeting_time: \"21:00\"\n")
	write(t, filepath.Join(xdg, "standup", "agent.yaml"), agentYAML)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "21:00", cfg.MeetingTime)
}

func TestEnvOverridesYAML(t *testing.T) {
	isolateDirs(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "meeting_time: \"09:30\"\ndata_file: \"~/.standup/tasks.jsonl\"\n")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("STANDUP_CONFIG_DIR", dir)
	t.Setenv("STANDUP_MEETING_TIME", "22:00")
	t.Setenv("STANDUP_DATA_FILE", "/tmp/direct.jsonl")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "22:00", cfg.MeetingTime)
	assert.Equal(t, "/tmp/direct.jsonl", cfg.DataFile)
}

func TestDotEnvPrecedence(t *testing.T) {
	isolateDirs(t)
	cleanStandupEnv(t)
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, ".env"), "STANDUP_MEETING_TIME=12:00\nSTANDUP_DATA_FILE=/tmp/fromdotenv.jsonl\n")
	t.Chdir(cwd)
	t.Setenv("STANDUP_MEETING_TIME", "13:00")

	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "meeting_time: \"09:30\"\ndata_file: \"~/.standup/tasks.jsonl\"\n")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("STANDUP_CONFIG_DIR", dir)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "13:00", cfg.MeetingTime)
	assert.Equal(t, "/tmp/fromdotenv.jsonl", cfg.DataFile)
}

func TestDotEnvFromConfigDir(t *testing.T) {
	isolateDirs(t)
	cleanStandupEnv(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	write(t, filepath.Join(dir, ".env"), "STANDUP_MEETING_TIME=20:30\n")
	t.Setenv("STANDUP_CONFIG_DIR", dir)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "20:30", cfg.MeetingTime, ".env next to the config files is loaded")
}

func TestCwdDotEnvWinsOverConfigDirDotEnv(t *testing.T) {
	isolateDirs(t)
	cleanStandupEnv(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	write(t, filepath.Join(dir, ".env"), "STANDUP_MEETING_TIME=20:30\n")
	t.Setenv("STANDUP_CONFIG_DIR", dir)
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, ".env"), "STANDUP_MEETING_TIME=19:00\n")
	t.Chdir(cwd)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "19:00", cfg.MeetingTime, "cwd .env wins over the config dir's")
}

func TestMissingPromptKey(t *testing.T) {
	isolateDirs(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "")
	write(t, filepath.Join(dir, "agent.yaml"), "editor_instructions: |\n  Edit things.\n")
	t.Setenv("STANDUP_CONFIG_DIR", dir)

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reporter_instructions")
}

func TestMissingTemplateKey(t *testing.T) {
	isolateDirs(t)
	base := "editor_instructions: |\n  Edit things.\nreporter_instructions: |\n  Report things.\ngenerate_input_template: |\n  {{range .Yesterday}}- {{.Text}}\n"
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "")
	write(t, filepath.Join(dir, "agent.yaml"), base)
	t.Setenv("STANDUP_CONFIG_DIR", dir)

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "generate_input_template_days")
}

func TestOfflineEnvOverridesYAML(t *testing.T) {
	isolateDirs(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "offline: false\n")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("STANDUP_CONFIG_DIR", dir)
	t.Setenv("STANDUP_OFFLINE", "true")

	cfg, err := Load()
	require.NoError(t, err)
	assert.True(t, cfg.Offline)
}

func TestInitWritesDefaults(t *testing.T) {
	xdg := isolateDirs(t)

	dir, err := Init()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(xdg, "standup"), dir)
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.Equal(t, defaults.ConfigYAML, string(b))
	b, err = os.ReadFile(filepath.Join(dir, "agent.yaml"))
	require.NoError(t, err)
	assert.Equal(t, defaults.AgentYAML, string(b))

	cfg, err := Load()
	require.NoError(t, err, "after init the written files are the resolved config")
	assert.Equal(t, "09:30", cfg.MeetingTime)
}

func TestInitKeepsExistingFiles(t *testing.T) {
	xdg := isolateDirs(t)
	dir := filepath.Join(xdg, "standup")
	write(t, filepath.Join(dir, "config.yaml"), "meeting_time: \"23:45\"\n")

	got, err := Init()
	require.NoError(t, err)
	assert.Equal(t, dir, got)
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "meeting_time: \"23:45\"\n", string(b), "init never clobbers user edits")
}
