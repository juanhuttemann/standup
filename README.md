# standup

<div align="center">

[![Go version](https://img.shields.io/github/go-mod/go-version/juanhuttemann/standup?style=for-the-badge&logo=go&logoColor=white)](https://github.com/juanhuttemann/standup/blob/main/go.mod)
[![CI](https://img.shields.io/github/actions/workflow/status/juanhuttemann/standup/ci.yml?style=for-the-badge&logo=githubactions&logoColor=white)](https://github.com/juanhuttemann/standup/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/juanhuttemann/standup?style=for-the-badge&logo=github)](https://github.com/juanhuttemann/standup/releases)
[![golangci-lint](https://img.shields.io/badge/golangci--lint-enabled-3FB950?style=for-the-badge&logo=go&logoColor=white&labelColor=1B2127)](https://github.com/juanhuttemann/standup/actions/workflows/ci.yml)
[![MIT license](https://img.shields.io/badge/License-MIT-3FB950?style=for-the-badge&labelColor=1B2127)](https://github.com/juanhuttemann/standup/blob/main/LICENSE)
![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20macOS%20%7C%20Windows-E8EDF2?style=for-the-badge&labelColor=1B2127)

</div>

AI-assisted standup CLI. Requires Go 1.26+.

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

Both fetch the latest release binaries; see [releases](https://github.com/juanhuttemann/standup/releases). To build from source: `go build -o standup ./cmd/standup`.

`standup update` compares your version with the latest release and prints
the installer command to rerun. Uninstall: delete the binary
(`rm ~/.local/bin/standup`; root installs live in `/usr/local/bin`; Windows:
delete `%LOCALAPPDATA%\standup` and remove it from PATH).

## Setup

The binary embeds working defaults, so read-only commands work immediately.
Before model-assisted `add`, `generate`, or `speak`, configure a provider or
enable offline mode:

```sh
standup config set offline true
```

## Configuration

```sh
standup config set offline true       # use without a model endpoint
standup config set meeting_time 09:30
standup config set OPENAI_BASE_URL http://localhost:8080/v1
standup config set OPENAI_MODEL my-model
standup config edit                   # open config.yaml in $EDITOR
```

Application settings are stored in the active `config.yaml`; provider settings
use that directory's `.env`, never YAML. The active directory is
`$STANDUP_CONFIG_DIR`, otherwise an existing `./config`, otherwise the user
config dir (`~/.config/standup`, or `%APPDATA%\standup` on Windows).

You can also run `standup init` to write `config.yaml` + `agent.yaml`, or keep
a `config/` dir beside the working directory. Resolution order per file is
`$STANDUP_CONFIG_DIR` → `./config` → the user config dir → embedded defaults.
`STANDUP_*` environment variables override YAML.

Online mode needs `OPENAI_BASE_URL` (OpenAI-compatible endpoint) and
`OPENAI_MODEL` (served model name) in the environment or a `.env` — looked
up in the working directory or any parent (like git resolves its config),
then the config dirs. Only `add`, `generate`, and `speak` use a model; every
other command runs without credentials. Skip both for offline mode (below).

## Use

```sh
standup add "fixed login bug"       # or: standup -a "fixed login bug"
standup add --raw "verbatim text"   # skip the model, one paragraph = one task
cat notes.txt | standup add         # piped text becomes tasks
standup commits [days] [paths...]   # git commits become done tasks (default: last working day)
standup list                        # or: standup -l  (arrow keys to navigate, Enter to act)
standup list --days 5               # trailing N days, oldest first, plain output
standup list --date 2026-08-10      # one calendar day, plain output
standup list --tag api              # only tasks carrying the literal #api token
standup generate                    # or: standup -g  (last working day + today before meeting time)
standup generate 5 -o standup.md    # multi-day report written to a file
standup generate --from 2026-08-03 --to 2026-08-07   # explicit window, dated headings
standup generate --clip             # copy the report to the clipboard
standup generate --webhook <url>    # POST the report (Slack-compatible JSON)
standup generate --mail <address>   # email the report (needs smtp_* in config.yaml)
standup speak                       # print the standup as a spoken brief (free preview, no audio)
standup speak -o standup.wav        # synthesize the brief to audio (needs OPENAI_SPEECH_MODEL/VOICE env)
standup status <id> in-progress     # set status: todo, in-progress, blocked, done
standup done <id>                   # mark done, no model involved
standup edit <id> "fixed text"      # no argument opens $EDITOR (fallback vi, notepad on Windows)
standup rm <id>                     # delete, no model involved
standup doctor                      # check the setup: data file, git identity, endpoint
standup skill install               # teach your AI agent the standup workflow (this repo)
standup skill install --global      # same, for every repo (~/.agents, ~/.claude)
standup init                        # write default config files for editing
standup config set KEY VALUE        # set an app or provider value
standup config edit                 # open the user config.yaml
standup update                      # check for a newer release
```

Commands that change a task print the resulting row. `<id>` accepts any
unambiguous id prefix (list output shows the first 8 characters).

Tasks carry a status (`todo`, `in-progress`, `blocked`, `done`). Yesterday's
unfinished tasks carry over into the Today section; blocked tasks are moved
to a `## Blockers` section until resolved. Tags are plain text: a trailing
`#word` token anywhere in task text.

Imported commits are stored with the commit's author date (day attribution
survives), the full message without its trailer block, and `done` status —
including commits you only co-authored. Re-running `commits` skips commits
already imported. Multi-repo: pass extra paths (`standup commits 1 ../api
../web`); submodules of each repo are included automatically. The
`repos.include`/`repos.exclude` glob lists in `config.yaml` filter which
repos and submodules get ingested (matched against each path as passed) —
skip forks and vendored dirs without listing paths every run.
`standup commits --branch` also records the branch name (best-effort,
`git name-rev` semantics); `list` and reports then render it as `[branch]`.

Team standup: `standup commits --all-authors` imports everyone's commits
and records the author on each; `standup generate --team` then renders one
section per author (`## alice@example.com`). The store stays one personal
file — grouping happens at report time.

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
binary; a model only rephrases the task texts. If the model is unreachable
or answers off-contract, `generate` falls back to the verbatim texts — same
layout, zero network dependency for the format. Missing provider env,
however, is a configuration error and fails fast with a hint — set
`offline: true` for the credential-free render.

## Offline mode

Set `offline: true` in `config.yaml` (or `STANDUP_OFFLINE=true`):
`add` splits input on blank lines (one paragraph, one task, verbatim) and
`generate` renders the deterministic day-split template directly. No model
endpoint needed; the `OPENAI_*` variables become optional.

Tasks live in a JSONL file (`data_file`, default `~/.standup/tasks.jsonl`).

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

## Tests

`make verify` (fmt, vet, cyclo, ineffassign, golangci, deadcode, tests) — no server required.
