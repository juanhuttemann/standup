package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"

	defaults "standup/config"
)

type Config struct {
	MeetingTime           string
	DataFile              string
	Offline               bool
	Language              string
	Timezone              string
	SMTPHost              string
	SMTPPort              int
	SMTPUser              string
	SMTPPassword          string
	MailFrom              string
	ReposInclude          []string
	ReposExclude          []string
	EditorInstructions    string
	ReporterInstructions  string
	GenerateInputTemplate string
	DaysTemplate          string
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
	v.AutomaticEnv()
	v.SetDefault("meeting_time", "09:30")
	v.SetDefault("data_file", "~/.standup/tasks.jsonl")
	v.SetDefault("offline", false)
	v.SetDefault("language", "")
	v.SetDefault("smtp_port", 587)

	cfg := Config{
		MeetingTime:  v.GetString("meeting_time"),
		DataFile:     v.GetString("data_file"),
		Offline:      v.GetBool("offline"),
		Language:     v.GetString("language"),
		Timezone:     v.GetString("timezone"),
		SMTPHost:     v.GetString("smtp_host"),
		SMTPPort:     v.GetInt("smtp_port"),
		SMTPUser:     v.GetString("smtp_user"),
		SMTPPassword: v.GetString("smtp_password"),
		MailFrom:     v.GetString("mail_from"),
		ReposInclude: v.GetStringSlice("repos.include"),
		ReposExclude: v.GetStringSlice("repos.exclude"),
	}

	dataFile, err := expandHome(cfg.DataFile)
	if err != nil {
		return Config{}, err
	}
	cfg.DataFile = dataFile

	agentYAML, err := readFile(dirs, "agent.yaml", defaults.AgentYAML)
	if err != nil {
		return Config{}, err
	}
	a := viper.New()
	a.SetConfigType("yaml")
	if err := a.ReadConfig(strings.NewReader(agentYAML)); err != nil {
		return Config{}, fmt.Errorf("read agent.yaml: %w", err)
	}
	for _, in := range []struct {
		key string
		dst *string
	}{
		{"editor_instructions", &cfg.EditorInstructions},
		{"reporter_instructions", &cfg.ReporterInstructions},
		{"generate_input_template", &cfg.GenerateInputTemplate},
		{"generate_input_template_days", &cfg.DaysTemplate},
	} {
		s := strings.TrimRight(a.GetString(in.key), " \t\r\n")
		if s == "" {
			return Config{}, fmt.Errorf("agent.yaml: missing required key %s", in.key)
		}
		*in.dst = s
	}
	return cfg, nil
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
