---
name: standup
description: Log tasks and generate daily standup reports. Use when the user mentions standup, daily update, blockers, or what they worked on.
---

# standup

CLI that logs work and generates standup reports from a local task store
(JSONL). Statuses: `todo`, `in-progress`, `blocked`, `done`. Tags are
trailing `#word` tokens in task text.

## Run what when

- After finishing a work item: `standup add "what was done"` — splits
  multi-task input, preserves `#tags`; `--raw` stores text verbatim.
- From a git repo: `standup commits` imports the last working day's commits
  as done tasks (stamped with commit time, deduped on re-run, submodules
  included); `standup commits 3` for N days, extra args are repo paths,
  `repos.include`/`repos.exclude` globs filter ingestion, `--branch`
  records the branch for `[branch]` attribution in list/report rows,
  `--all-authors` imports the whole team's commits with authors recorded.
- Team standup: `standup generate --team` renders one section per recorded
  author; tasks without an author render unattributed.
- Each morning: `standup generate` — yesterday + today (weekend-aware,
  cutoff at the configured meeting time), blocked tasks under
  `## Blockers`. `standup generate 5` for more days, `--from`/`--to
  YYYY-MM-DD` for an explicit window, `--clip` to copy the report,
  `--webhook <url>` to POST it (Slack-compatible JSON), `--mail <address>`
  to email it (needs `smtp_*` config), `--obsidian` to publish into the
  configured Obsidian vault.
- Status changes: `standup done <id>`, `standup status <id> blocked` (or
  `todo`, `in-progress`).
- Deletion: inspect the target reported by `standup rm <id>`, then confirm it
  with `standup rm --force <id>`.
- Mixed natural-language changes: `standup -p "add this today and mark
  yesterday's tasks done"`; use `standup -p -` to read the prompt from stdin.
  The whole CRUD plan succeeds or fails together, and ambiguous targets fail
  without writes. Add `--verbose` to show specialist tool calls.
- Review: `standup list` (interactive), `standup list --days 5`, `--date
  YYYY-MM-DD`, `--tag <token>`.

## Notes

- `<id>` accepts any unambiguous prefix; `list` shows short ids.
- `standup add --raw -` reads verbatim paragraphs from stdin.
- No model endpoint: `STANDUP_OFFLINE=true` — add splits paragraphs,
  generate renders deterministically. Online mode defaults to an OpenAI-compatible
  endpoint; set `STANDUP_PROVIDER=anthropic` for the Anthropic Messages API.
- Obsidian export is one-way. Configure `obsidian.vault` and optionally
  `obsidian.note` (default `Standups/{date}.md`); `{date}` is the report's
  current date in the configured timezone. Existing notes retain everything
  outside the managed `standup:start`/`standup:end` markers. The JSONL store
  remains authoritative.
- `standup doctor` sanity-checks the setup; `standup init` writes editable
  config files (meeting time, timezone, SMTP, repo globs); `standup update`
  securely installs the latest release in place (`--check` only checks).
