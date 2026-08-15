# standup

AI-assisted standup CLI. Requires Go 1.26+.

## Setup

1. Copy `.env.example` to `.env` and fill in `OPENAI_BASE_URL` (OpenAI-compatible endpoint) and `OPENAI_MODEL` (served model name). Skip this for offline mode (below).
2. Adjust `config/config.yaml` (`meeting_time`, `data_file`, `offline`) as needed.
3. Build: `go build -o standup ./cmd/standup`

## Use

```sh
standup add "fixed login bug"       # or: standup -a "fixed login bug"
cat notes.txt | standup add         # piped text becomes tasks
standup commits [days]              # git commits become tasks (default: last working day)
standup list                        # or: standup -l  (arrow keys to navigate, Enter to act)
standup list --days 5               # trailing N days, oldest first, plain output
standup list --date 2026-08-10      # one calendar day, plain output
standup list --tag api              # only tasks containing a #tag token
standup generate [days]             # or: standup -g  (yesterday + today before meeting time)
standup generate 5 -o standup.md    # multi-day report written to a file
standup done <id>                   # mark done, no model involved
standup edit <id> "fixed text"      # no argument opens $EDITOR (fallback vi)
standup rm <id>                     # delete, no model involved
```

Tasks carry a status (`todo`, `in-progress`, `blocked`, `done`). Yesterday's
unfinished tasks carry over into the Today section, and blocked tasks get a
`## Blockers` section until resolved. Tags are plain text: a trailing `#word`
token anywhere in task text.

## Report language

Add a sentence like `Write the report in <language>.` to `reporter_instructions`
in `config/agent.yaml` — no rebuild, no flag needed.

## Offline mode

Set `offline: true` in `config/config.yaml` (or `STANDUP_OFFLINE=true`):
`add` splits input on blank lines (one paragraph, one task, verbatim) and
`generate` renders the deterministic day-split template directly. No model
endpoint needed; the `OPENAI_*` variables become optional.

Tasks live in a JSONL file (`data_file`, default `~/.standup/tasks.jsonl`). Config dir defaults to `./config`, override with `STANDUP_CONFIG_DIR`.

## Tests

`go vet ./... && go test ./...` — no server required.
