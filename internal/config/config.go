package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

type Config struct {
	MeetingTime           string
	DataFile              string
	BaseURL               string
	Model                 string
	EditorInstructions    string
	ReporterInstructions  string
	GenerateInputTemplate string
}

func Load(dir string) (Config, error) {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("load .env: %w", err)
	}

	var missing []string
	for _, key := range []string{"OPENAI_BASE_URL", "OPENAI_MODEL"} {
		if os.Getenv(key) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	v := viper.New()
	v.SetConfigFile(filepath.Join(dir, "config.yaml"))
	if err := v.ReadInConfig(); err != nil {
		return Config{}, fmt.Errorf("read config.yaml: %w", err)
	}
	v.SetEnvPrefix("STANDUP")
	v.AutomaticEnv()
	v.SetDefault("meeting_time", "09:30")
	v.SetDefault("data_file", "~/.standup/tasks.jsonl")

	cfg := Config{
		MeetingTime: v.GetString("meeting_time"),
		DataFile:    v.GetString("data_file"),
		BaseURL:     os.Getenv("OPENAI_BASE_URL"),
		Model:       os.Getenv("OPENAI_MODEL"),
	}

	dataFile, err := expandHome(cfg.DataFile)
	if err != nil {
		return Config{}, err
	}
	cfg.DataFile = dataFile

	a := viper.New()
	a.SetConfigFile(filepath.Join(dir, "agent.yaml"))
	if err := a.ReadInConfig(); err != nil {
		return Config{}, fmt.Errorf("read agent.yaml: %w", err)
	}
	for _, in := range []struct {
		key string
		dst *string
	}{
		{"editor_instructions", &cfg.EditorInstructions},
		{"reporter_instructions", &cfg.ReporterInstructions},
		{"generate_input_template", &cfg.GenerateInputTemplate},
	} {
		s := strings.TrimRight(a.GetString(in.key), " \t\r\n")
		if s == "" {
			return Config{}, fmt.Errorf("agent.yaml: missing required key %s", in.key)
		}
		*in.dst = s
	}
	return cfg, nil
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
