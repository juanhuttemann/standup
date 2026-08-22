package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
	"go.yaml.in/yaml/v3"

	defaults "standup/config"
)

type Config struct {
	MeetingTime               string
	DataFile                  string
	Offline                   bool
	Provider                  string
	Language                  string
	Timezone                  string
	SMTPHost                  string
	SMTPPort                  int
	SMTPUser                  string
	SMTPPassword              string
	MailFrom                  string
	ReposInclude              []string
	ReposExclude              []string
	SyncURL                   string
	SyncCollection            string
	SyncEmail                 string
	SyncPassword              string
	ObsidianVault             string
	ObsidianNote              string
	DoctorInstructions        string
	EditorInstructions        string
	CuratorInstructions       string
	SpeakerInstructions       string
	PlannerInstructions       string
	PlannerDirectInstructions string
	CreatorInstructions       string
	UpdaterInstructions       string
	DeleterInstructions       string
	GenerateInputTemplate     string
	ModelCallTimeout          time.Duration
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

// WriteDir returns the directory containing the active config.yaml. An
// explicit config dir wins; otherwise ./config wins when it has the file,
// matching Load. With no file yet, commands create the per-user config.
func WriteDir() (string, error) {
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
	dir, err := WriteDir()
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

// ValidBaseURL rejects endpoints no client could call. It is the validator
// behind login's base-URL prompt: an unparseable host otherwise surfaces
// minutes later as a failed model call that names the wrong setting.
func ValidBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("base URL must be a URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("base URL must start with http:// or https://")
	}
	if u.Host == "" {
		return errors.New("base URL must include a host")
	}
	return nil
}

// Shadowed reports the settings a selection cannot actually take effect for:
// a variable already exported in the shell, or loaded from a .env nearer the
// cwd, wins over the file SaveProvider writes, because godotenv never
// overrides an already-set variable. Without this a login that verified fine
// is silently ignored by the very next command.
func Shadowed(sel ProviderSelection) []string {
	var shadowed []string
	for _, pair := range providerPairs(sel) {
		if existing := os.Getenv(pair[0]); existing != "" && existing != pair[1] {
			shadowed = append(shadowed, pair[0])
		}
	}
	return shadowed
}

// ProviderSelection is a complete provider setup as picked interactively.
type ProviderSelection struct {
	Provider string
	BaseURL  string
	Model    string
	APIKey   string
}

// SaveProvider persists a selection to the home each setting has and exports
// the same values into the current process. It is the one writer allowed to
// persist an API key: Set refuses secrets because `config set` echoes what it
// wrote, and the rule is that a key is never echoed, not that it is never
// written — this is the very .env that refusal points at. The export is not a
// convenience:
// agent.New and agent.Check read provider settings from the environment, and
// godotenv never overrides an already-set variable, so verifying a fresh
// login against the environment the process started with would prove nothing.
// It returns the .env path, never the key.
func SaveProvider(sel ProviderSelection) (string, error) {
	required, err := ProviderEnv(sel.Provider)
	if err != nil {
		return "", err
	}
	if err := ValidBaseURL(sel.BaseURL); err != nil {
		return "", err
	}
	if sel.Model == "" {
		return "", errors.New("a model is required")
	}
	prefix := envPrefix(sel.Provider)
	if sel.APIKey == "" && slices.Contains(required, prefix+"_API_KEY") {
		return "", fmt.Errorf("%s_API_KEY is required by the %s provider", prefix, sel.Provider)
	}
	pairs := providerPairs(sel)
	// .env first: a failed write here leaves the install exactly as it was,
	// where the reverse order would leave a provider with no settings.
	path, err := setEnvMany(pairs)
	if err != nil {
		return "", err
	}
	if _, err := Set("provider", sel.Provider); err != nil {
		return "", err
	}
	for _, pair := range pairs {
		if err := os.Setenv(pair[0], pair[1]); err != nil {
			return "", err
		}
	}
	return path, nil
}

// settingTags is the writable application config surface and each key's YAML
// type.
var settingTags = map[string]string{
	"meeting_time": "!!str", "data_file": "!!str", "offline": "!!bool",
	"model_call_timeout": "!!str",
	"provider":           "!!str",
	"language":           "!!str", "timezone": "!!str", "smtp_host": "!!str",
	"smtp_port": "!!int", "smtp_user": "!!str", "mail_from": "!!str",
	"obsidian.vault": "!!str", "obsidian.note": "!!str",
}

// validSetting checks a value against its key and returns the normalized
// form. Values that break every later command are rejected here: a bad
// timezone or meeting time left the tool unusable with nothing pointing back
// at the command that set it.
func validSetting(key, tag, value string) (string, error) {
	switch tag {
	case "!!bool":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return "", fmt.Errorf("%s must be a boolean: %w", key, err)
		}
		return strconv.FormatBool(parsed), nil
	case "!!int":
		if _, err := strconv.Atoi(value); err != nil {
			return "", fmt.Errorf("%s must be an integer: %w", key, err)
		}
		return value, nil
	}
	switch key {
	case "model_call_timeout":
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return "", errors.New("model_call_timeout must be a positive duration")
		}
	case "timezone":
		if value == "" {
			return value, nil
		}
		if _, err := time.LoadLocation(value); err != nil {
			return "", fmt.Errorf("timezone must be an IANA name such as America/Asuncion: %w", err)
		}
	case "meeting_time":
		if _, err := time.Parse("15:04", value); err != nil {
			return "", fmt.Errorf("meeting_time must be a 24-hour HH:MM time (got %q)", value)
		}
	case "provider":
		if _, err := ProviderEnv(value); err != nil {
			return "", err
		}
	}
	return value, nil
}

func setYAMLValue(doc *yaml.Node, key, value string) error {
	tag, ok := settingTags[key]
	if !ok {
		return fmt.Errorf("unknown config key %q", key)
	}
	value, err := validSetting(key, tag, value)
	if err != nil {
		return err
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

// setEnv writes one .env pair. setEnvMany is the general form: a login must
// not be able to leave a base URL written with its model missing.
func envPrefix(provider string) string {
	if provider == "anthropic" {
		return "ANTHROPIC"
	}
	return "OPENAI"
}

// providerPairs is the single mapping from a selection to .env keys, so
// Shadowed can never disagree with what SaveProvider writes.
func providerPairs(sel ProviderSelection) [][2]string {
	prefix := envPrefix(sel.Provider)
	pairs := [][2]string{{prefix + "_BASE_URL", sel.BaseURL}, {prefix + "_MODEL", sel.Model}}
	if sel.APIKey != "" {
		pairs = append(pairs, [2]string{prefix + "_API_KEY", sel.APIKey})
	}
	return pairs
}

func setEnv(key, value string) (string, error) {
	return setEnvMany([][2]string{{key, value}})
}

func setEnvMany(pairs [][2]string) (string, error) {
	for _, pair := range pairs {
		if pair[1] == "" || strings.ContainsAny(pair[1], "\r\n") {
			return "", fmt.Errorf("%s must be a non-empty single-line value", pair[0])
		}
	}
	dir, err := WriteDir()
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
	lines := strings.Split(string(b), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for _, pair := range pairs {
		lines = setEnvLine(lines, pair[0], pair[1])
	}
	out := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		return "", err
	}
	// WriteFile does not re-chmod an existing file, so a .env created by hand
	// at 0644 would keep those bits — and login writes API keys into it.
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func setEnvLine(lines []string, key, value string) []string {
	line := key + "=" + value
	found := false
	for i := range lines {
		if strings.HasPrefix(lines[i], key+"=") {
			lines[i], found = line, true
		}
	}
	if !found {
		lines = append(lines, line)
	}
	return lines
}

// Init writes the embedded default config files into the active config
// directory, never overwriting existing files, and returns the directory
// path. It resolves the directory exactly like `config set` and `config
// edit`: files written anywhere else would never be read back.
func Init() (string, error) {
	dir, err := WriteDir()
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

// loadAgentConfig binds every agent.yaml key, falling back per key to the
// embedded default. A partial agent.yaml is therefore valid: adding an agent
// must not break an install whose file predates it, and the embedded yaml
// stays the single home of every prompt.
func loadAgentConfig(cfg *Config, a *viper.Viper) error {
	embedded := viper.New()
	embedded.SetConfigType("yaml")
	if err := embedded.ReadConfig(strings.NewReader(defaults.AgentYAML)); err != nil {
		return fmt.Errorf("read embedded agent.yaml: %w", err)
	}
	for _, in := range []struct {
		key string
		dst *string
	}{
		{"doctor_instructions", &cfg.DoctorInstructions},
		{"editor_instructions", &cfg.EditorInstructions},
		{"curator_instructions", &cfg.CuratorInstructions},
		{"generate_input_template", &cfg.GenerateInputTemplate},
		{"speaker_instructions", &cfg.SpeakerInstructions},
		{"planner_instructions", &cfg.PlannerInstructions},
		{"creator_instructions", &cfg.CreatorInstructions},
		{"updater_instructions", &cfg.UpdaterInstructions},
		{"deleter_instructions", &cfg.DeleterInstructions},
		{"planner_direct_instructions", &cfg.PlannerDirectInstructions},
	} {
		s := strings.TrimRight(a.GetString(in.key), " \t\r\n")
		if s == "" {
			s = strings.TrimRight(embedded.GetString(in.key), " \t\r\n")
		}
		if s == "" {
			return fmt.Errorf("agent.yaml: missing required key %s", in.key)
		}
		*in.dst = s
	}
	return nil
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
