# AGENTS.md

Working rules for anyone (human or agent) touching this repo. AGENTS.md is NOT a README.

## Rules

- Minimalism is key: the least code that works, nothing speculative.
- Prefer existing solutions (viper, cobra, google uuid, etc.) before reinventing the wheel.
- Never commit references to any specific model or provider.
- Maintain a separation of concerns.
- Keep configuration centralized in dedicated files.
- Keep prompts outside the code.
- Follow TDD.
- Use test libraries like testify.
- Tests must not depend on a running LLM.
- Follow framework docs — never hack your way by reading framework/package source code.
- Do not overload an agent — use the framework's tools/orchestration and split work
  across sub-agents.
- Keep this AGENTS.md current with these rules and instruction patterns.
- Keep a minimal README.md (instructions only, no architecture).

## Instruction patterns (how these rules translate to decisions)

### Agent policy
- One responsibility per agent. Never widen an existing agent's instructions; add a new agent.
- Agents are declared in `config/agent.yaml` (prompts live there, never in code).
- Agent input message formats are `text/template`s in `config/agent.yaml` — the Go SDK
  ships no prompt-template facility (per the framework's feature-comparison docs);
  stdlib templates fill that gap without reinventing anything.
- Models read; Go writes. A model never mutates the store: extracted editor output is
  persisted deterministically in Go (a model-driven tool loop was observed to execute
  the same write repeatedly). Store mutations happen in `store`/`cli` only.
- Models phrase; Go formats. Report layout (sections, `[status]`, times) is rendered
  from the template in Go; the reporter only rephrases task texts over a count-checked
  JSON contract and any failure falls back to verbatim texts.
- Deterministic work stays deterministic: time math, day split, ordering, and the meeting
  cutoff live in `internal/report`. Commit ingestion is deterministic end to end (author
  dates, dedupe, `done` status) — a model never touches it. A model never sorts, filters
  by time, or invents ids.
- The assistant is lazy: only `add` (online) and `generate` construct it, so read-only
  commands never require provider credentials.

### Config policy
- Each setting has exactly one home — never duplicate a setting across files:
  application settings (`meeting_time`, `data_file`, `offline`, `language`) in
  `config/config.yaml`; provider facts (`OPENAI_BASE_URL`, `OPENAI_MODEL`) in
  local `.env`/env only.
- Precedence: `STANDUP_*` env > `.env` > yaml. `.env` resolves by walking up from
  the cwd (like git); config files resolve per file through the dir chain
  `$STANDUP_CONFIG_DIR` → `./config` → user config dir →
  embedded defaults (`config/embed.go` embeds the committed yaml; the yaml
  files stay the single home of every setting).
- Committed files contain zero provider references: no endpoints, no model
  names, anywhere. Provider settings are deployment facts — required in local
  `.env`/env for online mode (checked by `agent.New`, never defaulted), never
  committed. `--help`/`--version`/`init` never load them.
- `.env.example` is a template for one audience: the person installing the tool.
  One short comment per setting. No policy essays, no cross-references to other
  files, no conflicting instructions.

### TDD loop
- Write the failing test first (testify). Tests use `t.TempDir()` and fake `Assistant` impls.
- Pure packages (`store`, `report`, `config`) are tested without any network.
- `internal/agent` exposes an `Assistant` interface so CLI tests never hit a server.
- `make verify` (fmt, vet, cyclo, ineffassign, golangci, deadcode, test,
  ignored-go — the last fails if any `.go` file is gitignored, e.g. by an
  unanchored pattern) must pass before any commit and before calling any work
  done.
- Never discard errors (`_ =`, `_, _ =`). Handle, wrap, or return them — in tests, assert
  them. Lazy work is not allowed.

### E2E testing (real binary, manual)
- Build the real binary and run it in a scratch environment: `STANDUP_DATA_FILE` in a
  temp dir, `</dev/null` to force non-interactive runs, `STANDUP_CONFIG_DIR` when running
  outside the repo (config dir is cwd-relative). Assert exit codes: 0 ok, 1 error,
  130 SIGINT, 143 SIGTERM.
- Deterministic paths first: `STANDUP_OFFLINE=true` covers add (paragraph split) and
  generate (template render) with no network. Only model-dependent behavior needs a
  live endpoint — passed via env, never committed.
- A mock OpenAI endpoint must set `"finish_reason": "stop"` or the framework discards
  the completion. Use mocks for rogue-model tests: non-JSON output and invalid statuses
  must fail closed with zero store writes (assert `wc -l` on the JSONL).
- Injection is tested as data, not commands: injected task text and commit subjects may
  only ever produce ordinary validated tasks — never extra sections, statuses, or writes.
- Cancellation: use `timeout --preserve-status -s INT <secs> ./standup ...` for a real
  foreground SIGINT (background `&` jobs ignore SIGINT in shells); after cancellation
  the store must contain no partial writes. TTY-only paths (interactive list) run under
  `script -qec` with keystroke bytes (`printf '\003'`, `'\004'`); assert the binary's
  exit code, not the pipeline's.
- Fixtures: edit the JSONL directly (python one-liner) to backdate timestamps or set
  statuses for report tests. Scratch git repos must place out-of-window commits at the
  base (`--since` traversal prunes their ancestors), set dates via
  `GIT_AUTHOR_DATE`/`GIT_COMMITTER_DATE` env, and simulate teammates via
  `GIT_AUTHOR_EMAIL`.
- Every misbehavior found this way becomes a unit test in the same work unit as the fix.

### Changelog
- Every user-visible change gets a CHANGELOG.md entry (Keep a Changelog format,
  one `## [x.y.z] - date` section per release, `### Added/Changed/Fixed` bullets)
- in the same work unit. Work is not done until `make verify` is green AND the
  changelog reflects the change.

### Layout (separation of concerns)
- `cmd/standup` — entrypoint only.
- `internal/cli` — cobra commands, no business logic.
- `internal/agent` — sub-agent wiring + orchestration (the only place that knows providers).
- `internal/store` — Task model + JSONL persistence, injectable clock (`Now` field).
- `internal/report` — standup split/ordering, day ranges, carry-over, lookback time math.
- `internal/git` — commit collection (shells out to git, filtered to the repo's user).
- `internal/config` — viper loading, env overrides.
- `scripts/` — release-binary installers (`install.sh`, `install.ps1`); the
  release pipeline itself is `.goreleaser.yml` + the CI `release` job, flow in
  `RELEASING.md`.

### Statuses
- `todo` | `in-progress` | `blocked` | `done` — validated at the store boundary.
