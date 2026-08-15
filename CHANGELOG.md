# Changelog

All notable changes to this project are documented here. The format is
based on Keep a Changelog, and this project adheres to Semantic Versioning.

## [0.1.0] - 2026-08-15

### Added
- `add` (alias `-a`): fixes typos, rephrases, splits multi-task text via the
  editor agent; supports piped stdin. Prints saved tasks after a spinner.
- `list` (alias `-l`): arrow-key navigation with in-progress/done/delete
  actions; plain `id status time text` output when piped. Labels show short ids.
- `generate` (aliases `g`, `-g`): standup report split into Yesterday/Today at
  the configured meeting time; includes all logged statuses; spinner while
  the reporter formats.
- `done <id>` / `rm <id>`: direct store operations, git-style short-id
  prefixes accepted.
- JSONL task store (`todo` / `in-progress` / `done`) with injectable clock.
- Central configuration: app settings in `config/config.yaml`, agent prompts
  and input templates in `config/agent.yaml` (hot-editable), provider facts
  via `OPENAI_BASE_URL` / `OPENAI_MODEL` env or `.env`.
- `make verify` gate (fmt, vet, gocyclo, ineffassign, golangci-lint, deadcode,
  tests) and GitHub Actions CI mirroring it with `-race` and a binary smoke
  test.
