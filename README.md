# standup

AI-assisted standup CLI. Requires Go 1.26+.

## Setup

1. Copy `.env.example` to `.env` and fill in `OPENAI_BASE_URL` (OpenAI-compatible endpoint) and `OPENAI_MODEL` (served model name).
2. Adjust `config/config.yaml` (`meeting_time`, `data_file`) as needed.
3. Build: `go build -o standup ./cmd/standup`

## Use

```sh
standup add "fixed login bug"       # or: standup -a "fixed login bug"
cat notes.txt | standup add         # piped text becomes tasks
standup list                        # or: standup -l  (arrow keys to navigate, Enter to act)
standup generate                    # or: standup -g  (yesterday + today before meeting time)
standup done <id>                   # mark done, no model involved
standup rm <id>                     # delete, no model involved
```

Tasks live in a JSONL file (`data_file`, default `~/.standup/tasks.jsonl`). Config dir defaults to `./config`, override with `STANDUP_CONFIG_DIR`.

## Tests

`go vet ./... && go test ./...` — no server required.
