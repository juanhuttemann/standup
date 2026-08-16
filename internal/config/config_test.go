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

speaker_instructions: |
  Speak things.

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
		for _, k := range []string{"STANDUP_MEETING_TIME", "STANDUP_DATA_FILE", "STANDUP_OFFLINE", "STANDUP_LANGUAGE", "STANDUP_SMTP_PASSWORD"} {
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
	assert.Equal(t, "Speak things.", cfg.SpeakerInstructions)
	assert.Contains(t, cfg.GenerateInputTemplate, "{{range .Yesterday}}")
}

func TestLoadSMTPSettings(t *testing.T) {
	isolateDirs(t)
	cleanStandupEnv(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "smtp_host: \"smtp.example.com\"\nsmtp_port: 465\nsmtp_user: \"me@example.com\"\nmail_from: \"standup@example.com\"\ntimezone: \"Asia/Tokyo\"\n")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("STANDUP_CONFIG_DIR", dir)
	t.Setenv("STANDUP_SMTP_PASSWORD", "s3cret")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "smtp.example.com", cfg.SMTPHost)
	assert.Equal(t, 465, cfg.SMTPPort)
	assert.Equal(t, "me@example.com", cfg.SMTPUser)
	assert.Equal(t, "s3cret", cfg.SMTPPassword, "the password comes from env, never the yaml")
	assert.Equal(t, "standup@example.com", cfg.MailFrom)
	assert.Equal(t, "Asia/Tokyo", cfg.Timezone)
}

func TestLoadReposLists(t *testing.T) {
	isolateDirs(t)
	cleanStandupEnv(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "repos:\n  include: [\"src\", \"api-*\"]\n  exclude: [\"*/vendor\"]\n")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("STANDUP_CONFIG_DIR", dir)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"src", "api-*"}, cfg.ReposInclude)
	assert.Equal(t, []string{"*/vendor"}, cfg.ReposExclude)
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
	assert.Equal(t, 587, cfg.SMTPPort, "submission port by default")
	assert.Empty(t, cfg.SMTPHost, "mail is opt-in: no host, no --mail")
	assert.Empty(t, cfg.Timezone, "empty timezone = local")
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

func TestDotEnvFoundInParentDir(t *testing.T) {
	isolateDirs(t)
	cleanStandupEnv(t)
	root := t.TempDir()
	write(t, filepath.Join(root, ".env"), "STANDUP_MEETING_TIME=18:45\n")
	write(t, filepath.Join(root, "config", "config.yaml"), "")
	write(t, filepath.Join(root, "config", "agent.yaml"), agentYAML)
	sub := filepath.Join(root, "web", "src", "deep")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	t.Chdir(sub)
	t.Setenv("STANDUP_CONFIG_DIR", filepath.Join(root, "config"))

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "18:45", cfg.MeetingTime, ".env found by walking up to the project root")
}

func TestLanguageKey(t *testing.T) {
	isolateDirs(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "language: de\n")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("STANDUP_CONFIG_DIR", dir)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "de", cfg.Language)

	t.Setenv("STANDUP_LANGUAGE", "fr")
	cfg, err = Load()
	require.NoError(t, err)
	assert.Equal(t, "fr", cfg.Language, "STANDUP_LANGUAGE overrides the yaml")

	cleanStandupEnv(t)
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

func TestMissingSpeakerKey(t *testing.T) {
	isolateDirs(t)
	base := "editor_instructions: |\n  Edit things.\nreporter_instructions: |\n  Report things.\ngenerate_input_template: |\n  {{range .Yesterday}}- {{.Text}}\ngenerate_input_template_days: |\n  {{range .Days}}## {{.Heading}}\n  {{end}}\n"
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "")
	write(t, filepath.Join(dir, "agent.yaml"), base)
	t.Setenv("STANDUP_CONFIG_DIR", dir)

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "speaker_instructions")
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

func TestSetApplicationValue(t *testing.T) {
	xdg := isolateDirs(t)

	path, err := Set("offline", "true")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(xdg, "standup", "config.yaml"), path)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(b), "offline: true")
	assert.Contains(t, string(b), "# Application settings", "setting one value preserves the useful comments")
}

func TestSetUsesExplicitConfigDir(t *testing.T) {
	isolateDirs(t)
	dir := t.TempDir()
	t.Setenv("STANDUP_CONFIG_DIR", dir)

	path, err := Set("meeting_time", "10:15")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "config.yaml"), path)
}

func TestSetRejectsInvalidOrUnknownApplicationValue(t *testing.T) {
	isolateDirs(t)
	_, err := Set("offline", "sometimes")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boolean")
	_, err = Set("mystery", "value")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown config key")
}

func TestSetProviderValueWritesDotEnv(t *testing.T) {
	xdg := isolateDirs(t)
	dir := filepath.Join(xdg, "standup")
	write(t, filepath.Join(dir, ".env"), "# local provider\nOPENAI_MODEL=old\nKEEP=me\n")

	path, err := Set("OPENAI_MODEL", "new-model")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, ".env"), path)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "# local provider\nOPENAI_MODEL=new-model\nKEEP=me\n", string(b))
}

func TestEnsureConfigCreatesEditableFile(t *testing.T) {
	xdg := isolateDirs(t)
	path, err := EnsureConfig()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(xdg, "standup", "config.yaml"), path)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, defaults.ConfigYAML, string(b))
}
