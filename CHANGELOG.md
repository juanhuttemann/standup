# Changelog

All notable changes to this project are documented here. The format
is based on Keep a Changelog, and this project adheres to Semantic Versioning.

## [Unreleased]

### Added

- `standup login` sets a provider up interactively: pick one from a searchable
  list, paste the API key masked, pick a model — then the command saves the
  settings and proves them with the same real model call `doctor` ends on,
  before it exits. It replaces knowing a wire protocol, an endpoint and an
  exact model id before the tool would run at all, and it is the only writer
  that persists an API key: `config set` still refuses one because it echoes
  what it wrote, which is a rule about echoing, not about writing.
- The provider list is fetched at run time and cached in the active config
  directory, so no endpoint or model id is committed. A failed fetch falls
  back to that cache and says so; with neither, `login` says that too and
  still works, because the last entry in the list is a custom
  OpenAI-compatible endpoint — Ollama, LM Studio, or anything self-hosted.
  Those are in no catalog, so `login` asks the endpoint itself what it serves
  and only falls back to typing a model id when it will not answer.
- A login whose verification fails keeps what you typed and names the setting
  the HTTP status implicates. An unentitled model or a rate limit says nothing
  about the key you just pasted, and making you paste it again would be the
  worst possible answer.
- `login` warns when a provider variable already exported in your environment
  overrides the file it just wrote, which otherwise ends with a login that
  verified fine being ignored by the very next command.

### Changed

- `config set provider` rejects anything but `openai` and `anthropic`. It used
  to accept any string and only fail at the next model call, pointing at the
  endpoint rather than at the setting that was wrong.
- Writing a provider setting now also tightens the `.env` to `0600`. A file
  created by hand at `0644` kept those bits, and it is where API keys live.

## [0.17.0] - 2026-08-21

### Added

- Day sections are split into `### Done`, `### In progress` and `### Next`.
  Completed and planned work rendered as identical bullets differing only by
  an inline `[todo]` tag, and separating the two is the defining structure of
  a standup. The inline status tag is gone: the group heading carries it, and
  `--team` reports nest a level deeper as before.
- `add` with no model endpoint configured now stores the note as typed and
  prints what is missing plus a pointer to `doctor`, instead of exiting 1 with
  nothing captured — which is what the first command on a clean install did.
- Ambiguous id errors list the matching rows, so a retry is a copy-paste
  rather than another `list`.
- `InferStatus` reads work that is underway (`working on`, `in progress`,
  `wip`) as `in-progress`, a validated status the heuristic never produced.

### Changed

- `generate` gives the model editorial work instead of rewording. It used to
  rephrase entries one for one and return its input: an online report was
  byte-identical to the free offline render, on a 6-task day and on a 28-task
  week, so a user could not tell the feature working from the feature being
  off. The model now merges related entries within one section — a run of
  releases, several commits on one feature — into the handful of lines a
  standup is made of, and writes them in one register. Go validates the
  merge: every input line must be covered exactly once and every merged line
  must stay inside its own section, so an entry can never be dropped, moved
  between days, or given a different status. A merged line takes the earliest
  source's time and keeps a branch only when every source shared it.
- Report entries are normalized when rendered, not only when captured.
  Cleanup ran at `add` time, so `--raw` and `commits` entries kept their
  original register forever and one section mixed three of them.
- Status inference reads past the leading verb. Passive voice ("the parser is
  fixed"), an adverb or filler before the verb ("finally got the parser
  fixed", "ugh spent 4 hrs…"), and "done with X" all reported finished work
  as `todo`, and a wrong status is shipped to the team unchecked. The first
  status signal in the sentence now decides, so a stated intention still wins
  when it comes first ("need to fix the deprecated api" stays `todo`).
- Day headings are all relative or all dated, never mixed. A 7-day report read
  `## Sat 2026-08-15`, `## Sun 2026-08-16`, `## Today`, and so did the default
  Monday window (`## Fri 2026-08-14`, `## Today`).
- `-p` resolves most requests in one model call. Delegating to the
  coordinator and its three specialists unconditionally spent over forty
  seconds on requests the user could type as two CLI verbs; the specialists
  now run only when the single pass fails, refuses, or returns a plan that
  cannot be applied. A `-p` run is bounded by six times `model_call_timeout`.
- An operation plan that parses but cannot be applied (an edit with no text,
  a status change with no id) is refused as invalid and retried through the
  specialists, instead of surfacing `batch operation 1: empty task text`.
- `speak` reads like a person: the brief may combine entries into a sentence
  (only inventing extra ones is refused), `#tags` are never read aloud as
  markup, and the deterministic fallback varies its connectives and announces
  planned work as planned instead of repeating "Also," before five items that
  all sounded finished.
- `list` pads the status column and truncates at a word boundary; `add`'s
  echo confirms the whole task, since truncating the one line that exists to
  show what was stored defeats its purpose.
- `agent.yaml` fills every absent key from the embedded default, so a partial
  or older file keeps working. The `reporter` agent is replaced by `curator`,
  `planner_fallback_instructions` is now `planner_direct_instructions`, and
  the two report templates are one `generate_input_template`.

## [0.16.0] - 2026-08-20

### Added

- `standup -p --yes` (`-y`) approves a plan that deletes tasks without a
  confirmation prompt, for unattended runs.

### Changed

- `add` no longer lets the model choose a task's status. The model invented
  `blocked` for routine work ("triaged the flaky CI job"), and an invented
  blocker is reported to the whole team; past-tense work never landed on
  `done`. Statuses are now derived from the task text in Go, the same way on
  every path (model, `--raw`, offline): explicit impediment wording
  ("blocked on", "waiting on", "stuck on") is `blocked`, work described in
  the past tense ("fixed", "reviewed", "wrote") is `done`, everything else
  is `todo`. The rules are English-only by design — anything else lands on
  `todo`, one `standup status <id>` away.

### Fixed

- `doctor` no longer passes a setup that cannot work. It checked that the
  provider variables were present and that the host answered, so it reported
  all-green for a dead API key or a model that does not exist and the next
  command failed. It now finishes with a real model call (a one-word probe,
  prompt in `agent.yaml` like every other agent) and reports `fail model
  answers` with the reason.
- Failed model calls name the setting they are actually about: a rejected
  key points at `OPENAI_API_KEY`/`ANTHROPIC_API_KEY` and a rejected model at
  `OPENAI_MODEL`/`ANTHROPIC_MODEL`, instead of blaming the base URL and the
  network for every failure.
- `standup init` writes into the active config directory, resolved exactly
  like `config set` and `config edit`. With `$STANDUP_CONFIG_DIR` set it
  created files in the home directory that nothing would read, and the
  follow-up `config edit` opened a different file.
- `config set` refuses values that would break every later command:
  `timezone` must be an IANA name and `meeting_time` a 24-hour `HH:MM`, both
  validated with the same parsers the report uses. They used to be accepted
  and then failed on every `list` and `generate` with nothing pointing back
  at the command that set them.
- `standup -p` has a wall-clock budget (five times `model_call_timeout`, one
  per coordinator or specialist call) instead of running unbounded, and no
  longer reports no-op transitions (`blocked -> blocked`) as changes; a plan
  that changes nothing says "no changes". Its failure messages are clipped at
  a word boundary, so the part naming what could have matched survives.
- `doctor` no longer creates the data file it is checking. A read-only
  diagnostic left an empty `tasks.jsonl` behind on a machine that had never
  run standup; it now probes the parent directory and cleans up after itself.
- `list --days 0` is rejected like `--days -5` instead of being silently
  reinterpreted as today, `standup commits 1o` says the argument is neither a
  day count nor a directory, and internal package names (`store:`, `agent:`,
  `report:`) no longer prefix user-facing errors.
- `skill install` warns when it replaces an edited `SKILL.md` instead of
  overwriting it silently.
- `generate --team` produces a readable structure. Every block now carries a
  heading (the person running the report had none, so a reader could not tell
  whose the first one was), headings show the commit author's name instead of
  their email address, and the day headings nest under the author instead of
  sitting beside them.
- `speak` can no longer move work to a day the report does not name. A brief
  claimed yesterday for work done today — a factual error about the user's own
  work, spoken aloud in a meeting. Day words in the brief are now checked
  against the report's headings, and an ungrounded brief falls back to the
  deterministic script, which reads less like a template (one time anchor per
  section instead of one per item).
- `generate` says when it fell back to verbatim task text. The rephrase
  fallback fires exactly when the input is large and messy — when raw text is
  least usable — and it was silent, so a raw commit dump shipped to the team
  looked like the model wrote it. A one-line note on stderr now names the
  reason ("12 entries for 39 tasks", "no JSON entries in the reply", the call
  error). Offline mode says nothing: nothing was going to phrase it.
- Reports and list rows no longer carry whole commit bodies. `commits` still
  stores the full message, but report bullets keep the subject line (the
  committed templates gained a `subject` helper next to `fold`) and list rows
  are bounded, so one 1700-character task stops destroying the layout. The
  reporter is asked to rephrase the subject lines too, which is what made the
  contract fail on a store built from imported commits.
- Neither the editor nor the rephraser can invent `#tags`. `#word` is the
  app's own tag syntax (`list --tag`), so a minted tag showed a tag the task
  does not carry; an invented one now keeps its word and loses the `#`, on
  the stored text as well as the report. A list marker echoed back by the rephraser is
  stripped too, instead of rendering as `- [done] - fixed the bug` and
  travelling on into the spoken brief.
- `generate --clip` no longer hangs. On Wayland `wl-copy` keeps the clipboard
  alive in a forked child that inherited the command's output pipes, so the
  report was copied and the process then waited forever, printing nothing.
  The clipboard helper no longer gets those pipes, and the report is printed
  as usual.
- `generate -o <file>` echoes the path it wrote. A minute of model calls
  ended in complete silence, unlike every other mutating command.
- Delivery failures (`--webhook`, `--mail`, `--clip`) are reported once, as
  the command's error, instead of the same fact on stdout and stderr. `--mail`
  also checks `smtp_host` before generating the report rather than after.
- Deleting tasks now asks first on every path. `standup -p "delete all of my
  tasks"` wiped the whole store with no confirmation and no undo, while `rm`
  refused a single task without `--force`: a plan that deletes anything is
  now previewed line by line and confirmed on a terminal, or refused with a
  pointer to the new `--yes` flag when nothing can be asked. The interactive
  list confirms a delete too, and gained the missing `todo` action so a
  mis-click on `blocked` no longer has to be undone from the command line.
- Concurrent invocations no longer lose tasks and status changes. Every
  mutation is a read-modify-write of the whole JSONL, so overlapping
  commands (an agent driving the CLI, a CI job, a shell loop) clobbered
  each other and still exited 0. Writers now serialize on a lock file
  beside the store; a writer that cannot get the lock within 10 seconds
  fails by name instead of hanging. Reads are unaffected.

## [0.15.0] - 2026-08-19

### Added

- `standup sync`: keeps one task list across machines by merging the local
  store with a PocketBase server. Point `sync.url` at the server in
  `config.yaml`. The PocketBase connection shares one env prefix so each
  fact has exactly one name: `PB_URL`/`PB_COLLECTION` override the yaml
  keys, and `PB_EMAIL`/`PB_PASSWORD` are credentials that exist only in the
  environment or a `.env`, never a config file.
  The first sync provisions the collection (`sync.collection`,
  default `standup_tasks`) with superuser-only access, so PocketBase needs
  no manual setup, and sync stays off until `sync.url` is set. Merging is
  deterministic and model-free: tasks union by id, the most recently
  changed copy wins (ties go to the server), deletes travel as tombstones
  so a removal propagates instead of being pushed back, and the same commit
  imported on two machines collapses to one task. The merged result is
  saved locally before anything is uploaded, so an interrupted sync leaves
  a consistent store and retries on the next run; a second sync with
  nothing to do is a no-op. A remote record edited by hand into an invalid
  one is refused by name (record, id, bad value, and where to fix it) with
  zero local writes.

### Changed

- Deletes now tombstone instead of dropping a line, on both write paths
  (`rm` and the batch path behind `standup -p`): the record stays in the
  JSONL stamped with its deletion time so `sync` can propagate the removal,
  and every mutation on both paths stamps an update time for last-writer-
  wins ordering. `list` and reports hide tombstones, a deleted id reads as
  unknown, and `standup commits` no longer re-imports a commit whose task
  was deleted.

## [0.14.0] - 2026-08-17

### Added

- `standup version` prints a discoverable contextual version, `add --raw -`
  reads stdin explicitly, and a single `--from` or `--to` selects one report day.

### Changed

- `rm` now identifies the matched task and requires `--force`; `config edit`
  prints the file and editor before launching, while `doctor` prints the resolved
  data-file path.
- Speech documentation names compatible streaming chat-completions audio models,
  planner help sets free-model timeout expectations, and speaker instructions
  explicitly forbid additions beyond the report.

### Fixed

- Online single-task adds deterministically restore any `#tags` stripped by the
  editor model; ungrounded or advice-bearing speaker output falls back to a
  deterministic report-derived script, and speech failures explain that the
  printed script was preserved. Speech synthesis now uses an explicit verbatim
  boundary and rejects audio whose streamed transcript answers or alters the script.

## [0.13.0] - 2026-08-17

### Added

- A configurable `model_call_timeout` bounds each model request, and natural-
  language planning retries malformed tool-based plans through a direct planner.

### Changed

- Report delivery now prints an explicit result for each requested webhook,
  email, and clipboard sink, and attempts every sink before returning combined
  failures; `doctor` also calls out a missing `OPENAI_API_KEY` as optional for
  OpenAI-compatible endpoints.
- Exact duplicate task text now produces a warning without blocking the add,
  and the command guide clarifies that piped input splits on blank lines.

### Fixed

- Agent instructions are no longer duplicated in user messages, multi-task adds
  commit atomically, and secret `config set` attempts now point to the active
  `.env` without echoing or storing credentials.
- Local skill installation replaces Windows checkout placeholders safely and
  preflights both skill roots before writing, ID-only edits refuse to launch an
  editor without an interactive terminal, and source builds explain that
  self-update requires a released binary without making a network request.
- Email delivery rejects newline-bearing sender and recipient values so they
  cannot inject message headers.
- Store reads now reject persisted tasks with missing ids, blank text, invalid
  statuses, or zero timestamps, and identify the invalid JSONL line.
- Reports include tasks timestamped exactly at the meeting cutoff.
- Git commit parsing now uses NUL-delimited fields so commit bodies cannot be
  mistaken for records, and supports both SHA-1 and SHA-256 object ids.

## [0.12.0] - 2026-08-17

### Added

- Direct Anthropic Messages API support for all text-backed commands through
  `provider: anthropic` and `ANTHROPIC_BASE_URL`/`ANTHROPIC_API_KEY`/
  `ANTHROPIC_MODEL`; optional speech synthesis remains on the independently
  configured OpenAI-compatible endpoint.

### Changed

- Refocused the README on the AI-assisted quick start, including prominent
  natural-language `-p` orchestration, while keeping the offline workflow and
  complete command and configuration guide.

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
