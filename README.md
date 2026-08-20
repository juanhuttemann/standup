# standup

<div align="center">

[![Go version](https://img.shields.io/github/go-mod/go-version/juanhuttemann/standup?style=for-the-badge&logo=go&logoColor=white)](https://github.com/juanhuttemann/standup/blob/main/go.mod)
[![CI](https://img.shields.io/github/actions/workflow/status/juanhuttemann/standup/ci.yml?style=for-the-badge&logo=githubactions&logoColor=white)](https://github.com/juanhuttemann/standup/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/juanhuttemann/standup?style=for-the-badge&logo=github)](https://github.com/juanhuttemann/standup/releases)
[![golangci-lint](https://img.shields.io/badge/golangci--lint-enabled-3FB950?style=for-the-badge&logo=go&logoColor=white&labelColor=1B2127)](https://github.com/juanhuttemann/standup/actions/workflows/ci.yml)
[![MIT license](https://img.shields.io/badge/License-MIT-3FB950?style=for-the-badge&labelColor=1B2127)](https://github.com/juanhuttemann/standup/blob/main/LICENSE)
![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20macOS%20%7C%20Windows-E8EDF2?style=for-the-badge&labelColor=1B2127)

</div>

AI-assisted standups from rough notes, tasks, and Git history.

Write what you worked on in your own words. `standup` turns it into clean task
entries, lets you manage them with one natural-language request, and rephrases
the result into a daily update with a consistent structure. Task state,
statuses, report layout, time windows, and writes stay deterministic — the
wording is the model's, everything else is the binary's. Work on more than
one machine? [Sync](#sync-across-machines) keeps them on one task list.

![demo](demo.gif)

## Quick install

Linux, macOS, WSL2, Termux (installs to `~/.local/bin`):

```sh
curl -fsSL https://raw.githubusercontent.com/juanhuttemann/standup/main/scripts/install.sh | bash
```

Windows (native, PowerShell):

```powershell
iex (irm https://raw.githubusercontent.com/juanhuttemann/standup/main/scripts/install.ps1)
```

Both fetch the latest release binaries; see [releases](https://github.com/juanhuttemann/standup/releases).

## Quick start

Point `standup` at an OpenAI-compatible endpoint and its served model:

```sh
standup config set OPENAI_BASE_URL http://localhost:8080/v1
standup config set OPENAI_MODEL my-model
# For hosted endpoints, put OPENAI_API_KEY in your environment or the active .env.
standup doctor
```

Or use Anthropic's Messages API directly:

```sh
standup config set provider anthropic
standup config set ANTHROPIC_BASE_URL https://api.anthropic.com
standup config set ANTHROPIC_MODEL your-model
export ANTHROPIC_API_KEY=your-api-key  # or add it to the active config directory's .env
standup doctor
```

`doctor` finishes with a real one-word model call, so a dead key or a model
name that does not exist is reported now instead of on your next command.

Then capture work naturally and generate the report:

```sh
standup add "fixed the login redirect and reviewed the release"
standup generate
```

The assistant cleans and splits the note into tasks, then rephrases their text
for the standup. The binary—not the model—decides what belongs in Yesterday,
Today, and Blockers, carries unfinished work forward, and validates every
change before writing it.

Manage several tasks in one request with `-p`:

```sh
standup -p "mark the login work done, block the release review, and add API docs for today"
```

The coordinator delegates creates, edits, status changes, and deletions to
specialists, validates their complete operation plan, and applies the batch
atomically. If a target is missing or ambiguous, nothing is written. Add
`--verbose` to see the specialist tool calls.

Already have the work in Git? Import it as completed tasks before generating:

```sh
standup commits
standup generate
```

Repeated imports are deduplicated. The default window is the last working day,
so a Monday standup finds Friday's commits.

### No model endpoint?

The full task and reporting loop also works locally, without credentials:

```sh
standup config set offline true
standup add "fixed the login redirect"
standup commits
standup generate
```

Offline `add` stores one task per blank-line-separated paragraph, verbatim.
Offline `generate` uses the same deterministic report layout. Tasks stay in a
local JSONL file in either mode.

## What the AI helps with

- Cleans up rough notes and splits multi-task input while preserving tags.
- Rephrases task text into a concise standup without controlling its structure.
- Coordinates natural-language create, edit, status, and delete requests with
  specialized agents, then applies the validated plan as one atomic batch.
- Turns the rendered report into a spoken brief, with optional WAV synthesis.

AI is an enhancement, not the source of truth: models read and phrase; Go owns
task IDs, statuses, ordering, time math, storage, and report formatting. What
the model returns is checked before it is used: an entry count that does not
match the tasks, an invented `#tag`, or a spoken brief that names a day the
report does not — all fall back to the deterministic text, and `generate` says
so on stderr when it happens.

## Command guide

```sh
standup add "fixed login bug"       # or: standup -a "fixed login bug"
standup add --raw "verbatim text"   # skip the model, one paragraph = one task
printf 'verbatim text' | standup add --raw -  # explicit stdin marker
cat notes.txt | standup add         # blank-line-separated paragraphs become tasks
standup commits [days] [paths...]   # git commits become done tasks (default: last working day)

standup generate                    # or: standup -g  (last working day + today before meeting time)
standup generate 5 -o standup.md    # multi-day report written to a file
standup generate --from 2026-08-03 --to 2026-08-07   # explicit window, dated headings

standup list                        # or: standup -l  (arrow keys to navigate, Enter to act)
standup list --days 5               # trailing N days, oldest first, plain output
standup list --date 2026-08-10      # one calendar day, plain output
standup list --tag api              # only tasks carrying the literal #api token

standup done <id>                   # mark done, no model involved
standup status <id> in-progress     # set status: todo, in-progress, blocked, done
standup edit <id> "fixed text"      # no argument opens $EDITOR (fallback vi, notepad on Windows)
standup rm --force <id>             # delete after verifying the target shown without --force
standup -p "add this today and mark yesterday done"  # apply mixed CRUD atomically
standup -p "delete the old tasks" --yes  # approve a plan that deletes, unattended
standup -p "add this today" --verbose  # show coordinator → specialist tool calls
printf '%s\n' "delete the obsolete task" | standup -p -  # read the prompt from stdin

standup generate --clip             # copy the report to the clipboard
standup generate --obsidian         # publish into the configured Obsidian vault
standup generate --webhook <url>    # POST the report (Slack-compatible JSON)
standup generate --mail <address>   # email the report (needs smtp_* in config.yaml)
standup speak                       # print a spoken brief (no speech synthesis)
standup speak -o standup.wav        # synthesize via a chat-completions audio-output model
standup sync                        # merge tasks with your PocketBase server (see Sync)

standup doctor                      # check the setup: data file, git identity, live model call
standup init                        # write default config files for editing
standup config set KEY VALUE        # set an app or provider value
standup config edit                 # open the active config.yaml
standup skill install               # teach your AI agent the standup workflow (this repo)
standup skill install --global      # same, for every repo (~/.agents, ~/.claude)
standup update                      # securely update to the latest release
standup update --check              # check without installing
standup version                     # print the version with context
```

### Tasks and reports

- Statuses are `todo`, `in-progress`, `blocked`, and `done`. The binary picks
  one from the task's own text — impediment wording ("blocked on", "waiting
  on", "stuck on") is `blocked`, past-tense work ("fixed", "reviewed",
  "wrote") is `done`, anything else is `todo`. The rules are English-only;
  `standup status <id>` overrides any of them.
- Commands that change a task print the updated row.
- `-p` delegates interpretation to CRUD specialists, validates their complete
  plan, and applies it in one write; missing or ambiguous targets change
  nothing. A plan that deletes anything is previewed and confirmed first —
  pass `--yes` to approve it unattended.
- A whole `-p` run is bounded by five times `model_call_timeout`.
- Overlapping commands are safe: writers take a lock file beside the store,
  so a `commits` and an `add` running at once cannot lose each other's tasks.
- Online commands preflight endpoint connectivity for two seconds before the
  first model call, so stale local endpoints fail quickly.
- Agent coordination can require several model round trips. Increase
  `model_call_timeout` (for example, `standup config set model_call_timeout 2m`)
  for slow free-tier models.
- Task IDs accept any unambiguous prefix; `list` displays the first 8 characters.
- Unfinished tasks from yesterday carry over into Today.
- Blocked tasks appear under `## Blockers` until resolved.
- Tags are `#word` tokens in task text.
- Report bullets show a task's first line; `list` rows are truncated to keep
  the columns readable. The store always keeps the full text.
- `meeting_time` bounds today's section at the meeting; once it has passed,
  the section runs to now.

### Importing commits

`standup commits` imports commits as completed tasks. It preserves the commit's
author date and full message, removes trailer blocks, includes co-authored
commits, and skips commits already imported.

- Pass repository paths for a multi-repo import:
  `standup commits 1 ../api ../web`.
- Submodules are included automatically.
- Use `repos.include` and `repos.exclude` in `config.yaml` to filter repositories.
- Add `--branch` to show branch attribution as `[branch]` in lists and reports.

For a team report, run `standup commits --all-authors`, followed by
`standup generate --team`. The report gets one `##` section per author — named
after the commit author, with your own git user name over your own block — and
the day headings nest under it. The underlying store remains a single personal
file.

### Obsidian

Obsidian export is one-way; the JSONL task store remains the source of truth.

1. Set `obsidian.vault` to the vault directory.
2. Optionally set `obsidian.note` (default: `Standups/{date}.md`).
3. Run `standup generate --obsidian`.

`{date}` resolves to `YYYY-MM-DD` in the configured timezone. For existing
notes, standup replaces only the content between its managed
`standup:start` and `standup:end` markers.

## Sync across machines

Work on a laptop and a desktop? `standup sync` keeps one task list on both.
Point `sync.url` at a [PocketBase](https://pocketbase.io) server in
`config.yaml` and put the superuser credentials in your environment or
`.env` — they are never read from a config file:

```sh
PB_EMAIL=admin@example.com
PB_PASSWORD=your-superuser-password
```

```sh
standup sync    # merge local and remote, both directions
```

That is the whole setup. The first sync creates the collection
(`sync.collection`, default `standup_tasks`) with superuser-only access, so
there is nothing to configure in PocketBase by hand. Sync stays off until a
url is set, and every other command keeps working without it.

The four PocketBase settings share one prefix, so each has exactly one name:
`PB_URL` and `PB_COLLECTION` override `sync.url` and `sync.collection` from
the environment, and `PB_EMAIL`/`PB_PASSWORD` exist only there.

What to expect:

- **Both directions, one command.** Local tasks go up, remote tasks come
  down, and re-running with nothing to do is a no-op.
- **Edits win by recency.** The most recently changed copy of a task wins;
  exact ties go to the server.
- **Deletes travel.** `standup rm` on one machine removes the task
  everywhere, instead of the other machines pushing it back.
- **No duplicate commits.** The same commit imported on two machines
  collapses to a single task.
- **Safe to interrupt.** The merged result reaches your disk before
  anything is uploaded, so a failed sync just retries on the next run.

No model is involved: merging is deterministic, and `sync` never needs
`OPENAI_*` credentials.

Note that `PB_PASSWORD` is a PocketBase *superuser* password —
whoever holds it controls that whole server. Point `sync.url` at a server
you keep for standup rather than one hosting anything else.

## Use with AI agents

`standup skill install` teaches your coding agent the standup workflow —
it writes the skill once and every skills-compatible agent in that scope
can log tasks and run standups for you:

| Scope | Command | Skill lands in |
|---|---|---|
| this repo | `standup skill install` | `.agents/` + `.claude/` in the repo (commit them) |
| all your repos | `standup skill install --global` | `~/.agents/skills/` + `~/.claude/skills/` |

Works with Claude Code, Codex, Cursor, OpenCode, Amp, Copilot, Gemini CLI,
Goose. Then ask your agent:

> Run `standup commits` to log your work, then `standup generate`.

## Configuration

```sh
standup config set meeting_time 09:30
standup config set obsidian.vault /path/to/vault
standup config set obsidian.note 'Standups/{date}.md'
standup config edit
```

Application settings are stored in the active `config.yaml`; provider settings
use that directory's `.env`, never YAML. The active directory is
`$STANDUP_CONFIG_DIR`, otherwise an existing `./config`, otherwise the user
config directory (`~/.config/standup`, or `%APPDATA%\standup` on Windows).

You can also run `standup init` to write `config.yaml` and `agent.yaml` without
replacing existing files, in that same active directory. Resolution order per
file is `$STANDUP_CONFIG_DIR` → `./config` → the user config directory →
embedded defaults. `STANDUP_*` environment variables override `.env`, which overrides
YAML.

Online mode needs `OPENAI_BASE_URL` and `OPENAI_MODEL` in the environment or a
`.env` by default. Hosted endpoints may additionally require `OPENAI_API_KEY`;
because `config set` echoes values, put secrets directly in the environment or
the active `.env`. Set `provider: anthropic` (or `STANDUP_PROVIDER=anthropic`)
to use the Anthropic Messages API with `ANTHROPIC_BASE_URL`,
`ANTHROPIC_API_KEY`, and `ANTHROPIC_MODEL` instead. The `.env` lookup walks up
from the working directory, like Git, before checking the config directories.
Only online `add`, `generate`, `speak`, and `-p` construct the assistant; task
management and commit import need no provider credentials. Speech synthesis
remains OpenAI-compatible and independently requires `OPENAI_BASE_URL`,
`OPENAI_SPEECH_MODEL`, and `OPENAI_SPEECH_VOICE` only when `speak -o` runs.
The speech model must produce audio through streaming chat completions; classic
`tts-1`-style `/audio/speech` models are not compatible. On OpenRouter, current
examples are `openai/gpt-audio-mini` and `openai/gpt-audio`. The script is
printed before synthesis, and synthesis errors explicitly identify it as the
preserved preview.

## Report language

Set `language:` in `config.yaml` (or `STANDUP_LANGUAGE`) — the model
rephrases task entries (and the `speak` brief) in that language. Empty keeps
the input language.

## Timezone

Set `timezone:` in `config.yaml` (or `STANDUP_TIMEZONE`) to an IANA name
(e.g. `Asia/Tokyo`) — report windows, the meeting cutoff and the day split
follow it. Empty uses the machine's local zone.

## Deterministic reports

The report layout (sections, `[status]`, times) is always rendered by the
binary; a model only rephrases the task texts. Consistency is a promise about
structure, not wording: the same tasks always produce the same sections, order
and timestamps, while the phrasing varies between runs.

If the model is unreachable or answers off-contract, `generate` falls back to
the verbatim texts — same layout, zero network dependency for the format — and
prints a one-line note on stderr naming the reason, so a verbatim report is
never mistaken for a written one. Missing provider env, however, is a
configuration error and fails fast with a hint — set `offline: true` for the
credential-free render.

## Offline mode

Set `offline: true` in `config.yaml` (or `STANDUP_OFFLINE=true`):
`add` splits input on blank lines (one paragraph, one task, verbatim) and
`generate` renders the deterministic day-split template directly. No model
endpoint needed; all provider variables become optional.

Tasks live in a JSONL file (`data_file`, default `~/.standup/tasks.jsonl`).
The binary expands `~` with the operating system user-home API, including on
Windows; `standup doctor` prints the resolved path it checks.

## Run it on a schedule (GitHub Actions)

Offline mode keeps a scheduled standup credential-free — the only secret is
the webhook URL. `.github/workflows/standup.yml` in your repo:

```yaml
name: standup
on:
  schedule:
    - cron: '0 8 * * 1-5'   # weekdays, 08:00 UTC
  workflow_dispatch:         # manual run button
jobs:
  standup:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
        with:
          fetch-depth: 0     # `standup commits` walks history
      - run: curl -fsSL https://raw.githubusercontent.com/juanhuttemann/standup/main/scripts/install.sh | bash
      - run: echo "$HOME/.local/bin" >> "$GITHUB_PATH"
      - run: |
          git config user.email you@example.com   # whose commits to import
          standup commits 1
          standup generate --webhook "$SLACK_WEBHOOK"
        env:
          STANDUP_OFFLINE: 'true'
          SLACK_WEBHOOK: ${{ secrets.SLACK_WEBHOOK }}
```

The store is ephemeral in CI: every run re-imports the last working day's
commits into a fresh file and posts the rendered report.

## Install notes

`standup update` securely downloads, verifies, and installs the latest release
in place; `standup update --check` only reports whether one is available.

To build from source, install Go 1.26+ and run:

```sh
go build -o standup ./cmd/standup
```

To uninstall, delete the binary (`rm ~/.local/bin/standup`; root installs live
in `/usr/local/bin`; Windows: delete `%LOCALAPPDATA%\standup` and remove it from
PATH).

## Tests

`make verify` (fmt, vet, cyclo, ineffassign, golangci, deadcode, tests) — no server required.
