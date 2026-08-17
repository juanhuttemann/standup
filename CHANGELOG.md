# Changelog

All notable changes to this project are documented here. The format
is based on Keep a Changelog, and this project adheres to Semantic Versioning.

## [Unreleased]

## [0.11.0] - 2026-08-17

### Added

- `standup -p "prompt"` delegates natural-language create, edit, status, and
  delete requests to specialized agents, validates their typed plan, applies
  all changes atomically in Go, and prints the committed changes. `-p -` reads
  the prompt from stdin; `--verbose` shows coordinator-to-specialist tool calls;
  ambiguous or invalid plans make no changes.

### Fixed

- `standup -p` now distinguishes an empty plan from malformed model output:
  missing or ambiguous targets produce an actionable, bounded explanation,
  while contract failures suggest simplifying the prompt.
- Online commands now preflight the configured model endpoint for two seconds,
  reporting stale or unreachable endpoints before the longer model timeout.
- Store rewrites now use a synced temporary file and atomic replacement,
  preserve existing permissions, serialize in-process mutations, and reject
  duplicate task ids instead of selecting an arbitrary record.
- Endpoint and verbose-tool errors no longer expose credential-bearing URLs or
  model-supplied terminal control text; tool calls without ids remain visible.
- Root action flags now reject conflicts and stray prompt arguments, and
  `--verbose` is accepted only with `--prompt`.
- Existing agent configs from earlier releases inherit the embedded planner
  prompt group; partially configured planner groups still fail closed.

## [0.10.0] - 2026-08-16

### Added

- `standup generate --obsidian` publishes a report into the configured
  Obsidian vault. `obsidian.note` defaults to `Standups/{date}.md`, with the
  date resolved in the configured timezone; existing notes keep all content
  outside standup's managed markers, and the JSONL store remains authoritative.

## [0.9.0] - 2026-08-16

### Changed

- `standup update` now securely downloads, verifies, and installs the latest
  release in place instead of printing an installer command; `--check` keeps
  the previous check-only behavior.
- Installers now require a matching SHA-256 checksum and fail closed when the
  checksum file or entry is missing.

## [0.8.1] - 2026-08-16

### Fixed

- `config set` and `config edit` now target the active `./config` when present,
  so a higher-precedence project config cannot silently shadow changes written
  to the user config.
- Windows interactive `list` no longer panics in promptui's ANSI parser;
  unsupported truecolor output is disabled on Windows terminals.

## [0.8.0] - 2026-08-16

### Added

- `standup config set KEY VALUE` updates application settings in the user
  `config.yaml`; `OPENAI_BASE_URL` and `OPENAI_MODEL` stay provider facts and
  are written to the local config `.env` instead.
- `standup config edit` opens the user `config.yaml` in `$EDITOR` (falling
  back to `vi`, or Notepad on Windows) and restores the original if the edit
  is invalid YAML.

### Changed

- Missing-provider errors now give the terminal-ready fix:
  `standup config set offline true`.

## [0.7.0] - 2026-08-15

### Added

- `standup speak [days]`: rewrites the standup report as a spoken brief via
  a new `speaker_instructions` agent (`config/agent.yaml`) and prints the
  script — a free preview that never touches the speech endpoint.
  `speak -o standup.wav` additionally synthesizes the script into an audio
  file (streaming chat completion with the audio modality — the audio-output
  shape OpenAI-compatible endpoints implement; raw pcm16 is wrapped in a WAV
  container deterministically). The speech model and voice are deployment
  facts: `OPENAI_SPEECH_MODEL`/`OPENAI_SPEECH_VOICE` env, checked only when
  `-o` is given. Same window flags as `generate` (`--from`/`--to`, `--team`);
  scripts over 4096 chars fail closed; offline mode and empty reports skip
  the model entirely.

### Fixed

- `install.sh` aborted with a spurious "checksum mismatch" on every install:
  the archive was saved as `standup.tar.gz` while `sha256sum -c` resolves
  the release filename — it is now saved under its release name.

## [0.6.0] - 2026-08-15

### Added

- `standup update`: compares the running version with the latest release
  (via the GitHub `releases/latest` redirect — no API, no rate limit) and
  prints the installer command to rerun; up to date says so.
- `generate --webhook <url>`: POSTs the report as Slack-compatible JSON
  (`{"text": ...}` — a sensible generic payload for any JSON webhook), so
  it goes straight where the team reads it.
- `generate --mail <address>`: sends the report as a plain-text email via
  SMTP (`smtp_host`/`smtp_port`/`smtp_user`/`mail_from` in `config.yaml`;
  the password is `STANDUP_SMTP_PASSWORD` env, never a file).
- `commits` collects from git submodules too: each repo's `.gitmodules`
  paths feed the multi-repo collector (deduped by hash like any repo).
- `repos.include`/`repos.exclude` glob lists in `config.yaml` filter which
  repos and submodules `commits` ingests (path.Match against each path as
  passed; empty include = everything not excluded).
- `timezone` config key (IANA name, empty = local): report windows, the
  meeting cutoff and the day split follow the configured zone instead of
  the machine's — for teams whose standup time is not the runner's zone.
- `commits --branch` records the branch name with each imported commit
  (best-effort `git name-rev` semantics — ancestors show as `name~N`);
  `list` and report rows render it as `[branch]` when present.
- Team standup: `commits --all-authors` imports every author's commits and
  records the author email on each task; `generate --team` renders one
  section per author (`## alice@example.com`, unattributed tasks first).
  The store stays a single personal file — grouping happens at report time.
- Installers verify the downloaded archive's SHA-256 when the release
  publishes a checksums file (releases now ship a stable-named
  `standup_checksums.txt`; older releases skip verification with a note).
- Demo GIF in the README (recorded with vhs; `demo.tape` is reproducible
  against a live endpoint, seeded with a previous working day).
- README ships a ready-made GitHub Actions cron workflow: checkout, install,
  `commits` + `generate --webhook` in offline mode — a daily standup posted
  to the team's webhook with zero LLM credentials.

### Changed

- Documented `generate`'s contract: an unreachable model falls back to
  verbatim texts, but missing provider env fails fast with a hint — offline
  rendering stays an explicit `offline: true` opt-in.
- `add` now echoes the saved rows with their status (`- [todo] ...`) and
  `generate` colors the report's `[status]` tokens on a terminal — same
  palette as `list`; plain when piped or `NO_COLOR` is set.

### Fixed

- `commits` no longer hard-fails on conventional-commit subjects (`chore: seed
  repo` & co. were eaten as a "trailer block", leaving empty task text): a
  message that would strip to nothing is kept verbatim, and one bad commit no
  longer aborts the import — it is skipped with a warning and the rest are
  still imported.
- `doctor` no longer fails on a fresh install: the data-file check creates
  the parent directory (like the first `add` does) instead of failing on a
  missing `~/.standup`.
- Piped CRLF input (Windows `Get-Content | standup add`) is normalized at
  ingest — a literal `\r` no longer lands in the task store, reports, and
  model prompts.
- Multi-line task texts (imported commit bodies) render as one report bullet:
  both generate templates fold newlines via the new `fold` template func
  (user templates may call it too), so body lines keep their `- ` prefix and
  the timestamp stays on the row.
- `rm` folds the echoed task text like every other command — multi-line
  entries print as one row.
- Model calls are bounded by a 60 s HTTP timeout: a silent endpoint fails
  with the usual endpoint hint instead of riding the SDK default (~10 min).
- Windows installer no longer duplicates PATH entries: the persisted User
  PATH is checked before appending, not just the current session's.
- README documents uninstall (delete the binary / `%LOCALAPPDATA%\standup`).

## [0.5.0] - 2026-08-15

### Fixed

- `commits` no longer destroys day attribution: imported tasks are stamped
  with the commit's author date (not the import time), so `list --date` and
  the Yesterday section find them again. Importing is fully deterministic
  now (no model in the pipeline); commits land as `done` tasks.
- Re-running `commits` no longer duplicates tasks: already-imported commits
  (same text, same day) are skipped and reported as `skipped N`.
- Read-only commands (`list`, `done`, `rm`, `status`, `edit`, `commits`,
  `doctor`) no longer require `OPENAI_BASE_URL`/`OPENAI_MODEL` — the
  assistant is built on first use by `add`/`generate` only, restoring the
  zero-config claim for everything else.
- `list --tag` now matches the literal `#token` (case-insensitive,
  punctuation tolerated) instead of a substring search: `--tag fix` no
  longer matches "Fixed login bug #auth" or the word "API".
- `edit` without `$EDITOR` falls back to `notepad` on Windows (was `vi`,
  which does not exist there).
- `commits` outside a repository now says so (`not inside a git working
  tree`) instead of misdiagnosing a missing `user.email`; the repo check
  runs first. A zero-match run hints at a `git config user.email` /
  commit-identity mismatch instead of a bare "no commits found".
- `.env` lookup walks up parent directories from the cwd (like git), so
  running from a project subdir keeps the endpoint configuration.
- Blocked tasks are no longer duplicated: they appear under `## Blockers`
  only, not additionally in the day sections (and no longer carry over
  into Today while also being listed as blockers).
- `generate` output is deterministic: section layout, `[status]`, and times
  are rendered by the binary; the model only rephrases task texts (JSON
  contract, count-checked). Any model failure falls back to verbatim texts
  — same layout, no network dependency for the format.
- Multi-line tasks (imported commit bodies) render as one row in `list`
  and the interactive browser instead of breaking the column layout.
- Unreachable endpoints report a friendly hint (`check OPENAI_BASE_URL and
  network`) instead of a raw connection dump.

### Added

- Multi-repo commit ingestion: `commits [days] [paths...]` collects from
  several repositories, deduped by commit hash, ordered by commit time.
- Co-authored commits count as yours: commits carrying your address in a
  `Co-authored-by:` trailer are collected too.
- Full commit bodies: the whole commit message (trailer block stripped)
  becomes the task text, not just the subject line.
- Explicit report windows: `generate --from/--to YYYY-MM-DD` with dated
  headings and no meeting cutoff on historical days.
- Weekend-aware default window: bare `generate` (and `generate 2`) covers
  the last working day onward (Monday reports Friday+Monday), matching the
  `commits` lookback.
- `generate --clip`: copy the report to the clipboard (pbcopy / wl-copy /
  xclip / clip).
- `language` config key (env `STANDUP_LANGUAGE`): report language for the
  model's rephrasing; empty preserves the input language.
- `standup doctor`: checks data-file writability, git identity, provider
  env presence, and endpoint reachability (skipped in offline mode).
- `standup skill install`: writes the agent skill (real files, no symlinks)
  to `.agents/skills/standup/` and `.claude/skills/standup/` in the current
  repo, or to `~/.agents` + `~/.claude` with `--global`, so skills-compatible
  coding agents can log and report standups; idempotent, refreshes on
  re-run. The skill's single home is `config/skill/SKILL.md`, embedded like
  the config defaults.

## [0.4.0] - 2026-08-15

### Added
- Zero-config startup: config defaults are embedded in the binary; every
  command works right after install. Config resolution per file:
  `$STANDUP_CONFIG_DIR` → `./config` → user config dir (`~/.config/standup`,
  `%APPDATA%\standup`) → embedded defaults.
- `standup init`: writes the default `config.yaml` + `agent.yaml` into the
  user config dir for editing (never overwrites existing files).
- `standup status <id> <status>`: sets `todo`/`in-progress`/`blocked`/`done`
  directly — blocked tasks are no longer a dead end (`done` was the only way
  out via CLI).
- `add --raw` (also on the `-a` flag path): stores text verbatim through the
  offline splitter, no model call — verbatim capture while online.
- `.env` files are now also read from the config dirs (cwd `.env` still wins),
  so provider settings no longer need to live in every repo.
- Terminal color palette: task statuses render in their own hue in `list`
  output (todo amber, in-progress blue, blocked red, done green; ids and
  timestamps quiet gray). Truecolor on TTY output only — piped/redirected
  output stays plain and `NO_COLOR` disables it. README badges restyled with
  the project palette; the retired Go Report Card badge was replaced with a
  golangci-lint badge linking to the CI job that runs it.

### Changed
- `--help`, `--version`, and bare `standup` no longer load config or require
  provider env — command wiring is lazy.
- Generate input templates skip empty sections: no empty `## Yesterday`
  heading when there are no yesterday tasks.
- Plain `list` output shows 8-char short ids (UUIDs stay internal; prefix
  matching was already accepted and is now documented in the README).
- Release archives ship the binary only (no README/LICENSE dumped into the
  extraction directory).
- Invalid store statuses now name the valid set in the error.

### Fixed
- Fresh installs no longer die with `read config.yaml: ... cannot find the
  path` — embedded defaults cover it and `init` materializes editable files.
- `commits` in a repo without a configured git `user.email` now prints the
  exact fix (`git config --global user.email …`) instead of a raw
  `exit status 1`.

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
