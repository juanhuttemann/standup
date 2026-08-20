package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
	"go.yaml.in/yaml/v3"

	defaults "standup/config"
)

type Config struct {
	MeetingTime                 string
	DataFile                    string
	Offline                     bool
	Provider                    string
	Language                    string
	Timezone                    string
	SMTPHost                    string
	SMTPPort                    int
	SMTPUser                    string
	SMTPPassword                string
	MailFrom                    string
	ReposInclude                []string
	ReposExclude                []string
	SyncURL                     string
	SyncCollection              string
	SyncEmail                   string
	SyncPassword                string
	ObsidianVault               string
	ObsidianNote                string
	EditorInstructions          string
	ReporterInstructions        string
	SpeakerInstructions         string
	PlannerInstructions         string
	PlannerFallbackInstructions string
	CreatorInstructions         string
	UpdaterInstructions         string
	DeleterInstructions         string
	GenerateInputTemplate       string
	DaysTemplate                string
	ModelCallTimeout            time.Duration
}

// Dirs returns the config directory chain: $STANDUP_CONFIG_DIR if set,
// else ./config then the user config dir (e.g. ~/.config/standup). The
// first directory containing a given file wins; embedded defaults are the
// final fallback so a fresh install works with zero configuration.
func Dirs() []string {
	if d := os.Getenv("STANDUP_CONFIG_DIR"); d != "" {
		return []string{d}
	}
	dirs := []string{"config"}
	if ud, err := os.UserConfigDir(); err == nil {
		dirs = append(dirs, filepath.Join(ud, "standup"))
	}
	return dirs
}

// UserDir returns the per-user config directory used by Init.
func UserDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config: user config dir: %w", err)
	}
	return filepath.Join(base, "standup"), nil
}

// writeDir returns the directory containing the active config.yaml. An
// explicit config dir wins; otherwise ./config wins when it has the file,
// matching Load. With no file yet, commands create the per-user config.
func writeDir() (string, error) {
	if dir := os.Getenv("STANDUP_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	for _, dir := range Dirs() {
		if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err == nil {
			return dir, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return UserDir()
}

// EnsureConfig creates the editable config.yaml from the embedded defaults.
func EnsureConfig() (string, error) {
	dir, err := writeDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "config.yaml")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.WriteFile(path, []byte(defaults.ConfigYAML), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// ValidateFile checks that an edited application config is valid YAML.
func ValidateFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("read config.yaml: %w", err)
	}
	return nil
}

// Set persists application settings in config.yaml and provider deployment
// facts in the config directory's .env.
func Set(key, value string) (string, error) {
	if key == "OPENAI_API_KEY" || key == "ANTHROPIC_API_KEY" {
		return "", fmt.Errorf("%s is a secret; set it in the environment or the active config directory's .env file", key)
	}
	if providerEnvKey(key) {
		return setEnv(key, value)
	}
	path, err := EnsureConfig()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return "", fmt.Errorf("read config.yaml: %w", err)
	}
	if err := setYAMLValue(&doc, key, value); err != nil {
		return "", err
	}
	b, err = yaml.Marshal(&doc)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func providerEnvKey(key string) bool {
	switch key {
	case "OPENAI_BASE_URL", "OPENAI_MODEL", "OPENAI_SPEECH_MODEL", "OPENAI_SPEECH_VOICE",
		"ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL":
		return true
	default:
		return false
	}
}

// ProviderEnv returns the environment required by the selected text provider.
// Empty preserves the original OpenAI-compatible behavior for existing users.
func ProviderEnv(provider string) ([]string, error) {
	switch provider {
	case "", "openai":
		return []string{"OPENAI_BASE_URL", "OPENAI_MODEL"}, nil
	case "anthropic":
		return []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_MODEL"}, nil
	default:
		return nil, fmt.Errorf("unsupported provider %q (use openai or anthropic)", provider)
	}
}

func setYAMLValue(doc *yaml.Node, key, value string) error {
	tags := map[string]string{
		"meeting_time": "!!str", "data_file": "!!str", "offline": "!!bool",
		"model_call_timeout": "!!str",
		"provider":           "!!str",
		"language":           "!!str", "timezone": "!!str", "smtp_host": "!!str",
		"smtp_port": "!!int", "smtp_user": "!!str", "mail_from": "!!str",
		"obsidian.vault": "!!str", "obsidian.note": "!!str",
	}
	tag, ok := tags[key]
	if !ok {
		return fmt.Errorf("unknown config key %q", key)
	}
	switch tag {
	case "!!bool":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("%s must be a boolean: %w", key, err)
		}
		value = strconv.FormatBool(parsed)
	case "!!int":
		if _, err := strconv.Atoi(value); err != nil {
			return fmt.Errorf("%s must be an integer: %w", key, err)
		}
	}
	if key == "model_call_timeout" {
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return fmt.Errorf("model_call_timeout must be a positive duration")
		}
	}
	if len(doc.Content) == 0 {
		doc.Content = append(doc.Content, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return errors.New("config.yaml must contain a mapping")
	}
	parts := strings.Split(key, ".")
	parent := root
	for _, part := range parts[:len(parts)-1] {
		next, err := mappingValue(parent, part)
		if err != nil {
			return err
		}
		parent = next
	}
	setMappingValue(parent, parts[len(parts)-1], tag, value)
	return nil
}

func mappingValue(parent *yaml.Node, key string) (*yaml.Node, error) {
	for i := 0; i < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			child := parent.Content[i+1]
			if child.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("%s must be a mapping", key)
			}
			return child, nil
		}
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
	return child, nil
}

func setMappingValue(parent *yaml.Node, key, tag, value string) {
	for i := 0; i < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			parent.Content[i+1].Value = value
			parent.Content[i+1].Tag = tag
			return
		}
	}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
}

func setEnv(key, value string) (string, error) {
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("%s must be a non-empty single-line value", key)
	}
	dir, err := writeDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, ".env")
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	line := key + "=" + value
	lines := strings.Split(string(b), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	found := false
	for i := range lines {
		if strings.HasPrefix(lines[i], key+"=") {
			lines[i], found = line, true
		}
	}
	if !found {
		lines = append(lines, line)
	}
	out := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Init writes the embedded default config files into the user config dir,
// never overwriting existing files, and returns the directory path.
func Init() (string, error) {
	dir, err := UserDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for name, content := range map[string]string{
		"config.yaml": defaults.ConfigYAML,
		"agent.yaml":  defaults.AgentYAML,
	} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// Load resolves configuration via the Dirs chain. Precedence for values:
// STANDUP_* env > .env (nearest ancestor of the cwd, then config dirs) > yaml.
func Load() (Config, error) {
	dirs := Dirs()
	if err := loadDotEnv(dirs); err != nil {
		return Config{}, err
	}

	cfgYAML, err := readFile(dirs, "config.yaml", defaults.ConfigYAML)
	if err != nil {
		return Config{}, err
	}
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(cfgYAML)); err != nil {
		return Config{}, fmt.Errorf("read config.yaml: %w", err)
	}
	v.SetEnvPrefix("STANDUP")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	v.SetDefault("meeting_time", "09:30")
	v.SetDefault("data_file", "~/.standup/tasks.jsonl")
	v.SetDefault("offline", false)
	v.SetDefault("provider", "")
	v.SetDefault("language", "")
	v.SetDefault("smtp_port", 587)
	v.SetDefault("obsidian.note", "Standups/{date}.md")
	v.SetDefault("model_call_timeout", "60s")
	v.SetDefault("sync.collection", "standup_tasks")

	cfg, err := applicationConfig(v)
	if err != nil {
		return Config{}, err
	}

	agentYAML, err := readFile(dirs, "agent.yaml", defaults.AgentYAML)
	if err != nil {
		return Config{}, err
	}
	a := viper.New()
	a.SetConfigType("yaml")
	if err := a.ReadConfig(strings.NewReader(agentYAML)); err != nil {
		return Config{}, fmt.Errorf("read agent.yaml: %w", err)
	}
	if err := loadAgentConfig(&cfg, a); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applicationConfig(v *viper.Viper) (Config, error) {
	cfg := Config{
		MeetingTime:  v.GetString("meeting_time"),
		DataFile:     v.GetString("data_file"),
		Offline:      v.GetBool("offline"),
		Provider:     v.GetString("provider"),
		Language:     v.GetString("language"),
		Timezone:     v.GetString("timezone"),
		SMTPHost:     v.GetString("smtp_host"),
		SMTPPort:     v.GetInt("smtp_port"),
		SMTPUser:     v.GetString("smtp_user"),
		SMTPPassword: v.GetString("smtp_password"),
		MailFrom:     v.GetString("mail_from"),
		ReposInclude: v.GetStringSlice("repos.include"),
		ReposExclude: v.GetStringSlice("repos.exclude"),
		// The PocketBase connection shares one prefix, so each fact has
		// exactly one name: PB_URL and PB_COLLECTION override their yaml
		// keys, PB_EMAIL and PB_PASSWORD are credentials with no yaml key
		// at all (deployment facts, like OPENAI_*). A .env works for all
		// four — godotenv has already put it in the environment.
		SyncURL:          envOr("PB_URL", v.GetString("sync.url")),
		SyncCollection:   envOr("PB_COLLECTION", v.GetString("sync.collection")),
		SyncEmail:        os.Getenv("PB_EMAIL"),
		SyncPassword:     os.Getenv("PB_PASSWORD"),
		ObsidianVault:    v.GetString("obsidian.vault"),
		ObsidianNote:     v.GetString("obsidian.note"),
		ModelCallTimeout: v.GetDuration("model_call_timeout"),
	}
	if cfg.ModelCallTimeout <= 0 {
		return Config{}, errors.New("model_call_timeout must be a positive duration")
	}

	dataFile, err := expandHome(cfg.DataFile)
	if err != nil {
		return Config{}, err
	}
	cfg.DataFile = dataFile
	if cfg.ObsidianVault != "" {
		cfg.ObsidianVault, err = expandHome(cfg.ObsidianVault)
		if err != nil {
			return Config{}, fmt.Errorf("expand ~ in obsidian vault: %w", err)
		}
	}
	return cfg, nil
}

func loadAgentConfig(cfg *Config, a *viper.Viper) error {
	plannerKeys, embeddedAgent, plannerConfigured, err := plannerPromptDefaults(a)
	if err != nil {
		return err
	}
	for _, in := range []struct {
		key string
		dst *string
	}{
		{"editor_instructions", &cfg.EditorInstructions},
		{"reporter_instructions", &cfg.ReporterInstructions},
		{"generate_input_template", &cfg.GenerateInputTemplate},
		{"generate_input_template_days", &cfg.DaysTemplate},
		{"speaker_instructions", &cfg.SpeakerInstructions},
		{"planner_instructions", &cfg.PlannerInstructions},
		{"creator_instructions", &cfg.CreatorInstructions},
		{"updater_instructions", &cfg.UpdaterInstructions},
		{"deleter_instructions", &cfg.DeleterInstructions},
		{"planner_fallback_instructions", &cfg.PlannerFallbackInstructions},
	} {
		s := strings.TrimRight(a.GetString(in.key), " \t\r\n")
		if s == "" && in.key == "planner_fallback_instructions" {
			s = strings.TrimRight(embeddedAgent.GetString(in.key), " \t\r\n")
		}
		if s == "" && plannerKeys[in.key] && !plannerConfigured {
			s = strings.TrimRight(embeddedAgent.GetString(in.key), " \t\r\n")
		}
		if s == "" {
			return fmt.Errorf("agent.yaml: missing required key %s", in.key)
		}
		*in.dst = s
	}
	return nil
}

func plannerPromptDefaults(configured *viper.Viper) (map[string]bool, *viper.Viper, bool, error) {
	keys := map[string]bool{
		"planner_instructions": true, "creator_instructions": true,
		"updater_instructions": true, "deleter_instructions": true,
	}
	present := false
	for key := range keys {
		present = present || strings.TrimSpace(configured.GetString(key)) != ""
	}
	embedded := viper.New()
	embedded.SetConfigType("yaml")
	if err := embedded.ReadConfig(strings.NewReader(defaults.AgentYAML)); err != nil {
		return nil, nil, false, fmt.Errorf("read embedded agent.yaml: %w", err)
	}
	return keys, embedded, present, nil
}

// readFile returns the first name found in the dir chain, else the embedded
// fallback.
func readFile(dirs []string, name, embedded string) (string, error) {
	for _, d := range dirs {
		b, err := os.ReadFile(filepath.Join(d, name))
		if err == nil {
			return string(b), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("read %s: %w", name, err)
		}
	}
	return embedded, nil
}

// loadDotEnv loads the nearest .env found walking up from the cwd (like git
// resolves its config), then a .env in each config dir. godotenv never
// overrides variables that are already set, so earlier files win.
func loadDotEnv(dirs []string) error {
	paths := findUp(".env")
	for _, d := range dirs {
		paths = append(paths, filepath.Join(d, ".env"))
	}
	for _, p := range paths {
		if err := godotenv.Load(p); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("load %s: %w", p, err)
		}
	}
	return nil
}

// envOr returns the environment value when set, else the fallback.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// findUp returns the path if it exists in the cwd or any parent directory.
func findUp(name string) []string {
	dir, err := os.Getwd()
	if err != nil {
		return nil
	}
	for {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return []string{p}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

func expandHome(p string) (string, error) {
	if !strings.HasPrefix(p, "~") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand ~ in data_file: %w", err)
	}
	return filepath.Join(home, p[1:]), nil
}
