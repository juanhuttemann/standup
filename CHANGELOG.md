# Changelog

All notable changes to this project are documented here. The format
is based on Keep a Changelog, and this project adheres to Semantic Versioning.

## [Unreleased]

### Added
- Terminal color palette: task statuses render in their own hue in `list`
  output (todo amber, in-progress blue, blocked red, done green; ids and
  timestamps quiet gray). Truecolor on TTY output only — piped/redirected
  output stays plain and `NO_COLOR` disables it.
- README badges now use the project palette (Ink Slate labels, green/frost
  values).

### Changed
- README badges restyled (`for-the-badge`); the retired Go Report Card badge
  was replaced with a golangci-lint badge linking to the CI job that runs it.

## [0.3.0] - 2026-08-15

### Fixed
- `cmd/standup/` was never committed: the unanchored `standup` pattern in
  `.gitignore` matched the directory, so CI checked out a tree without the
  entrypoint. Pattern is now anchored (`/standup`) and `make verify` gained an
  `ignored-go` step that fails if any `.go` file is gitignored.

### Added
- Release tooling: `v*` tag pushes run GoReleaser (`.goreleaser.yml`) in CI to
  draft a GitHub release with archives and checksums for Linux, macOS, and
  Windows (amd64/arm64); `RELEASING.md` documents the process.
- One-liner installers: `scripts/install.sh` (Linux, macOS, WSL2, Termux) and
  `scripts/install.ps1` (native Windows) fetch the latest release binary.
- `--version` flag; the version is injected at release build time.
- README badges (Go version, CI, release, Go report, license, platforms) and a
  quick-install section.

## [0.2.0] - 2026-08-15

### Added
- Carry-over: yesterday's unfinished tasks are additionally included in the
  Today section (after today's own tasks, status preserved); purely a report
  projection, the store is never mutated.
- Multi-day reports: `generate [days]` covers any number of trailing days with
  dated headings for older days (cutoff still applies to today only); the
  default two-day output is unchanged. New `generate_input_template_days` key
  in `config/agent.yaml`.
- `commits [days]`: collects the repository's own git commits (default
  lookback: last working day; filtered to the repo's configured git user;
  merge commits excluded) and runs them through the add pipeline.
- Task tags: trailing `#word` tokens are preserved by the editor agent;
  `list --tag <token>` filters case-insensitively.
- History browsing: `list --date YYYY-MM-DD` and `list --days N` (plain
  tabular output, oldest first); interactive browser stays bound to today.
- `generate -o <file>` (also `--output`): writes the report to a file (created
  or truncated); stdout stays silent so scripts can redirect cleanly.
- `edit <id> [text]`: updates task text in place (status and timestamp
  preserved); with no text it opens `$EDITOR` (fallback vi); empty text is
  rejected.
- Blockers: `blocked` is a fourth task status (validated at the store
  boundary, offered in the interactive action menu, recognized by the editor
  agent); the report gains a `## Blockers` section and blocked tasks carry
  over until resolved.
- Offline mode: `offline: true` in `config/config.yaml` (or
  `STANDUP_OFFLINE=true`) selects a local assistant — `add` splits input on
  blank lines (one paragraph, one task, verbatim) and `generate` renders the
  deterministic template directly; provider variables become optional.

### Fixed
- Quitting the interactive list with Ctrl-D no longer prints `Error: ^D`;
  it exits cleanly like Ctrl-C.

### Changed
- Empty task listings print `no tasks` (was `no tasks today`) for today,
  date, and range listings alike.
- Editor agent replies carry a per-task status; `agent.yaml` requires the new
  `generate_input_template_days` key; generate input templates render a
  Blockers section when blockers exist.

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
