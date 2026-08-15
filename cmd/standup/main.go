package main

import (
	"os"
	"sync"

	"standup/internal/agent"
	"standup/internal/cli"
	"standup/internal/config"
	"standup/internal/store"
)

// version is injected at release build time (-X main.version, .goreleaser.yml).
var version = "dev"

func main() {
	// Built lazily so help, version, and init never touch config or
	// provider settings; memoized so each process wires at most once.
	load := sync.OnceValues(func() (cli.Deps, error) {
		cfg, err := config.Load()
		if err != nil {
			return cli.Deps{}, err
		}
		st, err := store.Open(cfg.DataFile)
		if err != nil {
			return cli.Deps{}, err
		}
		ass, err := agent.New(cfg, st)
		if err != nil {
			return cli.Deps{}, err
		}
		raw, err := agent.Local(cfg, st)
		if err != nil {
			return cli.Deps{}, err
		}
		return cli.Deps{Assist: ass, Raw: raw, Store: st, Config: cfg}, nil
	})
	root := cli.New(load)
	root.Version = version
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
