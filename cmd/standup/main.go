package main

import (
	"fmt"
	"os"

	"standup/internal/agent"
	"standup/internal/cli"
	"standup/internal/config"
	"standup/internal/store"
)

// version is injected at release build time (-X main.version, .goreleaser.yml).
var version = "dev"

func main() {
	dir := os.Getenv("STANDUP_CONFIG_DIR")
	if dir == "" {
		dir = "config"
	}
	cfg, err := config.Load(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	st, err := store.Open(cfg.DataFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ass, err := agent.New(cfg, st)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	root := cli.New(ass, st, cfg)
	root.Version = version
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
