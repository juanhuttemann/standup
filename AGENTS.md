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
- Deterministic work stays deterministic: time math, day split, ordering, and the meeting
  cutoff live in `internal/report`. A model never sorts, filters by time, or invents ids.

### Config policy
- Each setting has exactly one home — never duplicate a setting across files:
  application settings (`meeting_time`, `data_file`) in `config/config.yaml`;
  provider facts (`OPENAI_BASE_URL`, `OPENAI_MODEL`) in local `.env`/env only.
- Precedence: `STANDUP_*` env > `.env` > yaml. Config dir: `$STANDUP_CONFIG_DIR` else `./config`.
- Committed files contain zero provider references: no endpoints, no model names, anywhere.
  Provider settings are deployment facts — required in local `.env`/env, never defaulted,
  never committed. `config.Load` errors if missing.
- `.env.example` is a template for one audience: the person installing the tool.
  One short comment per setting. No policy essays, no cross-references to other files,
  no conflicting instructions.

### TDD loop
- Write the failing test first (testify). Tests use `t.TempDir()` and fake `Assistant` impls.
- Pure packages (`store`, `report`, `config`) are tested without any network.
- `internal/agent` exposes an `Assistant` interface so CLI tests never hit a server.
- `make verify` (fmt, vet, cyclo, ineffassign, golangci, deadcode, test) must pass before
  any commit and before calling any work done.
- Never discard errors (`_ =`, `_, _ =`). Handle, wrap, or return them — in tests, assert
  them. Lazy work is not allowed.

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
- `internal/report` — standup split/ordering.
- `internal/config` — viper loading, env overrides.

### Statuses
- `todo` | `in-progress` | `done` — validated at the store boundary.
