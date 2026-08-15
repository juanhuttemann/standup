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
  as done tasks (stamped with commit time, deduped on re-run);
  `standup commits 3` for N days, extra args are repo paths.
- Each morning: `standup generate` — yesterday + today (weekend-aware,
  cutoff at the configured meeting time), blocked tasks under
  `## Blockers`. `standup generate 5` for more days, `--from`/`--to
  YYYY-MM-DD` for an explicit window, `--clip` to copy the report.
- Status changes: `standup done <id>`, `standup status <id> blocked` (or
  `todo`, `in-progress`).
- Review: `standup list` (interactive), `standup list --days 5`, `--date
  YYYY-MM-DD`, `--tag <token>`.

## Notes

- `<id>` accepts any unambiguous prefix; `list` shows short ids.
- No model endpoint: `STANDUP_OFFLINE=true` — add splits paragraphs,
  generate renders deterministically. Online mode needs provider env vars.
- `standup doctor` sanity-checks the setup; `standup init` writes editable
  config files.
