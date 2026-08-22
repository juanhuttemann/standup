package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	defaults "standup/config"
)

const agentYAML = `
editor_instructions: |
  Edit things.

curator_instructions: |
  Curate things.

speaker_instructions: |
  Speak things.

planner_instructions: |
  Plan things.

planner_direct_instructions: |
  Plan things directly.

creator_instructions: |
  Create things.

updater_instructions: |
  Update things.

deleter_instructions: |
  Delete things.

generate_input_template: |
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
		for _, k := range []string{"STANDUP_MEETING_TIME", "STANDUP_DATA_FILE", "STANDUP_OFFLINE", "STANDUP_LANGUAGE", "STANDUP_SMTP_PASSWORD", "PB_URL", "PB_COLLECTION", "PB_EMAIL", "PB_PASSWORD"} {
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
	assert.Equal(t, "Curate things.", cfg.CuratorInstructions)
	assert.Equal(t, "Speak things.", cfg.SpeakerInstructions)
	assert.Contains(t, cfg.GenerateInputTemplate, "{{range .Days}}")
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

func TestLoadObsidianSettings(t *testing.T) {
	isolateDirs(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "obsidian:\n  vault: \"~/Notes\"\n  note: \"Daily/{date}.md\"\n")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("STANDUP_CONFIG_DIR", dir)
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "Notes"), cfg.ObsidianVault)
	assert.Equal(t, "Daily/{date}.md", cfg.ObsidianNote)

	t.Setenv("STANDUP_OBSIDIAN_VAULT", "/env/vault")
	cfg, err = Load()
	require.NoError(t, err)
	assert.Equal(t, "/env/vault", cfg.ObsidianVault)
}

func TestLoadSyncSettings(t *testing.T) {
	isolateDirs(t)
	cleanStandupEnv(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "sync:\n  url: \"https://pb.example.com\"\n  collection: \"my_tasks\"\n")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("STANDUP_CONFIG_DIR", dir)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "https://pb.example.com", cfg.SyncURL)
	assert.Equal(t, "my_tasks", cfg.SyncCollection)
}

// Sync credentials are deployment facts like OPENAI_*: PB_EMAIL/PB_PASSWORD
// in the environment or a .env, never a config key. Deliberately outside the
// STANDUP_ namespace so they cannot be mistaken for `sync:` yaml settings.
func TestLoadSyncCredentials(t *testing.T) {
	isolateDirs(t)
	cleanStandupEnv(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "sync:\n  url: \"https://pb.example.com\"\n")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("STANDUP_CONFIG_DIR", dir)
	t.Setenv("PB_EMAIL", "admin@example.com")
	t.Setenv("PB_PASSWORD", "s3cret")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "admin@example.com", cfg.SyncEmail)
	assert.Equal(t, "s3cret", cfg.SyncPassword)
}

func TestLoadSyncCredentialsNeverFromYAML(t *testing.T) {
	isolateDirs(t)
	cleanStandupEnv(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "sync:\n  url: \"https://pb.example.com\"\n  email: \"file@example.com\"\n  password: \"from-file\"\n")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("STANDUP_CONFIG_DIR", dir)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Empty(t, cfg.SyncEmail, "credentials are never read from a config file")
	assert.Empty(t, cfg.SyncPassword)
}

func TestLoadSyncDefaults(t *testing.T) {
	isolateDirs(t)
	cleanStandupEnv(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("STANDUP_CONFIG_DIR", dir)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Empty(t, cfg.SyncURL, "sync is disabled out of the box")
	assert.Equal(t, "standup_tasks", cfg.SyncCollection)
}

// The whole PocketBase connection shares one prefix — PB_URL, PB_COLLECTION,
// PB_EMAIL, PB_PASSWORD — so there is one name per fact, not two.
func TestLoadSyncEnvOverridesYAML(t *testing.T) {
	isolateDirs(t)
	cleanStandupEnv(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "sync:\n  url: \"https://yaml.example.com\"\n  collection: \"yaml_tasks\"\n")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("STANDUP_CONFIG_DIR", dir)
	t.Setenv("PB_URL", "https://env.example.com")
	t.Setenv("PB_COLLECTION", "env_tasks")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "https://env.example.com", cfg.SyncURL, "PB_URL beats the yaml")
	assert.Equal(t, "env_tasks", cfg.SyncCollection, "PB_COLLECTION beats the yaml")
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
	assert.Empty(t, cfg.ObsidianVault, "Obsidian export is opt-in")
	assert.Equal(t, "Standups/{date}.md", cfg.ObsidianNote)
}

func TestLoadLegacyAgentConfigUsesEmbeddedPlannerPrompts(t *testing.T) {
	dir := isolateDirs(t)
	t.Setenv("STANDUP_CONFIG_DIR", dir)
	legacy := `
editor_instructions: Edit things.
speaker_instructions: Speak things.
generate_input_template: '{{range .Days}}{{.Heading}}{{end}}'
`
	write(t, filepath.Join(dir, "agent.yaml"), legacy)

	cfg, err := Load()
	require.NoError(t, err)
	assert.NotEmpty(t, cfg.PlannerInstructions)
	assert.NotEmpty(t, cfg.CreatorInstructions)
	assert.NotEmpty(t, cfg.UpdaterInstructions)
	assert.NotEmpty(t, cfg.DeleterInstructions)
	assert.Equal(t, "Edit things.", cfg.EditorInstructions, "legacy custom prompts remain active")
}

// Every absent agent.yaml key falls back to its embedded default, so adding
// an agent never breaks an install whose file predates it.
func TestLoadFillsAbsentPromptsFromEmbeddedDefaults(t *testing.T) {
	dir := isolateDirs(t)
	t.Setenv("STANDUP_CONFIG_DIR", dir)
	partial := `
editor_instructions: Edit things.
planner_instructions: Plan things.
`
	write(t, filepath.Join(dir, "agent.yaml"), partial)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "Edit things.", cfg.EditorInstructions, "a configured prompt stays configured")
	assert.Equal(t, "Plan things.", cfg.PlannerInstructions)
	for name, got := range map[string]string{
		"curator":        cfg.CuratorInstructions,
		"speaker":        cfg.SpeakerInstructions,
		"creator":        cfg.CreatorInstructions,
		"updater":        cfg.UpdaterInstructions,
		"deleter":        cfg.DeleterInstructions,
		"planner_direct": cfg.PlannerDirectInstructions,
		"doctor":         cfg.DoctorInstructions,
		"template":       cfg.GenerateInputTemplate,
	} {
		assert.NotEmpty(t, got, "%s falls back to the embedded default", name)
	}
}

func TestEmbeddedFallbackWhenNoConfigAnywhere(t *testing.T) {
	isolateDirs(t)

	cfg, err := Load()
	require.NoError(t, err, "fresh install with zero config files must work")
	assert.Equal(t, "09:30", cfg.MeetingTime)
	assert.Contains(t, cfg.DataFile, ".standup")
	assert.Equal(t, defaults.ConfigYAML != "", true, "config.yaml is embedded")
	assert.Contains(t, cfg.EditorInstructions, "standup", "embedded agent.yaml supplies prompts")
	assert.Contains(t, cfg.GenerateInputTemplate, "{{range .Days}}")
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

func TestSetNestedObsidianValue(t *testing.T) {
	xdg := isolateDirs(t)

	path, err := Set("obsidian.vault", "/notes")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(xdg, "standup", "config.yaml"), path)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(b), "obsidian:\n    vault: \"/notes\"")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "/notes", cfg.ObsidianVault)
}

func TestSetNestedValueRejectsNonMappingParent(t *testing.T) {
	for name, value := range map[string]string{
		"scalar":   "legacy",
		"sequence": "[legacy]",
	} {
		t.Run(name, func(t *testing.T) {
			isolateDirs(t)
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			original := "obsidian: " + value + "\n"
			write(t, path, original)
			t.Setenv("STANDUP_CONFIG_DIR", dir)

			_, err := Set("obsidian.vault", "/notes")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "obsidian must be a mapping")
			b, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			assert.Equal(t, original, string(b))
		})
	}
}

func TestSetUsesExplicitConfigDir(t *testing.T) {
	isolateDirs(t)
	dir := t.TempDir()
	t.Setenv("STANDUP_CONFIG_DIR", dir)

	path, err := Set("meeting_time", "10:15")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "config.yaml"), path)
}

func TestSetUpdatesActiveWorkingDirectoryConfig(t *testing.T) {
	xdg := isolateDirs(t)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	write(t, filepath.Join(cwd, "config", "config.yaml"), "offline: false\n")

	path, err := Set("offline", "true")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("config", "config.yaml"), path)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(b), "offline: true")
	cfg, err := Load()
	require.NoError(t, err)
	assert.True(t, cfg.Offline, "the value written by config set must be the value Load resolves")
	_, err = os.Stat(filepath.Join(xdg, "standup", "config.yaml"))
	assert.ErrorIs(t, err, os.ErrNotExist, "a lower-precedence user file must not be written")
}

func TestSetRejectsInvalidOrUnknownApplicationValue(t *testing.T) {
	isolateDirs(t)
	_, err := Set("offline", "sometimes")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boolean")
	_, err = Set("mystery", "value")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown config key")
	_, err = Set("model_call_timeout", "forever")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "positive duration")
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

func TestSetAnthropicProviderValueWritesDotEnv(t *testing.T) {
	xdg := isolateDirs(t)
	dir := filepath.Join(xdg, "standup")

	path, err := Set("ANTHROPIC_MODEL", "test-model")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, ".env"), path)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "ANTHROPIC_MODEL=test-model\n", string(b))

	_, err = Set("ANTHROPIC_API_KEY", "secret")
	require.Error(t, err, "config set echoes values, so it must never accept secrets")
}

func TestProviderEnv(t *testing.T) {
	assert.Equal(t, []string{"OPENAI_BASE_URL", "OPENAI_MODEL"}, mustProviderEnv(t, ""))
	assert.Equal(t, []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_MODEL"}, mustProviderEnv(t, "anthropic"))
	_, err := ProviderEnv("mystery")
	require.Error(t, err)
}

func TestSetRejectsOpenAISecretWithGuidance(t *testing.T) {
	isolateDirs(t)
	_, err := Set("OPENAI_API_KEY", "secret")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "active config directory's .env")
}

func TestLoadModelCallTimeout(t *testing.T) {
	isolateDirs(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config.yaml"), "model_call_timeout: 7s\n")
	write(t, filepath.Join(dir, "agent.yaml"), agentYAML)
	t.Setenv("STANDUP_CONFIG_DIR", dir)
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 7*time.Second, cfg.ModelCallTimeout)
}

func TestLoadProviderFromEnvironment(t *testing.T) {
	isolateDirs(t)
	t.Setenv("STANDUP_PROVIDER", "anthropic")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "anthropic", cfg.Provider)
}

func mustProviderEnv(t *testing.T, provider string) []string {
	t.Helper()
	got, err := ProviderEnv(provider)
	require.NoError(t, err)
	return got
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

// init wrote to the home directory even with $STANDUP_CONFIG_DIR set, so the
// files it created were in a directory nothing would ever read and the
// follow-up `config edit` opened a different file.
func TestInitHonorsConfigDirEnv(t *testing.T) {
	xdg := isolateDirs(t)
	sandbox := t.TempDir()
	t.Setenv("STANDUP_CONFIG_DIR", sandbox)

	dir, err := Init()
	require.NoError(t, err)
	assert.Equal(t, sandbox, dir)
	for _, name := range []string{"config.yaml", "agent.yaml"} {
		assert.FileExists(t, filepath.Join(sandbox, name))
		assert.NoFileExists(t, filepath.Join(xdg, "standup", name))
	}
	path, err := EnsureConfig()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(sandbox, "config.yaml"), path,
		"config edit opens the file init just wrote")
}

func TestSetRejectsValuesThatBreakEveryCommand(t *testing.T) {
	isolateDirs(t)
	for _, tt := range []struct{ key, value, want string }{
		{"timezone", "Mars/Phobos", "IANA"},
		{"meeting_time", "99:99", "HH:MM"},
		{"meeting_time", "half past nine", "HH:MM"},
	} {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			_, err := Set(tt.key, tt.value)
			require.Error(t, err, "a value that breaks list and generate is refused at set time")
			assert.Contains(t, err.Error(), tt.want)
		})
	}
	for _, tt := range []struct{ key, value string }{
		{"timezone", "America/Asuncion"},
		{"timezone", ""},
		{"meeting_time", "09:30"},
	} {
		_, err := Set(tt.key, tt.value)
		assert.NoError(t, err, "valid values still apply")
	}
}

func TestSetStillRefusesSecretsAfterSetSecretExists(t *testing.T) {
	isolateDirs(t)
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		_, err := Set(key, "secret")
		require.Error(t, err, "config set echoes values, so it must never accept secrets")
	}
}

func TestValidBaseURL(t *testing.T) {
	for _, ok := range []string{"http://localhost:11434/v1", "https://api.example.test/v1", "https://api.example.test", "https://api.example.test/anthropic"} {
		assert.NoError(t, ValidBaseURL(ok), ok)
	}
	for _, bad := range []string{"", "localhost:11434", "ftp://example.test", "https://", "not a url"} {
		assert.Error(t, ValidBaseURL(bad), bad)
	}
}

// Pasting the full models or completions URL is the obvious mistake, and it
// costs a 404 on the model list and a base URL that only fails on the next
// command.
func TestNormalizeBaseURLTrimsAPastedRequestPath(t *testing.T) {
	for _, raw := range []string{
		"https://api.example.test/v1/models",
		"https://api.example.test/v1/chat/completions",
		"https://api.example.test/v1/responses/",
		"https://api.example.test/v1/models/",
	} {
		got, trimmed := NormalizeBaseURL(raw)
		assert.True(t, trimmed, raw)
		assert.Equal(t, "https://api.example.test/v1", got, raw)
	}
	for _, raw := range []string{"https://api.example.test/v1", "https://api.example.test", "https://api.example.test/anthropic"} {
		got, trimmed := NormalizeBaseURL(raw)
		assert.False(t, trimmed, raw)
		assert.Equal(t, raw, got, raw)
	}
}

func TestSaveProviderWritesBothHomesAndExports(t *testing.T) {
	xdg := isolateDirs(t)
	dir := filepath.Join(xdg, "standup")
	for _, key := range []string{"OPENAI_BASE_URL", "OPENAI_MODEL", "OPENAI_API_KEY"} {
		t.Setenv(key, "")
	}

	path, err := SaveProvider(ProviderSelection{
		Provider: "openai",
		BaseURL:  "https://api.example.test/v1",
		Model:    "test-model",
		APIKey:   "sk-test",
	})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, ".env"), path)

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "OPENAI_BASE_URL=https://api.example.test/v1\nOPENAI_MODEL=test-model\nOPENAI_API_KEY=sk-test\n", string(b))

	yml, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(yml), `provider: "openai"`)

	// agent.Check reads settings from the environment, never from Config.
	assert.Equal(t, "https://api.example.test/v1", os.Getenv("OPENAI_BASE_URL"))
	assert.Equal(t, "test-model", os.Getenv("OPENAI_MODEL"))
	assert.Equal(t, "sk-test", os.Getenv("OPENAI_API_KEY"))
}

func TestSaveProviderOmitsAnEmptyOpenAIKey(t *testing.T) {
	xdg := isolateDirs(t)
	for _, key := range []string{"OPENAI_BASE_URL", "OPENAI_MODEL"} {
		t.Setenv(key, "")
	}

	path, err := SaveProvider(ProviderSelection{Provider: "openai", BaseURL: "http://localhost:11434/v1", Model: "local-model"})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(xdg, "standup", ".env"), path)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "OPENAI_API_KEY", "local endpoints need no key")
	assert.Contains(t, string(b), "OPENAI_MODEL=local-model")
}

func TestSaveProviderWritesAnthropicKeys(t *testing.T) {
	xdg := isolateDirs(t)
	for _, key := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "ANTHROPIC_API_KEY"} {
		t.Setenv(key, "")
	}

	path, err := SaveProvider(ProviderSelection{
		Provider: "anthropic",
		BaseURL:  "https://api.example.test",
		Model:    "test-model",
		APIKey:   "sk-ant",
	})
	require.NoError(t, err)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(b), "ANTHROPIC_API_KEY=sk-ant")
	yml, err := os.ReadFile(filepath.Join(xdg, "standup", "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(yml), `provider: "anthropic"`)
}

func TestSaveProviderRejectsIncompleteSelections(t *testing.T) {
	cases := map[string]ProviderSelection{
		"unknown provider": {Provider: "gemini", BaseURL: "https://api.example.test", Model: "m"},
		"no base URL":      {Provider: "openai", Model: "m"},
		"no model":         {Provider: "openai", BaseURL: "https://api.example.test"},
		"anthropic no key": {Provider: "anthropic", BaseURL: "https://api.example.test", Model: "m"},
	}
	for name, sel := range cases {
		t.Run(name, func(t *testing.T) {
			xdg := isolateDirs(t)
			_, err := SaveProvider(sel)
			require.Error(t, err)
			_, statErr := os.Stat(filepath.Join(xdg, "standup", ".env"))
			assert.True(t, os.IsNotExist(statErr), "a rejected selection writes nothing")
		})
	}
}

func TestSetRejectsAnUnsupportedProvider(t *testing.T) {
	isolateDirs(t)
	_, err := Set("provider", "gemini")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openai or anthropic")
	_, err = Set("provider", "anthropic")
	require.NoError(t, err)
}

func TestSaveProviderTightensAnExistingEnvFile(t *testing.T) {
	xdg := isolateDirs(t)
	dir := filepath.Join(xdg, "standup")
	write(t, filepath.Join(dir, ".env"), "# hand written\nKEEP=me\n")
	require.NoError(t, os.Chmod(filepath.Join(dir, ".env"), 0o644))
	for _, key := range []string{"OPENAI_BASE_URL", "OPENAI_MODEL", "OPENAI_API_KEY"} {
		t.Setenv(key, "")
	}

	path, err := SaveProvider(ProviderSelection{Provider: "openai", BaseURL: "https://api.example.test/v1", Model: "m", APIKey: "sk-test"})
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "a hand-written .env must not stay world-readable once it holds a key")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(b), "# hand written\nKEEP=me\n", "unrelated content survives")
}

func TestShadowedForNamesEnvironmentValuesThatWinOverTheFile(t *testing.T) {
	isolateDirs(t)
	for _, key := range []string{"OPENAI_BASE_URL", "OPENAI_MODEL", "OPENAI_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "ANTHROPIC_API_KEY"} {
		t.Setenv(key, "")
	}
	assert.Empty(t, ShadowedFor("openai"), "an unset environment shadows nothing")

	t.Setenv("OPENAI_BASE_URL", "http://elsewhere.example.test/v1")
	assert.Equal(t, []string{"OPENAI_BASE_URL"}, ShadowedFor("openai"))
	assert.Empty(t, ShadowedFor("anthropic"), "the other provider's variables are irrelevant")

	t.Setenv("OPENAI_MODEL", "other-model")
	t.Setenv("OPENAI_API_KEY", "other-key")
	assert.Equal(t, []string{"OPENAI_BASE_URL", "OPENAI_MODEL", "OPENAI_API_KEY"}, ShadowedFor("openai"))

	t.Setenv("ANTHROPIC_API_KEY", "sk")
	assert.Equal(t, []string{"ANTHROPIC_API_KEY"}, ShadowedFor("anthropic"))
}
