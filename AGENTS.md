# AGENTS.md

Working rules for anyone (human or agent) touching this repo. AGENTS.md is NOT a README.

## Rules

- Minimalism is key: the least code that works, nothing speculative.
- Prefer existing solutions (viper, cobra, google uuid, etc.) before reinventing the wheel.
- Never commit provider credentials or model/endpoint defaults; isolated provider
  adapters, selection values, and documented environment-variable names are allowed.
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
- Models never judge state. The editor returns texts only; `store.InferStatus`
  derives the status from the task's own text (a model asked to judge invented
  `blocked` for routine work, and an invented blocker reaches the team). Model
  output is checked before use everywhere it can be: entry counts, invented
  `#tags`, and day words absent from the report all fall back to deterministic
  text, and the fallback is reported rather than silent.
- Destructive plans are previewed, never assumed: `-p` prints the deletions and
  asks (or refuses with `--yes` when nothing can be asked), because that is the
  path where a model decides the blast radius. A whole `-p` run is bounded by
  `promptCalls * model_call_timeout`.
- Natural-language CRUD uses Agent Framework agents-as-tools: one coordinator delegates
  to create, update, and delete specialists. They return a typed operation plan; Go
  validates and applies the complete batch atomically, then formats the committed changes.
  `-p --verbose` surfaces framework function-call names only; arguments repeat the task
  snapshot and stay hidden. Online construction preflights endpoint connectivity for two
  seconds before entering the longer per-model-call timeout.
- Models phrase; Go formats. Report layout (sections, `[status]`, times) is rendered
  from the template in Go; the reporter only rephrases task texts over a count-checked
  JSON contract and any failure falls back to verbatim texts. The speaker rewrites the
  rendered report as a spoken brief over the same contract-free boundary: `speak`
  prints the script (a free preview) and `-o` synthesizes it via a streaming chat
  completion with the audio modality; the pcm16 bytes are WAV-wrapped in Go and the
  audio model never sees the store.
- Speaker prompts explicitly forbid advice, plans, task ids, and information
  absent from the rendered report; synthesis failures identify the already
  printed script as the preserved preview. Go rejects ungrounded briefs or
  invented advice and deterministically derives a faithful script from the report.
  The audio request is explicitly verbatim, and Go rejects returned audio when
  the streamed transcript does not match the script's normalized word sequence.
- Deterministic work stays deterministic: time math, day split, ordering, and the meeting
  cutoff live in `internal/report`. Commit ingestion is deterministic end to end (author
  dates, dedupe, `done` status) — a model never touches it. A model never sorts, filters
  by time, or invents ids.
- The assistant is lazy: only `add` (online), `generate`, `speak`, and `-p` construct it, so
  read-only commands never require provider credentials. Speech env
  (`OPENAI_BASE_URL`/`OPENAI_SPEECH_MODEL`/`OPENAI_SPEECH_VOICE`) is checked even
  later: at `speak -o` synthesis time, so the preview needs only the text-provider vars.

### Config policy
- Each setting has exactly one home — never duplicate a setting across files:
  application settings (`meeting_time`, `data_file`, `offline`, `provider`, `language`)
  in `config/config.yaml`; provider facts (`OPENAI_*`, `ANTHROPIC_*`) in local
  `.env`/env only.
- `config set` writes application keys to the user `config.yaml` and the
  supported provider keys to its `.env`; `config edit` opens that YAML. Both
  commands target the active config dir (`$STANDUP_CONFIG_DIR`, otherwise an
  existing `./config`, otherwise the user dir), so writes cannot be shadowed.
- Precedence: `STANDUP_*` env > `.env` > yaml. `.env` resolves by walking up from
  the cwd (like git); config files resolve per file through the dir chain
  `$STANDUP_CONFIG_DIR` → `./config` → user config dir →
  embedded defaults (`config/embed.go` embeds the committed yaml; the yaml
  files stay the single home of every setting). The agent skill follows the
  same rule: `config/skill/SKILL.md` is the single home (embedded for
  `skill install`); the repo's `.agents`/`.claude` entries are symlinks to it.
- Committed files contain zero provider credentials and zero endpoint/model defaults.
  Provider settings are deployment facts — required in local `.env`/env for online
  mode (checked by `agent.New`, never defaulted), never committed with values.
  `--help`/`--version`/`init` never load them.
- `.env.example` is a template for one audience: the person installing the tool.
  One short comment per setting. No policy essays, no cross-references to other
  files, no conflicting instructions.

### Update policy
- `standup update` installs the latest release in place; `--check` is read-only.
- Release tags are compared as semantic versions and updates never downgrade.
- Downloads use version-pinned release URLs, require the matching SHA-256 entry,
  accept only the expected binary archive, validate the candidate's `--version`,
  and replace from the executable's directory so failures leave the old binary.

### Concurrency
- The JSONL store is rewritten whole on every mutation, so every writer takes an
  exclusive lock on `<data_file>.lock` (gofrs/flock) around load-modify-save.
  Reads take nothing: `save` publishes by atomic rename. Never call a locking
  store method from inside `withLock` — the mutex is not reentrant.

### Diagnostics
- `doctor` proves, never assumes: presence and reachability are checks, but the
  last one is a real model call. Failed model calls name the setting the HTTP
  status implicates (`*_API_KEY` for 401/403, `*_MODEL` for 400/404), never the
  base URL by default.
- User-facing errors carry no internal package prefixes; `lazy`/`runRoot` strip
  them at the CLI boundary.

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
- `internal/agent` — sub-agent wiring + provider client construction; provider setting
  validation remains centralized in `internal/config`.
- `internal/store` — Task model + JSONL persistence, injectable clock (`Now` field).
- `internal/report` — standup split/ordering, day ranges, carry-over, lookback time math.
- `internal/git` — commit collection (shells out to git, filtered to the repo's user).
- `internal/config` — viper loading, env overrides.
- `scripts/` — release-binary installers (`install.sh`, `install.ps1`); the
  release pipeline itself is `.goreleaser.yml` + the CI `release` job, flow in
  `RELEASING.md`.

### Statuses
- `todo` | `in-progress` | `blocked` | `done` — validated at the store boundary,
  and derived there too (`store.InferStatus`, English-only, `todo` when unsure).
- Destructive `rm` requires `--force` after the refusal message identifies the
  matched task; `add --raw -` is the explicit stdin form.
